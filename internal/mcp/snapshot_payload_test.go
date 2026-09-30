package mcp

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/ben-ranford/lopper/internal/dashboard"
	"github.com/ben-ranford/lopper/internal/report"
)

func TestSnapshotSavePayloadJSONFields(t *testing.T) {
	for _, failed := range []bool{false, true} {
		var saveErr error
		if failed {
			saveErr = errors.New("snapshot exists")
		}
		for _, tc := range []struct {
			name       string
			payload    any
			summaryKey string
			schema     string
		}{
			{
				name: "analysis", summaryKey: "reportSummary", schema: report.SchemaVersion,
				payload: shapeBaselineSavePayload(AnalysisMutationRequest{RepoPath: "repo", BaselineStorePath: "store", BaselineKey: "key"}, report.Report{Summary: &report.Summary{}}, "snapshot", saveErr),
			},
			{
				name: "dashboard", summaryKey: "dashboardSummary", schema: dashboard.BaselineSnapshotSchemaVersion,
				payload: shapeDashboardBaselineSavePayload(DashboardMutationRequest{RepoPath: "repo", BaselineStorePath: "store", BaselineKey: "key"}, dashboard.Report{}, "snapshot", saveErr),
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				encoded, err := json.Marshal(tc.payload)
				if err != nil {
					t.Fatal(err)
				}
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(encoded, &fields); err != nil {
					t.Fatal(err)
				}
				want := map[string]any{"schemaVersion": tc.schema, "repoPath": "repo", "baselineStorePath": "store", "baselineKey": "key", "snapshotPath": "snapshot"}
				for key, value := range want {
					var got any
					if err := json.Unmarshal(fields[key], &got); err != nil || !reflect.DeepEqual(got, value) {
						t.Fatalf("%s = %s, want %#v: %v", key, fields[key], value, err)
					}
				}
				if fields[tc.summaryKey] == nil || fields["report"] == nil || fields["summary"] == nil {
					t.Fatalf("report fields missing: %s", encoded)
				}
				if _, present := fields["error"]; present != failed {
					t.Fatalf("error presence = %t, want %t: %s", present, failed, encoded)
				}
			})
		}
	}
}
