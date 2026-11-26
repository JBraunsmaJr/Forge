package scheduler

// Shared fixtures for the security regression suite.
//
// The suite tracks the gaps listed in docs/Threat-Model.md. Each test states a
// security objective, named TestSecurity_<gap ID>_..., and guards against that
// gap reopening. The tests are part of the default test run; `make
// test-security` runs them on their own.
//
// The suite runs without a database. Requests go through the real routing
// table and authMiddleware, with a fake token verifier in place of the token
// store. Because the stores are nil, a handler that gets as far as touching
// one panics; the helpers below treat that the same as any other response
// that is not a denial. This is deliberate: authorization must be decided
// before a request reaches storage.

import (
	"net/http"
	"net/http/httptest"
	"strings"
)

// Raw tokens understood by the fake verifier.
const (
	tokenViewer           = "test-viewer"
	tokenOperator         = "test-operator"
	tokenAgent            = "test-agent"
	tokenOperatorOrgA     = "test-operator-org-a"
	tokenOperatorProjectA = "test-operator-project-a"
)

const (
	testOrgA     = "org-a"
	testOrgB     = "org-b"
	testProjectA = "project-a"
	testProjectB = "project-b"
)

// fakeVerifier is a tokenVerifier backed by a map of raw token to record.
type fakeVerifier map[string]*tokenRecord

func (f fakeVerifier) Verify(rawToken string) (*tokenRecord, bool) {
	rec, ok := f[rawToken]
	return rec, ok
}

// newSecurityTestHandler returns a Server with no storage behind it and the
// HTTP handler that Start would serve: the full routing table wrapped in
// authMiddleware.
func newSecurityTestHandler() (*Server, http.Handler) {
	s := &Server{
		agents: newAgentRegistry(),
		debug:  newDebugStore(),
		authn: fakeVerifier{
			tokenViewer:           {ID: "t-viewer", Name: "viewer", Role: "viewer"},
			tokenOperator:         {ID: "t-operator", Name: "operator", Role: "operator"},
			tokenAgent:            {ID: "t-agent", Name: "agent", Role: "agent"},
			tokenOperatorOrgA:     {ID: "t-op-org-a", Name: "operator-org-a", Role: "operator", OrgID: testOrgA},
			tokenOperatorProjectA: {ID: "t-op-proj-a", Name: "operator-project-a", Role: "operator", OrgID: testOrgA, ProjectID: testProjectA},
		},
	}
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	return s, s.authMiddleware(mux)
}

// outcome is how the server answered one request.
type outcome struct {
	status   int
	panicked bool
}

// denied reports whether the request was refused with 401 or 403.
func (o outcome) denied() bool {
	return !o.panicked && (o.status == http.StatusUnauthorized || o.status == http.StatusForbidden)
}

// String describes the outcome for a failure message.
func (o outcome) String() string {
	if o.panicked {
		return "the handler ran and reached storage"
	}
	return "HTTP " + http.StatusText(o.status)
}

// send issues one request against h and reports the outcome. An empty token
// sends no Authorization header. A panic inside the handler is recovered and
// reported, because with nil stores it means the request was not refused.
func send(h http.Handler, method, path, token, body string) (out outcome) {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()

	defer func() {
		if recover() != nil {
			out = outcome{panicked: true}
		}
	}()
	h.ServeHTTP(rec, req)
	return outcome{status: rec.Code}
}
