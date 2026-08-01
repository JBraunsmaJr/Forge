package scheduler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/JBraunsmaJr/forge/internal/api"
)

func (s *Server) handleSSOLogin(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	state := r.URL.Query().Get("state")
	var authURL string

	switch provider {
	case "github":
		clientID := os.Getenv("FORGE_GITHUB_CLIENT_ID")
		if clientID == "" {
			writeError(w, http.StatusServiceUnavailable, "GitHub SSO not configured")
			return
		}
		redirectURI := fmt.Sprintf("%s/api/v1/auth/callback/github", s.getPublicURL())
		authURL = fmt.Sprintf("https://github.com/login/oauth/authorize?client_id=%s&redirect_uri=%s&scope=user:email&state=%s",
			url.QueryEscape(clientID), url.QueryEscape(redirectURI), url.QueryEscape(state))

	case "gitlab":
		clientID := os.Getenv("FORGE_GITLAB_CLIENT_ID")
		if clientID == "" {
			writeError(w, http.StatusServiceUnavailable, "GitLab SSO not configured")
			return
		}
		redirectURI := fmt.Sprintf("%s/api/v1/auth/callback/gitlab", s.getPublicURL())
		authURL = fmt.Sprintf("https://gitlab.com/oauth/authorize?client_id=%s&redirect_uri=%s&response_type=code&scope=read_user&state=%s",
			url.QueryEscape(clientID), url.QueryEscape(redirectURI), url.QueryEscape(state))

	default:
		writeError(w, http.StatusBadRequest, "unsupported provider")
		return
	}

	http.Redirect(w, r, authURL, http.StatusTemporaryRedirect)
}

func (s *Server) handleSSOCallback(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	code := r.URL.Query().Get("code")
	if code == "" {
		writeError(w, http.StatusBadRequest, "missing code")
		return
	}

	var email, name, externalID string

	switch provider {
	case "github":
		email, name, externalID = s.handleGitHubCallback(w, r, code)
	case "gitlab":
		email, name, externalID = s.handleGitLabCallback(w, r, code)
	default:
		writeError(w, http.StatusBadRequest, "unsupported provider")
		return
	}

	if email == "" {
		// handleGitHubCallback/handleGitLabCallback already wrote error if email is empty
		return
	}

	// 3. Link/Create user and issue Forge token
	user, err := s.store.GetOrCreateUserBySSO(provider, externalID, email, name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to sync user: "+err.Error())
		return
	}

	rawToken, _, err := s.tokens.Create(user.Name+" (SSO)", user.Role, "", "", nil, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create session: "+err.Error())
		return
	}

	// Link token to user
	s.store.db.Exec(`UPDATE api_tokens SET user_id = $1 WHERE token_hash = $2`, user.ID, hashToken(rawToken))

	// 4. Set cookie and redirect back to UI (or CLI)
	http.SetCookie(w, &http.Cookie{
		Name:     "forge_token",
		Value:    rawToken,
		Path:     "/",
		Expires:  time.Now().Add(30 * 24 * time.Hour),
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	})

	state := r.URL.Query().Get("state")
	if strings.HasPrefix(state, "http://localhost:") {
		// Redirect to CLI callback
		http.Redirect(w, r, state+"?token="+rawToken, http.StatusTemporaryRedirect)
		return
	}

	uiURL := os.Getenv("FORGE_UI_URL")
	if uiURL == "" {
		uiURL = "/"
	}
	http.Redirect(w, r, uiURL, http.StatusTemporaryRedirect)
}

func (s *Server) handleGitHubCallback(w http.ResponseWriter, r *http.Request, code string) (string, string, string) {
	clientID := os.Getenv("FORGE_GITHUB_CLIENT_ID")
	clientSecret := os.Getenv("FORGE_GITHUB_CLIENT_SECRET")

	tokenReq := map[string]string{
		"client_id":     clientID,
		"client_secret": clientSecret,
		"code":          code,
	}
	body, _ := json.Marshal(tokenReq)
	req, _ := http.NewRequest("POST", "https://github.com/login/oauth/access_token", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to exchange code: "+err.Error())
		return "", "", ""
	}
	defer resp.Body.Close()

	var tokenResp struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	json.NewDecoder(resp.Body).Decode(&tokenResp)

	if tokenResp.Error != "" {
		writeError(w, http.StatusBadRequest, "GitHub error: "+tokenResp.Error)
		return "", "", ""
	}

	req, _ = http.NewRequest("GET", "https://api.github.com/user", nil)
	req.Header.Set("Authorization", "Bearer "+tokenResp.AccessToken)
	req.Header.Set("Accept", "application/json")

	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to fetch user info: "+err.Error())
		return "", "", ""
	}
	defer resp.Body.Close()

	var githubUser struct {
		ID    int    `json:"id"`
		Login string `json:"login"`
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	json.NewDecoder(resp.Body).Decode(&githubUser)

	email := githubUser.Email
	if email == "" {
		email = s.fetchGitHubEmail(tokenResp.AccessToken)
	}

	if email == "" {
		writeError(w, http.StatusBadRequest, "could not retrieve email from GitHub")
		return "", "", ""
	}

	name := githubUser.Name
	if name == "" {
		name = githubUser.Login
	}

	return email, name, fmt.Sprint(githubUser.ID)
}

