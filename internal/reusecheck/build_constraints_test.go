package reusecheck

import (
	"go/build/constraint"
	"strings"
	"testing"
)

func TestSourceBuildConstraintFilenames(t *testing.T) {
	for _, tc := range []struct{ path, expression string }{
		{"worker_linux.go", "linux"},
		{"worker_arm64.go", "arm64"},
		{"worker_linux_amd64.go", "linux && amd64"},
		{"worker_linux_test.go", "linux"},
		{"worker_linux_arm64_test.go", "linux && arm64"},
		{"worker_amd64_linux.go", "linux"},
		{"worker_linux.generated.go", "linux"},
		{"nested/windows/worker_linux.go", "linux"},
		{"linux.go", ""},
		{"worker_unix.go", ""},
		{"worker_feature.go", ""},
		{"worker_LINUX.go", ""},
		{"nested/linux/worker.go", ""},
		{"worker_test.go", ""},
	} {
		t.Run(tc.path, func(t *testing.T) {
			actual := buildPredicateForTest(t, tc.path, "package fixture\n")
			checkEquivalentBuildPredicates(t, actual, taggedBuildPredicate(t, tc.expression))
		})
	}
}

func TestSourceBuildConstraintHeaders(t *testing.T) {
	for _, tc := range []struct{ name, source, expression string }{
		{"modern", "//go:build linux\n\npackage fixture\n", "linux"},
		{"modern without blank", "//go:build linux\npackage fixture\n", "linux"},
		{"modern wins", "//go:build linux\n// +build windows\n\npackage fixture\n", "linux"},
		{"modern after block", "/* license */\n//go:build linux\n\npackage fixture\n", "linux"},
		{"legacy", "// +build linux\n\npackage fixture\n", "linux"},
		{"legacy comma", "// +build linux,amd64\n\npackage fixture\n", "linux && amd64"},
		{"legacy lines", "// +build linux darwin\n// +build amd64\n\npackage fixture\n", "(linux || darwin) && amd64"},
		{"legacy without blank", "// +build linux\npackage fixture\n", ""},
		{"legacy after block", "/* license */\n// +build linux\n\npackage fixture\n", ""},
		{"inside block", "/*\n//go:build windows\n// +build windows\n*/\npackage fixture\n", ""},
		{"comment text", "// example: //go:build windows\n\npackage fixture\n", ""},
		{"after package", "package fixture\n//go:build windows\n", ""},
		{"inside raw string", "package fixture\nconst text = `\n//go:build windows\n`\n", ""},
		{"BOM", "\ufeff//go:build linux\n\npackage fixture\n", "linux"},
		{"same-line block", "/* license */ //go:build windows\npackage fixture\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actual := buildPredicateForTest(t, "worker.go", tc.source)
			checkEquivalentBuildPredicates(t, actual, taggedBuildPredicate(t, tc.expression))
		})
	}
}

func TestSourceBuildConstraintCombinesFilenameAndHeader(t *testing.T) {
	actual := buildPredicateForTest(t, "worker_linux.go", "//go:build amd64 && feature\n\npackage fixture\n")
	checkEquivalentBuildPredicates(t, actual, taggedBuildPredicate(t, "linux && amd64 && feature"))
	if actual.unconditional() {
		t.Fatal("filename and header restrictions became unconditional")
	}
}

func TestSourceBuildConstraintPlatformImplications(t *testing.T) {
	for _, tc := range []struct {
		target, other string
		want          bool
	}{
		{"android", "linux", true},
		{"linux", "android", false},
		{"ios", "darwin", true},
		{"darwin", "ios", false},
		{"illumos", "solaris", true},
		{"solaris", "illumos", false},
		{"linux", "unix", true},
		{"freebsd", "unix", true},
		{"windows", "unix", false},
		{"linux && amd64", "linux", true},
		{"linux", "amd64", false},
		{"amd64", "linux", false},
		{"windows && arm64", "arm64", true},
	} {
		t.Run(tc.target+" implies "+tc.other, func(t *testing.T) {
			actual := taggedBuildPredicate(t, tc.target).implies(taggedBuildPredicate(t, tc.other))
			if actual != tc.want {
				t.Fatalf("implication=%v want=%v", actual, tc.want)
			}
		})
	}
}

