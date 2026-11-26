//go:build security

package scheduler

import (
	"net/http"
	"testing"
)

// TestSecurity_S2_AgentEndpointsRequireAgentRole covers threat model gap S2:
// endpoints that exist for agents must refuse every other role. A viewer
// token must not be able to lease jobs, report results, write logs, poison
// the cache, or upload artifacts.
func TestSecurity_S2_AgentEndpointsRequireAgentRole(t *testing.T) {
	_, h := newSecurityTestHandler()

	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"lease a job", http.MethodPost, "/api/v1/jobs/lease", `{"agent_id":"agent-0000000001"}`},
		{"heartbeat a job", http.MethodPost, "/api/v1/jobs/job-1/heartbeat", `{"lease_id":"lease-1","agent_id":"agent-0000000001"}`},
		{"complete a job", http.MethodPost, "/api/v1/jobs/job-1/complete", `{"lease_id":"lease-1","exit_code":0}`},
		{"append job logs", http.MethodPost, "/api/v1/jobs/job-1/logs", `{"lease_id":"lease-1","events":[{"level":"INFO","message":"x"}]}`},
		{"store a cache entry", http.MethodPost, "/api/v1/cache", `{"task_hash":"abc","exit_code":0}`},
		{"presign an artifact upload", http.MethodPost, "/api/v1/artifacts/presign", `{"run_id":"run-1","job_id":"job-1","name":"a","filename":"a.txt"}`},
		{"record a test report", http.MethodPost, "/api/v1/test-reports", `{}`},
		{"lease a debug session", http.MethodPost, "/api/v1/debug/lease", `{"agent_id":"agent-0000000001"}`},
		{"submit debug output", http.MethodPost, "/api/v1/debug/session-1/output", `{"command_id":"c","output":"x"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := send(h, tc.method, tc.path, tokenViewer, tc.body)
			if !got.denied() {
				t.Errorf("viewer token: %s %s was not refused: %s", tc.method, tc.path, got)
			}
		})
	}
}

// TestSecurity_S6_DebugSessionsRequireOperator covers the debug-session half
// of threat model gap S6: opening a shell on an agent, or sending input to
// one, changes state and must require the operator role. Viewer tokens and
// agent tokens must both be refused.
func TestSecurity_S6_DebugSessionsRequireOperator(t *testing.T) {
	_, h := newSecurityTestHandler()

	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"create a debug session", http.MethodPost, "/api/v1/debug", `{"job_id":"job-1"}`},
		{"run a debug command", http.MethodPost, "/api/v1/debug/session-1/exec", `{"input":"id"}`},
		{"close a debug session", http.MethodDelete, "/api/v1/debug/session-1", ``},
	}

	for _, token := range []string{tokenViewer, tokenAgent} {
		for _, tc := range cases {
			t.Run(token+"/"+tc.name, func(t *testing.T) {
				got := send(h, tc.method, tc.path, token, tc.body)
				if !got.denied() {
					t.Errorf("%s: %s %s was not refused: %s", token, tc.method, tc.path, got)
				}
			})
		}
	}
}
