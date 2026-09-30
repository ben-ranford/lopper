package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestRunRejectsDirectAssertedReportMapping(t *testing.T) {
	root := t.TempDir()
	source := `package fixture
import r "github.com/ben-ranford/lopper/internal/report"
import s "github.com/ben-ranford/lopper/internal/lang/shared"
func build(name string, raw any) r.DependencyReport { ` + strings.ReplaceAll(packageReport, "measured.", "raw.(s.DependencyStats).") + " }"
	testutil.MustWriteFile(t, filepath.Join(root, "copy.go"), source)
	var output bytes.Buffer
	code := run([]string{"-root", root}, &output, &output)
	if code != 1 || strings.Count(output.String(), "violation dependency-report-mapping in build") != 1 {
		t.Fatalf("direct assertion: code=%d output=%s", code, &output)
	}
}
