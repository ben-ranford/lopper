package main

import (
	"bytes"
	"runtime"
	"strings"
	"testing"
)

func TestDarwinProofExecutesMatchingBaseAndHead(t *testing.T) {
	repo := darwinProofFixture(t, "if !Fixed() { t.Fatal(\"darwin behavior remains broken\") }", "func Fixed() bool { return false }")
	for _, metadata := range []string{"", " [darwin]"} {
		t.Run("metadata"+metadata, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			r := &runner{stderr: &stderr}
			env := regressionProofEnv(map[string]string{"PR_TITLE": "fix(ci): prove Darwin behavior", "PR_BASE_SHA": repo.baseSHA, "PR_BODY": "Regression-Test: ./buggy::TestDarwinBehavior" + metadata})
			if code := r.run([]string{"--repo", repo.path, "--target-os", "darwin"}, env, &stdout); code != 0 {
				t.Fatalf("proof code=%d stderr=%s", code, &stderr)
			}
			for _, expected := range []string{"Expected base failure: ./buggy::TestDarwinBehavior", "darwin behavior remains broken", "Regression proof verified: ./buggy::TestDarwinBehavior", "runner=darwin/" + runtime.GOARCH, "base=fail head=pass", "base_commit=" + repo.baseSHA} {
				if !strings.Contains(stdout.String(), expected) {
					t.Fatalf("missing %q in %s", expected, &stdout)
				}
			}
		})
	}
}

func TestDarwinProofRejectsNonBehavioralOutcomes(t *testing.T) {
	for _, tc := range []struct{ name, body, base, want string }{
		{"base skip", `t.Skip("unsupported fixture")`, "func Fixed() bool { return false }", "must fail instead of skip"},
		{"head skip", `if !Fixed() { t.Fatal("broken") }; t.Skip("unsupported fixture")`, "func Fixed() bool { return false }", "must pass instead of skip on head"},
		{"compiler failure", `if !Fixed() { t.Fatal("broken") }`, "func Other() bool { return false }", "must compile before proof"},
		{"base passes", `if !Fixed() { t.Fatal("broken") }`, "func Fixed() bool { return true }", "unexpectedly passed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := darwinProofFixture(t, tc.body, tc.base)
			var stdout, stderr bytes.Buffer
			r := &runner{stderr: &stderr}
			env := regressionProofEnv(map[string]string{"PR_TITLE": "fix(ci): prove Darwin behavior", "PR_BASE_SHA": repo.baseSHA, "PR_BODY": "Regression-Test: ./buggy::TestDarwinBehavior [darwin]"})
			code := r.run([]string{"--repo", repo.path, "--target-os", "darwin"}, env, &stdout)
			if code != 1 || !strings.Contains(stderr.String(), tc.want) || strings.Contains(stdout.String(), "Regression proof verified:") {
				t.Fatalf("code=%d stdout=%s stderr=%s", code, &stdout, &stderr)
			}
		})
	}
}

func darwinProofFixture(t *testing.T, body, base string) regressionProofRepo {
	t.Helper()
	return newRegressionProofRepo(t, regressionProofScenario{
		baseFiles: map[string]string{"go.mod": "module example.com/darwinproof\n\ngo 1.23\n", "buggy/buggy.go": "package buggy\n" + base + "\n"},
		headFiles: map[string]string{"buggy/buggy.go": "package buggy\nfunc Fixed() bool { return true }\n", "buggy/buggy_darwin_test.go": "package buggy\nimport \"testing\"\nfunc TestDarwinBehavior(t *testing.T) { " + body + " }\n"},
	})
}