func TestSourceBuildConstraintSymbolicImplications(t *testing.T) {
	for _, tc := range []struct {
		target, other string
		want          bool
	}{
		{"feature", "feature", true},
		{"!feature", "!feature", true},
		{"feature && extra", "feature", true},
		{"feature && extra", "extra", true},
		{"feature || extra", "feature", false},
		{"(feature && left) || (feature && right)", "feature", true},
		{"feature", "extra", false},
		{"feature", "!extra", false},
		{"", "feature", false},
		{"", "!feature", false},
		{"cgo", "cgo", true},
		{"", "cgo", false},
		{"", "!cgo", false},
		{"go1.99", "go1.99", true},
		{"", "go1.99", false},
		{"", "!go1.99", false},
	} {
		t.Run(tc.target+" implies "+tc.other, func(t *testing.T) {
			actual := taggedBuildPredicate(t, tc.target).implies(taggedBuildPredicate(t, tc.other))
			if actual != tc.want {
				t.Fatalf("implication=%v want=%v", actual, tc.want)
			}
		})
	}
}

func TestSourceBuildConstraintExclusions(t *testing.T) {
	for _, tc := range []struct {
		first, second string
		want          bool
	}{
		{"linux", "windows", true},
		{"amd64", "arm64", true},
		{"android", "!linux", true},
		{"ios", "!darwin", true},
		{"illumos", "!solaris", true},
		{"windows", "unix", true},
		{"linux", "android", false},
		{"darwin", "ios", false},
		{"solaris", "illumos", false},
		{"feature", "!feature", true},
		{"feature && extra", "!extra", true},
		{"feature || extra", "!feature", false},
		{"feature", "extra", false},
		{"cgo", "!cgo", true},
		{"go1.99", "!go1.99", true},
		{"", "linux", false},
	} {
		t.Run(tc.first+" versus "+tc.second, func(t *testing.T) {
			first, second := taggedBuildPredicate(t, tc.first), taggedBuildPredicate(t, tc.second)
			if first.excludes(second) != tc.want || second.excludes(first) != tc.want {
				t.Fatalf("exclusion must be symmetric and equal %v", tc.want)
			}
		})
	}
}

func TestSourceBuildConstraintInvalidPredicatesProveNothing(t *testing.T) {
	for _, tc := range []struct{ name, path, source string }{
		{"malformed", "worker.go", "//go:build linux &&\n\npackage fixture\n"},
		{"duplicate modern", "worker.go", "//go:build linux\n//go:build windows\n\npackage fixture\n"},
		{"platform contradiction", "worker.go", "//go:build linux && windows\n\npackage fixture\n"},
		{"symbol contradiction", "worker.go", "//go:build feature && !feature\n\npackage fixture\n"},
		{"filename contradiction", "worker_linux.go", "//go:build windows\n\npackage fixture\n"},
		{"hidden file", ".worker.go", "package fixture\n"},
		{"underscore file", "_worker.go", "package fixture\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			invalid := buildPredicateForTest(t, tc.path, tc.source)
			other := taggedBuildPredicate(t, "linux")
			if invalid.unconditional() || invalid.implies(other) || invalid.excludes(other) {
				t.Fatal("invalid target supplied a build proof")
			}
			if other.implies(invalid) || other.excludes(invalid) {
				t.Fatal("invalid dependency supplied a build proof")
			}
		})
	}
}

func TestSourceBuildConstraintImplicitCgo(t *testing.T) {
	for _, imported := range []string{`"C"`, "`C`", `"\x43"`} {
		actual := buildPredicateForTest(t, "worker.go", "package fixture; import "+imported)
		checkEquivalentBuildPredicates(t, actual, taggedBuildPredicate(t, "cgo"))
	}
	other := buildPredicateForTest(t, "worker.go", `package fixture; import "example.com/C"`)
	if !other.unconditional() {
		t.Fatal("ordinary import acquired a cgo condition")
	}
}

