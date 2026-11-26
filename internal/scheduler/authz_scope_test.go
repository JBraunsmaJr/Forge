package scheduler

import "testing"

func TestOrgScopeFor(t *testing.T) {
	tests := []struct {
		name  string
		rec   *tokenRecord
		orgID string
		want  scopeDecision
	}{
		{"no token record", nil, "org-a", scopeDeny},
		{"unscoped token", &tokenRecord{Role: roleOperator}, "org-a", scopeAllow},
		{"org token, same org", &tokenRecord{OrgID: "org-a"}, "org-a", scopeAllow},
		{"org token, other org", &tokenRecord{OrgID: "org-a"}, "org-b", scopeDeny},
		{"project token, its own org", &tokenRecord{OrgID: "org-a", ProjectID: "p1"}, "org-a", scopeDeny},
		{"project token without an org, any org", &tokenRecord{ProjectID: "p1"}, "org-a", scopeDeny},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := orgScopeFor(tt.rec, tt.orgID); got != tt.want {
				t.Errorf("orgScopeFor() = %d; want %d", got, tt.want)
			}
		})
	}
}

func TestProjectScopeFor(t *testing.T) {
	// p1 belongs to org-a, p2 to org-b; nothing else exists.
	owners := map[string]string{"p1": "org-a", "p2": "org-b"}

	tests := []struct {
		name       string
		rec        *tokenRecord
		projectID  string
		want       scopeDecision
		wantLookup bool
	}{
		{"no token record", nil, "p1", scopeDeny, false},
		{"unscoped token", &tokenRecord{Role: roleOperator}, "p1", scopeAllow, false},
		{"unscoped token, unknown project", &tokenRecord{Role: roleOperator}, "nope", scopeAllow, false},
		{"project token, its project", &tokenRecord{OrgID: "org-a", ProjectID: "p1"}, "p1", scopeAllow, false},
		{"project token, other project in its org", &tokenRecord{OrgID: "org-a", ProjectID: "p1"}, "p3", scopeDeny, false},
		{"project token, project in another org", &tokenRecord{OrgID: "org-a", ProjectID: "p1"}, "p2", scopeDeny, false},
		{"org token, project in its org", &tokenRecord{OrgID: "org-a"}, "p1", scopeAllow, true},
		{"org token, project in another org", &tokenRecord{OrgID: "org-a"}, "p2", scopeDeny, true},
		{"org token, unknown project", &tokenRecord{OrgID: "org-a"}, "nope", scopeNotFound, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookedUp := false
			lookup := func(projectID string) (string, bool) {
				lookedUp = true
				orgID, found := owners[projectID]
				return orgID, found
			}
			if got := projectScopeFor(tt.rec, tt.projectID, lookup); got != tt.want {
				t.Errorf("projectScopeFor() = %d; want %d", got, tt.want)
			}
			if lookedUp != tt.wantLookup {
				t.Errorf("project lookup performed = %v; want %v", lookedUp, tt.wantLookup)
			}
		})
	}
}