func (s *Server) handleGitLabCallback(w http.ResponseWriter, r *http.Request, code string) (string, string, string) {
	clientID := os.Getenv("FORGE_GITLAB_CLIENT_ID")
	clientSecret := os.Getenv("FORGE_GITLAB_CLIENT_SECRET")
	redirectURI := fmt.Sprintf("%s/api/v1/auth/callback/gitlab", s.getPublicURL())

	data := url.Values{}
	data.Set("client_id", clientID)
	data.Set("client_secret", clientSecret)
	data.Set("code", code)
	data.Set("grant_type", "authorization_code")
	data.Set("redirect_uri", redirectURI)

	resp, err := http.PostForm("https://gitlab.com/oauth/token", data)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to exchange code: "+err.Error())
		return "", "", ""
	}
	defer resp.Body.Close()

	var tokenResp struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	json.NewDecoder(resp.Body).Decode(&tokenResp)

	if tokenResp.Error != "" {
		writeError(w, http.StatusBadRequest, "GitLab error: "+tokenResp.Error)
		return "", "", ""
	}

	req, _ := http.NewRequest("GET", "https://gitlab.com/api/v4/user", nil)
	req.Header.Set("Authorization", "Bearer "+tokenResp.AccessToken)

	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to fetch user info: "+err.Error())
		return "", "", ""
	}
	defer resp.Body.Close()

	var gitlabUser struct {
		ID    int    `json:"id"`
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	json.NewDecoder(resp.Body).Decode(&gitlabUser)

	if gitlabUser.Email == "" {
		writeError(w, http.StatusBadRequest, "could not retrieve email from GitLab")
		return "", "", ""
	}

	return gitlabUser.Email, gitlabUser.Name, fmt.Sprint(gitlabUser.ID)
}

func (s *Server) fetchGitHubEmail(token string) string {
	req, _ := http.NewRequest("GET", "https://api.github.com/user/emails", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	var emails []struct {
		Email   string `json:"email"`
		Primary bool   `json:"primary"`
	}
	json.NewDecoder(resp.Body).Decode(&emails)

	for _, e := range emails {
		if e.Primary {
			return e.Email
		}
	}
	if len(emails) > 0 {
		return emails[0].Email
	}
	return ""
}

func (s *Server) getPublicURL() string {
	if u := os.Getenv("FORGE_PUBLIC_URL"); u != "" {
		return u
	}
	return "http://localhost:8080"
}

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	rec, ok := r.Context().Value(contextKeyToken).(*tokenRecord)
	if !ok {
		// Try extracting manually since this endpoint might be skipped by middleware
		raw := extractToken(r)
		if raw != "" {
			rec, ok = s.tokens.Verify(raw)
		}
	}

	if !ok || rec == nil {
		writeJSON(w, http.StatusOK, api.AuthStatusResponse{Authenticated: false})
		return
	}

	var user *api.UserInfo
	// Find user linked to this token
	var userID string
	err := s.store.db.QueryRow(`SELECT user_id FROM api_tokens WHERE id = $1`, rec.ID).Scan(&userID)
	if err == nil && userID != "" {
		user, _ = s.store.GetUserByID(userID)
	}
	if user == nil {
		user = &api.UserInfo{
			ID:   rec.ID,
			Name: rec.Name,
			Role: rec.Role,
		}
	}

	writeJSON(w, http.StatusOK, api.AuthStatusResponse{
		Authenticated: true,
		User:          user,
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	// Revoke the token if it's a session token
	rec, ok := r.Context().Value(contextKeyToken).(*tokenRecord)
	if !ok {
		// Try extracting manually since this endpoint might be skipped by middleware
		raw := extractToken(r)
		if raw != "" {
			rec, ok = s.tokens.Verify(raw)
		}
	}

	if ok && rec != nil {
		s.tokens.Revoke(rec.ID)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "forge_token",
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		HttpOnly: true,
		MaxAge:   -1,
	})
	w.WriteHeader(http.StatusNoContent)
}
