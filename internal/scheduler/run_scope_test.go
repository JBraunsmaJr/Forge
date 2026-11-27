package scheduler

import (
	"net/http"
	"testing"
)

func TestResolveRunScope(t *testing.T) {
	// p1 belongs to org-a, p2 to org-b, and p-free to no org.
	owners := map[string]string{"p1": "org-a", "p2": "org-b", "p-free": ""}

	unscoped := &tokenRecord{Role: roleOperator}
	orgA := &tokenRecord{Role: roleOperator, OrgID: "org-a"}
	projectP1 := &tokenRecord{Role: roleOperator, OrgID: "org-a", ProjectID: "p1"}

	tests := []struct {
		name       string
		rec        *tokenRecord
		reqOrg     string
		reqProject string
		want       runScope
		wantStatus int // 0 means the request is accepted
	}{
		{"no token record", nil, "", "", runScope{}, http.StatusForbidden},

		// Installation-wide tokens.
		{"unscoped, nothing named", unscoped, "", "", runScope{}, 0},
		{"unscoped, org named", unscoped, "org-a", "", runScope{OrgID: "org-a"}, 0},
		{"unscoped, project named: org comes from the project", unscoped, "", "p1", runScope{OrgID: "org-a", ProjectID: "p1"}, 0},
		{"unscoped, project and its own org named", unscoped, "org-a", "p1", runScope{OrgID: "org-a", ProjectID: "p1"}, 0},
		{"unscoped, project named with another org", unscoped, "org-b", "p1", runScope{}, http.StatusBadRequest},
		{"unscoped, project without an org", unscoped, "", "p-free", runScope{ProjectID: "p-free"}, 0},
		{"unscoped, org claimed for a project without one", unscoped, "org-a", "p-free", runScope{}, http.StatusBadRequest},
		{"unscoped, unknown project", unscoped, "", "nope", runScope{}, http.StatusNotFound},

		// Org-scoped tokens: the policy-bypass cases.
		{"org token, org omitted: gets the token's org", orgA, "", "", runScope{OrgID: "org-a"}, 0},
		{"org token, its own org", orgA, "org-a", "", runScope{OrgID: "org-a"}, 0},
		{"org token, another org", orgA, "org-b", "", runScope{}, http.StatusForbidden},
		{"org token, project in its org", orgA, "", "p1", runScope{OrgID: "org-a", ProjectID: "p1"}, 0},
		{"org token, project in another org", orgA, "", "p2", runScope{}, http.StatusForbidden},
		{"org token, project without an org", orgA, "", "p-free", runScope{OrgID: "org-a", ProjectID: "p-free"}, 0},

		// Project-scoped tokens.
		{"project token, nothing named: gets its project and org", projectP1, "", "", runScope{OrgID: "org-a", ProjectID: "p1"}, 0},
		{"project token, its own project", projectP1, "", "p1", runScope{OrgID: "org-a", ProjectID: "p1"}, 0},
		{"project token, another project", projectP1, "", "p2", runScope{}, http.StatusForbidden},
		{"project token, another org named", projectP1, "org-b", "", runScope{}, http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookups := 0
			lookup := func(projectID string) (string, bool) {
				lookups++
				orgID, found := owners[projectID]
				return orgID, found
			}

			got, scopeErr := resolveRunScope(tt.rec, tt.reqOrg, tt.reqProject, lookup)

			if tt.wantStatus == 0 {
				if scopeErr != nil {
					t.Fatalf("refused with %d %q; want accepted", scopeErr.Status, scopeErr.Message)
				}
				if got != tt.want {
					t.Errorf("scope = %+v; want %+v", got, tt.want)
				}
			} else {
				if scopeErr == nil {
					t.Fatalf("accepted as %+v; want status %d", got, tt.wantStatus)
				}
				if scopeErr.Status != tt.wantStatus {
					t.Errorf("status = %d (%q); want %d", scopeErr.Status, scopeErr.Message, tt.wantStatus)
				}
			}
			if lookups > 1 {
				t.Errorf("project looked up %d times; want at most once", lookups)
			}
		})
	}
}
