package scheduler

import "net/http"

// Scope guards.
//
// A token may be limited to one org, or to one project inside an org. The
// guards below enforce that limit on routes that name their org or project in
// the URL as {id}. Like the role guards in authz.go, they wrap the handler in
// routes.go and run before it. A token with no scope is installation-wide
// and passes every scope guard; its role still applies.
//
// Routes that identify their target some other way (a run, job, or artifact
// ID, or a field in the request body) are not covered here.

// scopeDecision is the outcome of checking a token's scope against a target.
type scopeDecision int

const (
	scopeAllow scopeDecision = iota
	scopeDeny
	// scopeNotFound means the target had to be looked up to decide and does
	// not exist.
	scopeNotFound
)

// orgScopeFor decides whether a token may act on org-level resources of
// orgID. A project-scoped token never may: it is confined to its project.
func orgScopeFor(rec *tokenRecord, orgID string) scopeDecision {
	if rec == nil || rec.ProjectID != "" {
		return scopeDeny
	}
	if rec.OrgID == "" || rec.OrgID == orgID {
		return scopeAllow
	}
	return scopeDeny
}

// projectScopeFor decides whether a token may act on projectID. A
// project-scoped token is compared directly. An org-scoped token needs to
// know which org owns the project, so projectOrg is called for it, and only
// then; it returns the owning org's ID and whether the project exists.
func projectScopeFor(rec *tokenRecord, projectID string, projectOrg func(projectID string) (orgID string, found bool)) scopeDecision {
	switch {
	case rec == nil:
		return scopeDeny
	case rec.ProjectID != "":
		if rec.ProjectID == projectID {
			return scopeAllow
		}
		return scopeDeny
	case rec.OrgID != "":
		orgID, found := projectOrg(projectID)
		if !found {
			return scopeNotFound
		}
		if orgID == rec.OrgID {
			return scopeAllow
		}
		return scopeDeny
	default:
		return scopeAllow
	}
}

// orgScoped guards a route whose {id} path value is an org ID.
func (s *Server) orgScoped(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rec, _ := r.Context().Value(contextKeyToken).(*tokenRecord)
		if orgScopeFor(rec, r.PathValue("id")) != scopeAllow {
			writeError(w, http.StatusForbidden, "this token is not scoped to that org")
			return
		}
		next(w, r)
	}
}

// projectScoped guards a route whose {id} path value is a project ID.
func (s *Server) projectScoped(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rec, _ := r.Context().Value(contextKeyToken).(*tokenRecord)
		switch projectScopeFor(rec, r.PathValue("id"), s.projectOrg) {
		case scopeAllow:
			next(w, r)
		case scopeNotFound:
			writeError(w, http.StatusNotFound, "project not found")
		default:
			writeError(w, http.StatusForbidden, "this token is not scoped to that project")
		}
	}
}

// projectOrg returns the ID of the org that owns a project.
func (s *Server) projectOrg(projectID string) (string, bool) {
	proj, _, _, ok := s.projects.GetProject(projectID)
	if !ok {
		return "", false
	}
	return proj.OrgID, true
}
