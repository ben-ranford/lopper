package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestRunRejectsDirectReceiverReportMapping(t *testing.T) {
	for _, receiver := range []string{"raw.(s.DependencyStats)", "values[i+1]", "values[-i]"} {
		t.Run(receiver, func(t *testing.T) { checkDirectReceiverMapping(t, receiver) })
	}
}

func checkDirectReceiverMapping(t *testing.T, receiver string) {
	t.Helper()
	root := t.TempDir()
	source := `package fixture
import r "github.com/ben-ranford/lopper/internal/report"
import s "github.com/ben-ranford/lopper/internal/lang/shared"
func build(name string, raw any, values []s.DependencyStats, i int) r.DependencyReport { ` + strings.ReplaceAll(packageReport, "measured.", receiver+".") + " }"
	testutil.MustWriteFile(t, filepath.Join(root, "copy.go"), source)
	var output bytes.Buffer
	code := run([]string{"-root", root}, &output, &output)
	if code != 1 || strings.Count(output.String(), "violation dependency-report-mapping in build") != 1 {
		t.Fatalf("direct receiver: code=%d output=%s", code, &output)
	}
}
