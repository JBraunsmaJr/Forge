package scheduler

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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
// If preset is non-empty, that value is used instead of a random one -
// useful for compose/CI environments where the token must be known in advance.
func (ts *tokenStore) Create(name, role string, expiresAt *time.Time, preset string) (rawToken string, info *api.TokenInfo, err error) {
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
		`INSERT INTO api_tokens (id, token_hash, name, role, expires_at)
		 VALUES ($1,$2,$3,$4,$5) RETURNING created_at`,
		id, hash, name, role, expiresAt,
	).Scan(&createdAt)
	if err != nil {
		return "", nil, fmt.Errorf("creating token: %w", err)
	}

	info = &api.TokenInfo{ID: id, Name: name, Role: role, CreatedAt: createdAt, ExpiresAt: expiresAt}
	return
}

// tokenRecord is the in-memory result of a successful token lookup.
type tokenRecord struct {
	ID   string
	Name string
	Role string
}

// Verify looks up a raw token by its hash. Returns (nil, false) if not found or expired.
func (ts *tokenStore) Verify(rawToken string) (*tokenRecord, bool) {
	hash := hashToken(rawToken)
	var rec tokenRecord
	var expiresAt *time.Time
	err := ts.db.QueryRow(
		`SELECT id, name, role, expires_at FROM api_tokens WHERE token_hash=$1`, hash,
	).Scan(&rec.ID, &rec.Name, &rec.Role, &expiresAt)
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
		`SELECT id, name, role, created_at, expires_at FROM api_tokens ORDER BY created_at`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var result []api.TokenInfo
	for rows.Next() {
		var t api.TokenInfo
		rows.Scan(&t.ID, &t.Name, &t.Role, &t.CreatedAt, &t.ExpiresAt)
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

		if !isAPI || isWebhook || isMetrics {
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

// requireAdmin rejects agent tokens for admin-only endpoints.
func requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if agentOnly(r) {
		writeError(w, http.StatusForbidden, "admin token required")
		return false
	}
	return true
}

// extractToken reads the token from the Authorization header or ?token= param
// Query param is needed for browser EventSource which can't send custom headers.
func extractToken(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	return r.URL.Query().Get("token")
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
		`INSERT INTO api_tokens (id, token_hash, name, role, expires_at) VALUES ($1,$2,'root','admin', NULL)`,
		id, hash,
	)

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
	raw, info, err := s.tokens.Create(req.Name, req.Role, req.ExpiresAt, req.Preset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	fmt.Printf("[auth] token created: %s (%s)\n", info.Name, info.Role)
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
	w.WriteHeader(http.StatusNoContent)
}
