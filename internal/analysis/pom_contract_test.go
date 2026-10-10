package analysis

import (
	"encoding/json"
	"fmt"
	"github.com/ben-ranford/lopper/internal/lang/shared"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestPomIdentityCharacterization(t *testing.T) {
	data, err := os.ReadFile("../lang/shared/testdata/pom-consumers.xml")
	if err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "pom.xml"), string(data))
	result := report.Report{Dependencies: []report.DependencyReport{}}
	for _, name := range []string{"alias", "pre-lib-lib", "managed", "explicit", "missing", "cycle", "bom", "only"} {
		result.Dependencies = append(result.Dependencies, report.DependencyReport{Language: "jvm", Name: name})
	}
	annotateDependencyIdentities(repo, &result)
	actual, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	path := "../lang/shared/testdata/pom-identity.json"

	expected, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(expected) != string(actual) {
		t.Fatalf("consumer output changed:\n%s\nwant:\n%s", actual, expected)
	}
	t.Logf("normalized-consumer-output: %s", actual)
}

func TestMavenEightStepCharacterization(t *testing.T) {
	for _, depth := range []int{7, 8, 9} {
		properties := map[string]string{}
		for step := 0; step < depth; step++ {
			properties[fmt.Sprint(step)] = fmt.Sprintf("${%d}", step+1)
		}
		properties[fmt.Sprint(depth-1)] = "done"
		want := "done"
		if depth == 9 {
			want = ""
		}
		if got := resolveMavenVersion("${0}", properties); got != want {
			t.Fatalf("depth %d: %q, want %q", depth, got, want)
		}
	}
	for _, value := range []string{"v${x}", "${x}-${y}", "${}"} {
		want := value
		if value == "${x}-${y}" {
			want = ""
		}
		if got := resolveMavenVersion(value, map[string]string{"x": "1", "y": "2"}); got != want {
			t.Fatalf("%q resolved to %q want %q", value, got, want)
		}
	}
}

func TestPomIdentityMalformedCharacterization(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, "pom.xml")
	testutil.MustWriteFile(t, path, "<project>")
	warnings := newIdentityWarningCollector(repo)
	index := make(identityIndex)
	collectPomIdentityEvidence(repo, path, index, warnings)
	if len(index) != 0 || !reflect.DeepEqual(warnings.list(), []string{"identity manifest parse failed for pom.xml: invalid XML"}) {
		t.Fatalf("unexpected diagnostics: %#v %#v", index, warnings.list())
	}
}

func TestPomIdentityBoundedRead(t *testing.T) {
	const content = `<project><dependencies><dependency><groupId>example</groupId><artifactId>late</artifactId><version>1</version></dependency></dependencies></project>`
	for _, size := range []int{shared.POMByteLimit, shared.POMByteLimit + 1} {
		repo := t.TempDir()
		path := filepath.Join(repo, "pom.xml")
		testutil.MustWriteFile(t, path, strings.Repeat(" ", size-len(content))+content)
		index := make(identityIndex)
		warnings := newIdentityWarningCollector(repo)
		collectPomIdentityEvidence(repo, path, index, warnings)
		if size == shared.POMByteLimit {
			if len(index) == 0 || len(warnings.list()) != 0 {
				t.Fatalf("late dependency lost at boundary: %#v %#v", index, warnings.list())
			}
			continue
		}
		if len(index) != 0 || !reflect.DeepEqual(warnings.list(), []string{"identity manifest read failed for pom.xml: file exceeds size limit"}) {
			t.Fatalf("unexpected bounded read result: %#v %#v", index, warnings.list())
		}
	}
}

func TestPomIdentityConfinedRead(t *testing.T) {
	repo, outside := t.TempDir(), t.TempDir()
	target := filepath.Join(outside, "pom.xml")
	testutil.MustWriteFile(t, target, `<project><dependencies><dependency><groupId>outside</groupId><artifactId>outside</artifactId></dependency></dependencies></project>`)
	link := filepath.Join(repo, "pom.xml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{target, link} {
		index := make(identityIndex)
		warnings := newIdentityWarningCollector(repo)
		collectPomIdentityEvidence(repo, path, index, warnings)
		if len(index) != 0 || len(warnings.list()) != 1 {
			t.Fatalf("unconfined evidence: %#v %#v", index, warnings.list())
		}
	}
}
