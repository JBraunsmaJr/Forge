package scheduler

import "net/http"

// Roles a token can carry. See docs/Roles.md for what each may do.
const (
	roleAdmin    = "admin"
	roleOperator = "operator"
	roleViewer   = "viewer"
	roleAgent    = "agent"
)

// Route guards.
//
// A guard wraps a handler at registration time (see routes.go), so the role a
// route needs is visible in the routing table and is checked before the
// handler reads the request body or touches storage. authMiddleware has
// already authenticated the caller by the time a guard runs.

// agentRoute admits tokens with the agent role. Admin tokens are also
// admitted, so that an administrator can drive the agent protocol by hand
// when diagnosing a problem.
func agentRoute(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rec, _ := r.Context().Value(contextKeyToken).(*tokenRecord)
		if rec == nil || (rec.Role != roleAgent && rec.Role != roleAdmin) {
			writeError(w, http.StatusForbidden, "agent token required")
			return
		}
		next(w, r)
	}
}

// operatorRoute admits tokens with the operator role or higher. Agent and
// viewer tokens are refused.
func operatorRoute(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireOperator(w, r) {
			return
		}
		next(w, r)
	}
}
