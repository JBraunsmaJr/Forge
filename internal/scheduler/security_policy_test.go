package scheduler

import (
	"net/http"
	"testing"
)

// TestSecurity_S7_RunOrgIsNotTakenFromTheRequest covers threat model gap S7:
// the org a run belongs to decides which policies are injected, so a scoped
// token must not be able to submit a run into another org or project by
// naming it in the request body.
//
// The complementary case, a scoped token that names no org and so gets its
// own org's policies, is covered by TestResolveRunScope; it needs a database
// to observe through the API.
func TestSecurity_S7_RunOrgIsNotTakenFromTheRequest(t *testing.T) {
	_, h := newSecurityTestHandler()

	const steps = `"pipeline_name":"p","steps":[{"id":"a","image":"alpine","command":["true"]}]`

	cases := []struct {
		name  string
		token string
		body  string
	}{
		{"org token names another org", tokenOperatorOrgA, `{"org_id":"` + testOrgB + `",` + steps + `}`},
		{"project token names another project", tokenOperatorProjectA, `{"project_id":"` + testProjectB + `",` + steps + `}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := send(h, http.MethodPost, "/api/v1/runs", tc.token, tc.body)
			if !got.denied() {
				t.Errorf("POST /api/v1/runs was not refused: %s", got)
			}
		})
	}
}