func TestSourceBuildConstraintUnknownPlatformsRemainPossible(t *testing.T) {
	unrestricted := taggedBuildPredicate(t, "")
	for _, names := range [][]string{buildKnownOS, buildKnownArch} {
		known := taggedBuildPredicate(t, strings.Join(names, " || "))
		future := taggedBuildPredicate(t, "!("+strings.Join(names, " || ")+")")
		if unrestricted.implies(known) || unrestricted.excludes(future) || !future.implies(unrestricted) {
			t.Fatal("known platform list became a closed-world assumption")
		}
	}
	knownSystems := taggedBuildPredicate(t, strings.Join(buildKnownOS, " || "))
	if taggedBuildPredicate(t, "unix").implies(knownSystems) {
		t.Fatal("a future Unix platform disappeared from the proof domain")
	}
}

func TestSourceBuildConstraintProofsHaveNoBooleanCounterexamples(t *testing.T) {
	expressions := []string{
		"a", "!a", "a && b", "a || b", "a && !b",
		"(a && b) || (a && c)", "(a || b) && (!a || c)",
		"(a || b) && (!a || !b)", "(a && b) || (!a && c)",
		"(a || b) && (c || d) && (!a || !c) && (!b || !d)",
		"(a || b) && (!a || b) && (a || !b) && (!a || !b)",
	}
	predicates := make([]*sourceBuildPredicate, len(expressions))
	parsed := make([]constraint.Expr, len(expressions))
	for index, expression := range expressions {
		predicates[index] = taggedBuildPredicate(t, expression)
		var err error
		parsed[index], err = constraint.Parse("//go:build " + expression)
		if err != nil {
			t.Fatal(err)
		}
	}
	for first, target := range predicates {
		for second, provider := range predicates {
			checkBooleanBuildProof(t, parsed[first], parsed[second], target.implies(provider), target.excludes(provider))
		}
	}
}

func checkBooleanBuildProof(t *testing.T, target, provider constraint.Expr, implication, exclusion bool) {
	t.Helper()
	for assignment := 0; assignment < 16; assignment++ {
		evaluate := func(tag string) bool { return assignment&(1<<strings.Index("abcd", tag)) != 0 }
		left, right := target.Eval(evaluate), provider.Eval(evaluate)
		if (implication && left && !right) || (exclusion && left && right) {
			t.Fatalf("unsound proof between %s and %s for assignment %04b", target, provider, assignment)
		}
	}
}

func TestSourceBuildConstraintUnconditional(t *testing.T) {
	unrestricted := taggedBuildPredicate(t, "")
	if !unrestricted.unconditional() {
		t.Fatal("source without constraints must be unconditional")
	}
	for _, expression := range []string{"linux", "amd64", "feature", "!feature", "cgo", "go1.99"} {
		predicate := taggedBuildPredicate(t, expression)
		if predicate.unconditional() || !predicate.implies(unrestricted) {
			t.Fatalf("constraint %q lost its restriction or implication to unconstrained source", expression)
		}
	}
}

func taggedBuildPredicate(t *testing.T, expression string) *sourceBuildPredicate {
	t.Helper()
	source := "package fixture\n"
	if expression != "" {
		source = "//go:build " + expression + "\n\n" + source
	}
	return buildPredicateForTest(t, "worker.go", source)
}

func buildPredicateForTest(t *testing.T, path, source string) *sourceBuildPredicate {
	t.Helper()
	predicate := sourceBuildConstraint(path, []byte(source))
	if predicate == nil {
		t.Fatal("build constraint parser returned nil")
	}
	return predicate
}

func checkEquivalentBuildPredicates(t *testing.T, actual, expected *sourceBuildPredicate) {
	t.Helper()
	if !actual.implies(expected) || !expected.implies(actual) {
		t.Fatal("filename/header constraint differs from its expected expression")
	}
	if actual.unconditional() != expected.unconditional() {
		t.Fatal("equivalent constraints disagree about unrestricted applicability")
	}
}
