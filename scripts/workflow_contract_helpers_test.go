package scripts

import (
	"slices"
	"strings"
	"testing"
)

func TestWorkflowContiguousStepIndices(t *testing.T) {
	job := workflowJobConfig{Steps: []workflowStepConfig{{Name: "checkout"}, {Name: "reset"}, {Name: "build"}, {Name: "validate"}, {Name: "upload"}}}
	for _, tc := range []struct {
		name      string
		steps     []string
		indices   []int
		wantError string
	}{
		{name: "contiguous sequence", steps: []string{"reset", "build", "validate", "upload"}, indices: []int{1, 2, 3, 4}},
		{name: "missing validation", steps: []string{"build", "missing"}, wantError: `must define step "missing"`},
		{name: "gap before validation", steps: []string{"reset", "validate"}, wantError: `step "validate" must immediately follow "reset"`},
		{name: "reordered validation", steps: []string{"validate", "build"}, wantError: `step "build" must immediately follow "validate"`},
		{name: "repeated step", steps: []string{"build", "build"}, wantError: `step "build" must immediately follow "build"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := workflowContiguousStepIndices(job, tc.steps)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("error = %v, want %q", err, tc.wantError)
				}
				return
			}
			if err != nil || !slices.Equal(got, tc.indices) {
				t.Fatalf("indices = %v, error = %v; want %v", got, err, tc.indices)
			}
		})
	}
}
