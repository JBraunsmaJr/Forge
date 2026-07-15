package scheduler

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/JBraunsmaJr/forge/internal/api"
)

type tokenStore struct {
	db *sql.DB
}

func newTokenStore(db *sql.DB) *tokenStore {
	return &tokenStore{db: db}
}

// Create generates a new token, stores its hash, and returns the raw value.
func (ts *tokenStore) Create(name, role string, orgID, projectID string, expiresAt *time.Time, preset string) (rawToken string, info *api.TokenInfo, err error) {
	if role == "" {
		role = "admin"
	}

	if preset != "" {
		rawToken = preset
	} else {
		b := make([]byte, 32)
		if _, err = rand.Read(b); err != nil {
			return
		}
		rawToken = "fgt_" + hex.EncodeToString(b)
	}

	hash := hashToken(rawToken)
	id := newID()[:12]

	var createdAt time.Time
	err = ts.db.QueryRow(
		`INSERT INTO api_tokens (id, token_hash, name, role, org_id, project_id, expires_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING created_at`,
		id, hash, name, role, orgID, projectID, expiresAt,
	).Scan(&createdAt)
	if err != nil {
		return "", nil, fmt.Errorf("creating token: %w", err)
	}

	info = &api.TokenInfo{
		ID:        id,
		Name:      name,
		Role:      role,
		OrgID:     orgID,
		ProjectID: projectID,
		CreatedAt: createdAt,
		ExpiresAt: expiresAt,
	}
	return
}

// tokenRecord is the in-memory result of a successful token lookup.
type tokenRecord struct {
	ID        string
	Name      string
	Role      string
	OrgID     string
	ProjectID string
}

// Verify looks up a raw token by its hash. Returns (nil, false) if not found or expired.
func (ts *tokenStore) Verify(rawToken string) (*tokenRecord, bool) {
	hash := hashToken(rawToken)
	var rec tokenRecord
	var expiresAt *time.Time
	err := ts.db.QueryRow(
		`SELECT id, name, role, COALESCE(org_id, ''), COALESCE(project_id, ''), expires_at FROM api_tokens WHERE token_hash=$1`, hash,
	).Scan(&rec.ID, &rec.Name, &rec.Role, &rec.OrgID, &rec.ProjectID, &expiresAt)
	if err != nil {
		return nil, false
	}
	if isTokenExpired(expiresAt) {
		return nil, false
	}
	return &rec, true
}

func isTokenExpired(expiresAt *time.Time) bool {
	return expiresAt != nil && expiresAt.Before(time.Now())
}

