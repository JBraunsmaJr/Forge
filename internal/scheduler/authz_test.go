package scheduler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRouteGuards(t *testing.T) {
	const noToken = ""

	tests := []struct {
		name  string
		guard func(http.HandlerFunc) http.HandlerFunc
		role  string
		want  int
	}{
		{"agentRoute admits agent", agentRoute, roleAgent, http.StatusOK},
		{"agentRoute admits admin", agentRoute, roleAdmin, http.StatusOK},
		{"agentRoute refuses operator", agentRoute, roleOperator, http.StatusForbidden},
		{"agentRoute refuses viewer", agentRoute, roleViewer, http.StatusForbidden},
		{"agentRoute refuses a request with no token record", agentRoute, noToken, http.StatusForbidden},

		{"operatorRoute admits operator", operatorRoute, roleOperator, http.StatusOK},
		{"operatorRoute admits admin", operatorRoute, roleAdmin, http.StatusOK},
		{"operatorRoute refuses viewer", operatorRoute, roleViewer, http.StatusForbidden},
		{"operatorRoute refuses agent", operatorRoute, roleAgent, http.StatusForbidden},
		{"operatorRoute refuses a request with no token record", operatorRoute, noToken, http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reached := false
			h := tt.guard(func(w http.ResponseWriter, r *http.Request) {
				reached = true
				w.WriteHeader(http.StatusOK)
			})

			req := httptest.NewRequest(http.MethodPost, "/", nil)
			if tt.role != noToken {
				ctx := context.WithValue(req.Context(), contextKeyToken, &tokenRecord{Role: tt.role})
				req = req.WithContext(ctx)
			}
			rec := httptest.NewRecorder()
			h(rec, req)

			if rec.Code != tt.want {
				t.Errorf("status = %d; want %d", rec.Code, tt.want)
			}
			if wantReached := tt.want == http.StatusOK; reached != wantReached {
				t.Errorf("handler reached = %v; want %v", reached, wantReached)
			}
		})
	}
}
