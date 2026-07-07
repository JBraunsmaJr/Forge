package integration

import (
	"net/http"
	"testing"
)

// TestUnauthenticated verifies all protected endpoints return 401 without a token.
func TestUnauthenticated(t *testing.T) {
	anon := newClient("") // no token

	endpoints := []struct {
		method string
		path   string
	}{
		{"GET", "/api/v1/runs"},
		{"POST", "/api/v1/runs"},
		{"GET", "/api/v1/orgs"},
		{"GET", "/api/v1/tokens"},
	}

	for _, ep := range endpoints {
		t.Run(ep.method+" "+ep.path, func(t *testing.T) {
			resp, err := anon.do(ep.method, ep.path, nil)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("expected 401, got %d", resp.StatusCode)
			}
		})
	}
}

// TestWebUIIsPublic verifies the web UI loads without authentication.
func TestWebUIIsPublic(t *testing.T) {
	resp, err := http.Get(schedulerURL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("web UI should be public, got %d", resp.StatusCode)
	}
}

// TestWrongToken verifies an invalid token is rejected.
func TestWrongToken(t *testing.T) {
	bad := newClient("fgt_this_is_not_a_real_token")
	resp, err := bad.get("/api/v1/runs")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

// TestCreateAndRevokeToken verifies the full token lifecycle.
func TestCreateAndRevokeToken(t *testing.T) {
	// Create a new token.
	resp, err := adminClient.post("/api/v1/tokens", map[string]string{
		"name": "test-lifecycle-token",
		"role": "admin",
	})
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	requireStatus(t, resp, http.StatusCreated)

	var created struct {
		Token string `json:"token"`
		Info  struct {
			ID string `json:"id"`
		} `json:"info"`
	}
	decode(t, resp, &created)
	if created.Token == "" {
		t.Fatal("no token in response")
	}

	// The new token should work.
	newClient := newClient(created.Token)
	resp2, err := newClient.get("/api/v1/runs")
	if err != nil {
		t.Fatalf("use new token: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("new token should work, got %d", resp2.StatusCode)
	}

	// Revoke it.
	resp3, err := adminClient.delete("/api/v1/tokens/" + created.Info.ID)
	if err != nil {
		t.Fatalf("revoke token: %v", err)
	}
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusNoContent {
		t.Errorf("expected 204 on revoke, got %d", resp3.StatusCode)
	}

	// The revoked token should no longer work.
	resp4, err := newClient.get("/api/v1/runs")
	if err != nil {
		t.Fatalf("use revoked token: %v", err)
	}
	resp4.Body.Close()
	if resp4.StatusCode != http.StatusUnauthorized {
		t.Errorf("revoked token should return 401, got %d", resp4.StatusCode)
	}
}

// TestAgentTokenCannotAdminOp verifies agent-role tokens cannot perform admin operations.
func TestAgentTokenCannotAdminOp(t *testing.T) {
	// Create an agent-role token.
	resp, err := adminClient.post("/api/v1/tokens", map[string]string{
		"name": "test-agent-restrictions",
		"role": "agent",
	})
	if err != nil {
		t.Fatalf("create agent token: %v", err)
	}
	requireStatus(t, resp, http.StatusCreated)

	var created struct {
		Token string `json:"token"`
		Info  struct {
			ID string `json:"id"`
		} `json:"info"`
	}
	decode(t, resp, &created)
	defer adminClient.delete("/api/v1/tokens/" + created.Info.ID) // cleanup

	agent := newClient(created.Token)

	// Agent token should NOT be able to create orgs.
	r, _ := agent.post("/api/v1/orgs", map[string]string{"name": "should-be-denied"})
	if r != nil {
		r.Body.Close()
		if r.StatusCode != http.StatusForbidden {
			t.Errorf("agent token should not create orgs, got %d", r.StatusCode)
		}
	}

	// Agent token should NOT be able to list tokens.
	r2, _ := agent.get("/api/v1/tokens")
	if r2 != nil {
		r2.Body.Close()
		if r2.StatusCode != http.StatusForbidden {
			t.Errorf("agent token should not list tokens, got %d", r2.StatusCode)
		}
	}
}