// List returns all tokens (without their hash or raw value).
func (ts *tokenStore) List() []api.TokenInfo {
	rows, err := ts.db.Query(
		`SELECT id, name, role, COALESCE(org_id, ''), COALESCE(project_id, ''), created_at, expires_at FROM api_tokens ORDER BY created_at`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var result []api.TokenInfo
	for rows.Next() {
		var t api.TokenInfo
		rows.Scan(&t.ID, &t.Name, &t.Role, &t.OrgID, &t.ProjectID, &t.CreatedAt, &t.ExpiresAt)
		result = append(result, t)
	}
	return result
}

// Revoke deletes a token by ID.
func (ts *tokenStore) Revoke(id string) error {
	res, err := ts.db.Exec(`DELETE FROM api_tokens WHERE id=$1`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("token %s not found", id)
	}
	return nil
}

// Count returns the total number of tokens — used for bootstrap detection.
func (ts *tokenStore) Count() int {
	var n int
	ts.db.QueryRow(`SELECT COUNT(*) FROM api_tokens`).Scan(&n)
	return n
}

func hashToken(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

type contextKeyType string

const contextKeyToken = contextKeyType("forge_token")

// authMiddleware enforces token authentication on all endpoints except:
// - Web UI assets and SPA routes (anything not starting with /api/v1/)
// - GET /metrics - Promethus scraping
// - POST /api/v1/webhook/* - HMAC secured per project
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		// public paths - no token required.
		isAPI := strings.HasPrefix(r.URL.Path, "/api/v1/")
		isWebhook := strings.HasPrefix(r.URL.Path, "/api/v1/webhook/")
		isMetrics := r.URL.Path == "/metrics"
		isAuth := strings.HasPrefix(r.URL.Path, "/api/v1/auth/")

		if !isAPI || isWebhook || isMetrics || isAuth {
			next.ServeHTTP(w, r)
			return
		}

		raw := extractToken(r)
		if raw == "" {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("WWW-Authenticate", `Bearer realm="Forge"`)
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprintf(w, `{"error":"authentication required — set FORGE_API_TOKEN"}`)
			return
		}

		rec, ok := s.tokens.Verify(raw)
		if !ok {
			ip := r.RemoteAddr
			if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
				ip = strings.Split(forwarded, ",")[0]
			}
			prefix := ""
			if len(raw) >= 8 {
				prefix = raw[:8]
			}
			s.store.db.Exec(`
				INSERT INTO audit_logs (id, action, details, ip_address)
				VALUES ($1, $2, $3, $4)`,
				newID(), "auth.failure", []byte(fmt.Sprintf(`{"token_prefix":"%s"}`, prefix)), ip)

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprintf(w, `{"error":"invalid or revoked token"}`)
			return
		}

		// Attach token info to the context for downstream handlers.
		ctx := context.WithValue(r.Context(), contextKeyToken, rec)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// agentOnly rejects requests from tokens that don't have admin role when
// the operation is admin-only. Agent tokens can only touch the job queue.
func agentOnly(r *http.Request) bool {
	rec, _ := r.Context().Value(contextKeyToken).(*tokenRecord)
	return rec != nil && rec.Role == "agent"
}

// hasRole checks if the current token has at least the required role.
// Hierarchy: admin (100) > operator (50) > viewer (10)
func hasRole(r *http.Request, required string) bool {
	rec, _ := r.Context().Value(contextKeyToken).(*tokenRecord)
	if rec == nil {
		return false
	}
	if rec.Role == "admin" {
		return true
	}
	weights := map[string]int{
		"admin":    100,
		"operator": 50,
		"viewer":   10,
		"agent":    0,
	}
	return weights[rec.Role] >= weights[required]
}

// requireAdmin rejects non-admin tokens for admin-only endpoints.
func requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if !hasRole(r, "admin") {
		writeError(w, http.StatusForbidden, "admin token required")
		return false
	}
	return true
}

// requireOperator rejects viewer tokens for write operations.
func requireOperator(w http.ResponseWriter, r *http.Request) bool {
	if !hasRole(r, "operator") {
		writeError(w, http.StatusForbidden, "operator permission required")
		return false
	}
	return true
}

// extractToken reads the token from the Authorization header, ?token= param, or forge_token cookie.
// Query param and cookie are needed for browser-based access and EventSource.
func extractToken(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	if t := r.URL.Query().Get("token"); t != "" {
		return t
	}
	if cookie, err := r.Cookie("forge_token"); err == nil {
		return cookie.Value
	}
	return ""
}

