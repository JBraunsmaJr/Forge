//go:build security

package scheduler

import (
	"net/http"
	"testing"
)

// TestSecurity_S5_GenericWebhookRequiresACredential covers threat model gap
// S5: the webhook endpoints are exempt from token authentication, so each
// must verify a credential of its own. The GitHub and GitLab endpoints check
// a signature or secret token; the generic endpoint checks nothing.
//
// A request to the generic endpoint that carries no signature and no token
// has nothing to verify, so it must be refused before any project lookup.
func TestSecurity_S5_GenericWebhookRequiresACredential(t *testing.T) {
	_, h := newSecurityTestHandler()

	paths := []string{
		"/api/v1/webhook/generic/" + testProjectA,
		"/api/v1/webhook/generic/" + testProjectA + "?branch=main&commit=HEAD",
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			got := send(h, http.MethodPost, path, "", `{}`)
			if !got.denied() {
				t.Errorf("POST %s with no credential was not refused: %s", path, got)
			}
		})
	}
}
