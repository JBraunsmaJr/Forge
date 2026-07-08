package scheduler

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAuthMiddleware_PublicPaths(t *testing.T) {
	s := &Server{
		tokens: &tokenStore{db: nil}, // verify won't be called for public paths
	}

	dummyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mw := s.authMiddleware(dummyHandler)

	tests := []struct {
		name       string
		path       string
		wantStatus int
	}{
		{"Root", "/", http.StatusOK},
		{"Metrics", "/metrics", http.StatusOK},
		{"Webhook GitHub", "/api/v1/webhook/github/123", http.StatusOK},
		{"Webhook GitLab", "/api/v1/webhook/gitlab/456", http.StatusOK},
		{"Protected Path", "/api/v1/projects", http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.path, nil)
			rr := httptest.NewRecorder()
			mw.ServeHTTP(rr, req)

			if rr.Code != tt.wantStatus {
				t.Errorf("path %s: expected status %d, got %d", tt.path, tt.wantStatus, rr.Code)
			}
		})
	}
}

func TestIsTokenExpired(t *testing.T) {
	now := time.Now()
	past := now.Add(-1 * time.Hour)
	future := now.Add(1 * time.Hour)

	tests := []struct {
		name      string
		expiresAt *time.Time
		want      bool
	}{
		{"No Expiry", nil, false},
		{"Past Expiry", &past, true},
		{"Future Expiry", &future, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTokenExpired(tt.expiresAt); got != tt.want {
				t.Errorf("isTokenExpired() = %v, want %v", got, tt.want)
			}
		})
	}
}