// bootstrapIfEmpty creates an initial admin token on first run if none exist.
// If FORGE_ROOT_TOKEN is set, that value is used as the token - useful for
// reproducible dev/CI environments where the token must be known in advance.
// Otherwise, a cryptographically random token is generated and printed once.
func (ts *tokenStore) bootstrapIfEmpty() {
	if ts.Count() > 0 {
		return
	}

	preset := os.Getenv("FORGE_ROOT_TOKEN")

	var rawToken string
	if preset != "" {
		rawToken = preset
	} else {
		b := make([]byte, 32)
		rand.Read(b)
		rawToken = "fgt_" + hex.EncodeToString(b)
	}

	hash := hashToken(rawToken)
	id := newID()[:12]
	ts.db.Exec(
		`INSERT INTO api_tokens (id, token_hash, name, role, org_id, project_id, expires_at) VALUES ($1,$2,'root','admin', '', '', NULL)`,
		id, hash,
	)

	// Also bootstrap an agent token if provided via environment.
	// This ensures that distributed deployments (where init.sh might not run)
	// can still have authenticated agents.
	agentToken := os.Getenv("FORGE_AGENT_TOKEN")
	if agentToken == "" {
		agentToken = os.Getenv("FORGE_API_TOKEN")
	}

	if agentToken != "" && agentToken != rawToken {
		ahash := hashToken(agentToken)
		aid := newID()[:12]
		ts.db.Exec(
			`INSERT INTO api_tokens (id, token_hash, name, role, org_id, project_id, expires_at) VALUES ($1,$2,'default-agent','agent', '', '', NULL)`,
			aid, ahash,
		)
		fmt.Printf("[auth] agent token initialised from environment\n")
	}

	if preset != "" {
		fmt.Printf("[auth] root token initialised from FORGE_ROOT_TOKEN\n")
		return
	}

	// Auto-generated token - print once.
	w := 58
	bar := strings.Repeat("─", w)
	pad := func(s string) string {
		sp := w - len(s) - 1
		if sp < 0 {
			sp = 0
		}
		return "│ " + s + strings.Repeat(" ", sp) + "│"
	}
	fmt.Printf("\n┌%s┐\n", bar)
	fmt.Printf("%s\n", pad("First run — root admin token created"))
	fmt.Printf("%s\n", pad(""))
	fmt.Printf("%s\n", pad("Token:  "+rawToken))
	fmt.Printf("%s\n", pad(""))
	fmt.Printf("%s\n", pad("⚠ This is shown ONCE. Store it securely."))
	fmt.Printf("└%s┘\n\n", bar)
	fmt.Printf("Set for this session:\n  $env:FORGE_API_TOKEN = '%s'\n\n", rawToken)
}

func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var req api.CreateTokenRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	raw, info, err := s.tokens.Create(req.Name, req.Role, req.OrgID, req.ProjectID, req.ExpiresAt, req.Preset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	fmt.Printf("[auth] token created: %s (%s)\n", info.Name, info.Role)
	s.AuditLog(r, "token.create", "token", info.ID, map[string]any{"name": info.Name, "role": info.Role, "org_id": info.OrgID, "project_id": info.ProjectID})
	writeJSON(w, http.StatusCreated, api.CreateTokenResponse{Token: raw, Info: *info})
}

func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, s.tokens.List())
}

func (s *Server) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if err := s.tokens.Revoke(r.PathValue("id")); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	fmt.Printf("[auth] token %s revoked\n", r.PathValue("id"))
	s.AuditLog(r, "token.revoke", "token", r.PathValue("id"), nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) AuditLog(r *http.Request, action, targetType, targetID string, details any) {
	rec, _ := r.Context().Value(contextKeyToken).(*tokenRecord)
	var actorID, actorName, orgID string
	if rec != nil {
		actorID = rec.ID
		actorName = rec.Name
		orgID = rec.OrgID
	}

	ip := r.RemoteAddr
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		ip = strings.Split(forwarded, ",")[0]
	}

	// Details can also contain org_id override
	if m, ok := details.(map[string]any); ok {
		if o, ok := m["org_id"].(string); ok && o != "" {
			orgID = o
		}
	} else if m, ok := details.(map[string]string); ok {
		if o, ok := m["org_id"]; ok && o != "" {
			orgID = o
		}
	}

	detailsJSON, _ := json.Marshal(details)
	s.store.db.Exec(`
		INSERT INTO audit_logs (id, actor_id, actor_name, action, target_type, target_id, details, ip_address, org_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		newID(), actorID, actorName, action, targetType, targetID, detailsJSON, ip, orgID)
}
