package scheduler

import "net/http"

// jobTokenMayAccess reports whether a request made with a job token is one a
// job is allowed to make.
//
// Job tokens are default-deny. Code inside a pipeline step is only as trusted
// as whoever can push to the repository, so a job gets the single API call a
// step needs: downloading the source of its own project, which the injected
// checkout step uses. Everything else a job produces or consumes (logs,
// artifacts, cache entries, test reports) is handled by the agent outside
// the container, with the agent's own credential.
//
// Add a route here only when a step genuinely needs it, and scope it to the
// token's own project, run, or job.
func jobTokenMayAccess(rec *tokenRecord, r *http.Request) bool {
	if rec == nil || rec.ProjectID == "" {
		return false
	}
	return r.Method == http.MethodGet && r.URL.Path == "/api/v1/source/"+rec.ProjectID
}
