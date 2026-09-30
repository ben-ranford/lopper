package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestCollectionProvenanceAcrossCLISources(t *testing.T) {
	for _, tc := range []struct {
		name, declaration, parameter, setup string
	}{
		{"source function", "func stats() []Stats { panic(0) }", "", "values := stats(); measured := values[0];"},
		{"collection assertion", "", ", raw any", "values, ok := raw.([]Stats); _ = ok; measured := values[0];"},
		{"variadic parameter", "", ", values ...Stats", "measured := values[0];"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			provider := "package fixture\nimport s \"github.com/ben-ranford/lopper/internal/lang/shared\"\ntype Stats = s.DependencyStats\n" + tc.declaration
			consumer := "package fixture\nimport r \"github.com/ben-ranford/lopper/internal/report\"\nfunc build(name string" + tc.parameter + ") r.DependencyReport { " + tc.setup + packageReport + " }"
			testutil.MustWriteFile(t, filepath.Join(root, "provider.go"), provider)
			testutil.MustWriteFile(t, filepath.Join(root, "consumer.go"), consumer)
			var output bytes.Buffer
			code := run([]string{"-root", root}, &output, &output)
			if code != 1 || !strings.Contains(output.String(), "consumer.go:3: violation dependency-report-mapping in build") {
				t.Fatalf("collection provenance: code=%d output=%s", code, &output)
			}
		})
	}
}
