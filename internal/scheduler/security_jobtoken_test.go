package scheduler

import (
	"net/http"
	"testing"
)

// TestSecurity_S2_JobTokenIsConfined covers the job-token half of threat
// model gap S2: the credential a job receives must not be able to do what an
// agent, an operator, or even a viewer can. It may fetch its own project's
// source and nothing else.
func TestSecurity_S2_JobTokenIsConfined(t *testing.T) {
	s, h := newSecurityTestHandler()
	s.jobTokens = newTestJobTokenSigner(t)

	jobToken, err := s.jobTokens.Mint(jobTokenClaims{
		JobID: "job-1", RunID: "run-1", ProjectID: testProjectA, OrgID: testOrgA,
	})
	if err != nil {
		t.Fatalf("minting a job token: %v", err)
	}

	refused := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"lease a job", http.MethodPost, "/api/v1/jobs/lease", `{"agent_id":"agent-0000000001"}`},
		{"complete another job", http.MethodPost, "/api/v1/jobs/job-2/complete", `{"lease_id":"lease-1","exit_code":0}`},
		{"store a cache entry", http.MethodPost, "/api/v1/cache", `{"task_hash":"abc","exit_code":0}`},
		{"submit a run", http.MethodPost, "/api/v1/runs", `{"name":"x","steps":[]}`},
		{"list runs", http.MethodGet, "/api/v1/runs", ``},
		{"read a run's logs", http.MethodGet, "/api/v1/runs/run-2/logs", ``},
		{"list its own project's secrets", http.MethodGet, "/api/v1/projects/" + testProjectA + "/secrets", ``},
		{"list its own org's secrets", http.MethodGet, "/api/v1/orgs/" + testOrgA + "/secrets", ``},
		{"open a debug session", http.MethodPost, "/api/v1/debug", `{"job_id":"job-1"}`},
		{"list API tokens", http.MethodGet, "/api/v1/tokens", ``},
		{"download another project's source", http.MethodGet, "/api/v1/source/" + testProjectB + "?commit=abc1234", ``},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			got := send(h, tc.method, tc.path, jobToken, tc.body)
			if !got.denied() {
				t.Errorf("job token: %s %s was not refused: %s", tc.method, tc.path, got)
			}
		})
	}

	t.Run("download its own project's source", func(t *testing.T) {
		got := send(h, http.MethodGet, "/api/v1/source/"+testProjectA+"?commit=abc1234", jobToken, ``)
		if got.denied() {
			t.Errorf("job token was refused its own project's source: %s", got)
		}
	})

	t.Run("a tampered job token is not accepted", func(t *testing.T) {
		if rec, ok := s.authenticate(jobToken + "x"); ok {
			t.Errorf("a tampered job token was authenticated as %+v", rec)
		}
	})
}
