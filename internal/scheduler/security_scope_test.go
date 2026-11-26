package scheduler

import (
	"net/http"
	"testing"
)

// TestSecurity_S3_TokenScopeIsEnforced covers threat model gap S3: a token
// issued for one org or project must not act on another, whatever its role.
//
// Every case here names the target org or project in the URL, so the request
// can be refused by comparing the path with the token's scope. Requests that
// need a lookup to find their org (runs, jobs, artifacts) are covered by the
// database-backed integration tests.
func TestSecurity_S3_TokenScopeIsEnforced(t *testing.T) {
	_, h := newSecurityTestHandler()

	const secretBody = `{"name":"REGISTRY_PASSWORD","value":"x"}`

	cases := []struct {
		name   string
		token  string
		method string
		path   string
		body   string
	}{
		{"org token sets another org's secret", tokenOperatorOrgA, http.MethodPost, "/api/v1/orgs/" + testOrgB + "/secrets", secretBody},
		{"org token lists another org's secrets", tokenOperatorOrgA, http.MethodGet, "/api/v1/orgs/" + testOrgB + "/secrets", ``},
		{"org token deletes another org's secret", tokenOperatorOrgA, http.MethodDelete, "/api/v1/orgs/" + testOrgB + "/secrets/REGISTRY_PASSWORD", ``},
		{"org token creates a policy in another org", tokenOperatorOrgA, http.MethodPost, "/api/v1/orgs/" + testOrgB + "/policies", `{"name":"p"}`},
		{"org token lists another org's policies", tokenOperatorOrgA, http.MethodGet, "/api/v1/orgs/" + testOrgB + "/policies", ``},
		{"project token sets another project's secret", tokenOperatorProjectA, http.MethodPost, "/api/v1/projects/" + testProjectB + "/secrets", secretBody},
		{"project token lists another project's secrets", tokenOperatorProjectA, http.MethodGet, "/api/v1/projects/" + testProjectB + "/secrets", ``},
		{"project token triggers another project", tokenOperatorProjectA, http.MethodPost, "/api/v1/projects/" + testProjectB + "/trigger", `{"branch":"main"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := send(h, tc.method, tc.path, tc.token, tc.body)
			if !got.denied() {
				t.Errorf("%s %s was not refused: %s", tc.method, tc.path, got)
			}
		})
	}
}
