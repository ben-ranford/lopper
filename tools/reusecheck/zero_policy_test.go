package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestRunRejectsRetiredAllowanceRequests(t *testing.T) {
	root := t.TempDir()
	path := "internal/lang/fixture/copy.go"
	testutil.MustWriteFile(t, filepath.Join(root, filepath.FromSlash(path)), copiedKeys)
	document := fmt.Sprintf(`[{"path":%q,"function":"keys","rule":"sorted-set-keys","sha256":"%x","reason":"Previously accepted exact-source waiver","issue":"https://github.com/ben-ranford/lopper/issues/1615"}]`, path, sha256.Sum256([]byte(copiedKeys)))
	testutil.MustWriteFile(t, filepath.Join(root, "waiver.json"), document)
	testutil.MustWriteFile(t, filepath.Join(root, "empty.json"), "[]")
	for _, flags := range [][]string{
		{"-legacy-advisory=true"}, {"-legacy-advisory"},
		{"-exceptions", "waiver.json"}, {"-exceptions", "empty.json"},
		{"-exceptions", ""}, {"-exceptions="},
		{"-exceptions", filepath.Join(root, "waiver.json")},
	} {
		t.Run(strings.Join(flags, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			args := append([]string{"-root", root}, flags...)
			if code := run(args, &stdout, &stderr); code != 2 || stderr.Len() == 0 {
				t.Fatalf("retired allowance code=%d stdout=%s stderr=%s", code, &stdout, &stderr)
			}
		})
	}
}

func TestRunDefaultAndProtectedInvocationReportEveryCopy(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"first", "second"} {
		testutil.MustWriteFile(t, filepath.Join(root, "internal", "lang", name, "copy.go"), copiedKeys)
	}
	for _, flags := range [][]string{nil, {"-legacy-advisory=false"}} {
		var stdout, stderr bytes.Buffer
		args := append([]string{"-root", root}, flags...)
		if code := run(args, &stdout, &stderr); code != 1 || strings.Count(stdout.String(), ": violation sorted-set-keys ") != 2 || stderr.Len() != 0 {
			t.Fatalf("strict invocation code=%d stdout=%s stderr=%s", code, &stdout, &stderr)
		}
	}
}

func TestRunRetainsAdviceForUnprovenEquivalentMapping(t *testing.T) {
	root := t.TempDir()
	source := `package fixture
import r "github.com/ben-ranford/lopper/internal/report"
import s "github.com/ben-ranford/lopper/internal/lang/shared"
func mutate() string { return "changed" }
func build(measured s.DependencyStats) r.DependencyReport { ` + strings.Replace(packageReport, "Name:name", "Name:mutate()", 1) + " }"
	testutil.MustWriteFile(t, filepath.Join(root, "mapping.go"), source)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-root", root}, &stdout, &stderr); code != 0 || strings.Count(stdout.String(), ": advisory dependency-report-mapping ") != 1 || stderr.Len() != 0 {
		t.Fatalf("unproven equivalence code=%d stdout=%s stderr=%s", code, &stdout, &stderr)
	}
}
