package scheduler

import (
	"testing"

	"github.com/JBraunsmaJr/forge/internal/api"
)

func TestValidateSteps(t *testing.T) {
	tests := []struct {
		name    string
		steps   []api.StepDef
		wantErr bool
		errSub  string
	}{
		{
			name: "valid linear",
			steps: []api.StepDef{
				{ID: "build"},
				{ID: "test", DependsOn: []string{"build"}},
			},
			wantErr: false,
		},
		{
			name: "duplicate ID",
			steps: []api.StepDef{
				{ID: "build"},
				{ID: "build"},
			},
			wantErr: true,
			errSub:  "duplicate step ID",
		},
		{
			name: "missing dependency",
			steps: []api.StepDef{
				{ID: "test", DependsOn: []string{"build"}},
			},
			wantErr: true,
			errSub:  "depends on non-existent step",
		},
		{
			name: "self cycle",
			steps: []api.StepDef{
				{ID: "build", DependsOn: []string{"build"}},
			},
			wantErr: true,
			errSub:  "cycle detected",
		},
		{
			name: "circular cycle",
			steps: []api.StepDef{
				{ID: "A", DependsOn: []string{"B"}},
				{ID: "B", DependsOn: []string{"A"}},
			},
			wantErr: true,
			errSub:  "cycle detected",
		},
		{
			name: "complex cycle",
			steps: []api.StepDef{
				{ID: "A", DependsOn: []string{"B"}},
				{ID: "B", DependsOn: []string{"C"}},
				{ID: "C", DependsOn: []string{"A"}},
			},
			wantErr: true,
			errSub:  "cycle detected",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSteps(tt.steps)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateSteps() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && err != nil {
				if !contains(err.Error(), tt.errSub) {
					t.Errorf("validateSteps() error = %v, want substring %v", err, tt.errSub)
				}
			}
		})
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || (len(s) > 0 && (s[0:len(sub)] == sub || contains(s[1:], sub))))
}
