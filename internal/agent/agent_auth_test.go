package agent

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleTerminalWS_Auth(t *testing.T) {
	a := &Agent{
		apiToken: "valid-token",
	}

	tests := []struct {
		name       string
		authHeader string
		wantCode   int
	}{
		{"Valid Token", "Bearer valid-token", 0},
		{"Invalid Token", "Bearer wrong", http.StatusUnauthorized},
		{"Missing Header", "", http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/debug/session123/ws", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			w := httptest.NewRecorder()

			a.handleTerminalWS(w, req)

			if w.Code == http.StatusUnauthorized {
				if tt.wantCode == 0 {
					t.Errorf("expected success (auth pass), got 401")
				}
			} else if tt.wantCode != 0 && w.Code != tt.wantCode {
				t.Errorf("expected status %d, got %d", tt.wantCode, w.Code)
			}
		})
	}
}
