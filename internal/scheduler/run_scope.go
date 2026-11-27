package scheduler

import (
	"fmt"
	"net/http"
)

// runScope is the org and project a submitted run belongs to.
type runScope struct {
	OrgID     string
	ProjectID string
}

// runScopeError explains why a run submission was refused, with the HTTP
// status to answer with.
type runScopeError struct {
	Status  int
	Message string
}

// resolveRunScope decides which org and project a submitted run belongs to.
//
// The org decides which policies are injected into the run, so it is never
// taken on trust from the request. It is derived, in order of authority:
//
//  1. from the project, when the run names one: a project belongs to exactly
//     one org, and a request that names a different org is refused;
//  2. from the caller's token, when the token is scoped to an org or project:
//     a scoped token can only submit runs inside its scope, and a request
//     that names no org gets the token's org;
//  3. from the request, only for installation-wide tokens.
//
// projectOrg returns the ID of the org that owns a project and whether the
// project exists. It is called at most once, and only when a project is
// named.
func resolveRunScope(rec *tokenRecord, reqOrgID, reqProjectID string, projectOrg func(projectID string) (orgID string, found bool)) (runScope, *runScopeError) {
	if rec == nil {
		return runScope{}, &runScopeError{http.StatusForbidden, "authentication required"}
	}

	projectID := reqProjectID
	if rec.ProjectID != "" {
		switch {
		case projectID == "":
			projectID = rec.ProjectID
		case projectID != rec.ProjectID:
			return runScope{}, &runScopeError{http.StatusForbidden, "this token is not scoped to that project"}
		}
	}

	orgID := reqOrgID
	if projectID != "" {
		owner, found := projectOrg(projectID)
		if !found {
			return runScope{}, &runScopeError{http.StatusNotFound, "project not found"}
		}
		if orgID != "" && orgID != owner {
			return runScope{}, &runScopeError{
				http.StatusBadRequest,
				fmt.Sprintf("project %s does not belong to org %s", projectID, orgID),
			}
		}
		orgID = owner
	}

	if rec.OrgID != "" {
		switch {
		case orgID == "":
			orgID = rec.OrgID
		case orgID != rec.OrgID:
			return runScope{}, &runScopeError{http.StatusForbidden, "this token is not scoped to that org"}
		}
	}

	return runScope{OrgID: orgID, ProjectID: projectID}, nil
}
