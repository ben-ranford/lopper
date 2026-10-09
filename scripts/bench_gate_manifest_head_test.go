package scripts

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBenchGateRejectsIncompleteManifestStreams(t *testing.T) {
	for name, payload := range corruptHarnessStreams() {
		t.Run(name, func(t *testing.T) {
			fixture := newBenchGateFixture(t, "benchpkg")
			fixture.writeBenchmarkPackage("benchpkg", benchmarkHarnessPackageFiles(nil))
			fixture.commit("base")
			fixture.writeFile("README.md", "head\n")
			fixture.commit("head")
			injectHarnessProducerOutput(t, fixture, payload)
			assertHarnessInfrastructureFailure(t, fixture)
		})
	}
}

func corruptHarnessStreams() map[string]string {
	row := "test-init\texample.test/setup\tsha256:" + fmt.Sprintf("%x", sha256.Sum256([]byte("harness-init-v1\ntest\nexample.test/setup\n"))) + "\n"
	valid := frameHarnessTestPayload("test", row, 1)
	return map[string]string{
		"empty":                "",
		"header only":          "harness-v1\ttest\n",
		"valid row prefix":     "harness-v1\ttest\n" + row,
		"duplicate header":     "harness-v1\ttest\n" + valid,
		"wrong count":          strings.Replace(valid, "harness-end\ttest\t1\t", "harness-end\ttest\t0\t", 1),
		"stale digest":         strings.Replace(valid, "example.test/setup", "example.test/other", 1),
		"unknown row":          frameHarnessTestPayload("test", "other\tidentity\tsha256:bad\n", 1),
		"empty canonical hash": frameHarnessTestPayload("test", "test-init\texample.test/setup\t\n", 1),
		"duplicate rows":       frameHarnessTestPayload("test", row+row, 2),
		"wrong kind":           frameHarnessTestPayload("xtest", "", 0),
		"unsupported version":  strings.Replace(valid, "harness-v1", "harness-v2", 1),
		"after completion":     valid + row,
		"duplicate completion": valid + strings.SplitAfter(valid, "\n")[2],
		"unterminated":         strings.TrimSuffix(valid, "\n"),
		"noncanonical count":   strings.Replace(valid, "harness-end\ttest\t1\t", "harness-end\ttest\t01\t", 1),
		"empty source hash":    frameHarnessTestPayload("test", "test\tbench_test.go\t\n", 1),
		"hashed embed":         frameHarnessTestPayload("test", "test-embed\tdata.txt\tsha256:bad\n", 1),
		"extra field":          frameHarnessTestPayload("test", strings.TrimSuffix(row, "\n")+"\textra\n", 1),
	}
}

func frameHarnessTestPayload(kind, payload string, count int) string {
	return fmt.Sprintf("harness-v1\t%s\n%sharness-end\t%s\t%d\tsha256:%x\n", kind, payload, kind, count, sha256.Sum256([]byte(payload)))
}

func injectHarnessProducerOutput(t *testing.T, fixture benchGateFixture, payload string) {
	t.Helper()
	fixture.writeFile("broken-manifest", payload)
	mutateHarnessProducer(t, fixture, func(string) string { return payloadProducerCommand(payload) })
}

func mutateHarnessProducer(t *testing.T, fixture benchGateFixture, replacement func(string) string) {
	t.Helper()
	path := filepath.Join(fixture.root, "scripts/bench-gate.sh")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	original := `"$benchmark_harness_selector_bin" "$fingerprint_dir" "$fingerprint_kind" "$fingerprint_metadata_tmp" "$fingerprint_import_path" < "$fingerprint_kind_files_tmp"`
	if strings.Count(string(content), original) != 1 {
		t.Fatal("actual producer boundary not unique")
	}
	changed := strings.Replace(string(content), original, replacement(original), 1)
	if err := os.WriteFile(path, []byte(changed), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertHarnessInfrastructureFailure(t *testing.T, fixture benchGateFixture) {
	t.Helper()
	output, code := fixture.runBenchGate()
	if code != 2 || !strings.Contains(output, "Memory benchmark gate invalid") || strings.Contains(output, "approval required") {
		t.Fatalf("expected invalid infrastructure, code=%d\n%s", code, output)
	}
	for _, name := range []string{"memory-bench-status.txt", "memory-bench-summary.md"} {
		data, err := os.ReadFile(filepath.Join(fixture.root, ".artifacts", name))
		if err != nil {
			t.Fatal(err)
		}
		if name == "memory-bench-status.txt" && strings.TrimSpace(string(data)) != "2" {
			t.Fatalf("status=%s", data)
		}
		if name == "memory-bench-summary.md" && !strings.Contains(string(data), "invalid") {
			t.Fatalf("summary=%s", data)
		}
	}
}

// An empty later invocation must fail even after the first complete stream succeeded.
func TestBenchGateRejectsMissingExternalManifest(t *testing.T) {
	fixture := newBenchGateFixture(t, "benchpkg")
	fixture.writeBenchmarkPackage("benchpkg", benchmarkHarnessPackageFiles(nil))
	fixture.commit("base")
	fixture.writeFile("README.md", "head\n")
	fixture.commit("head")
	injectHarnessProducerOutput(t, fixture, "omit-xtest")
	assertHarnessInfrastructureFailure(t, fixture)
}

func payloadProducerCommand(payload string) string {
	if payload == "omit-xtest" {
		return `if [ "$fingerprint_kind" = test ]; then "$benchmark_harness_selector_bin" "$fingerprint_dir" "$fingerprint_kind" "$fingerprint_metadata_tmp" "$fingerprint_import_path" < "$fingerprint_kind_files_tmp"; fi`
	}
	return `cat broken-manifest`
}

func TestBenchGateRejectsInvalidNormalPackageMetadata(t *testing.T) {
	for name, preparation := range map[string]string{
		"missing metadata":          `rm "$fingerprint_metadata_tmp";`,
		"generated main":            normalMetadataCommand(`{"Dir":"%s","ImportPath":"%s.test"}`),
		"augmented test":            normalMetadataCommand(`{"Dir":"%s","ImportPath":"%s","ForTest":"fixture"}`),
		"wrong directory":           normalMetadataCommand(`{"Dir":"%s/other","ImportPath":"%s"}`),
		"missing test inventory":    normalMetadataCommand(`{"Dir":"%s","ImportPath":"%s"}`),
		"missing production source": normalMetadataCommand(`{"Dir":"%s","ImportPath":"%s","TestGoFiles":["bench_test.go"],"GoFiles":["missing.go"]}`),
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newBenchGateFixture(t, "benchpkg")
			fixture.writeBenchmarkPackage("benchpkg", benchmarkHarnessPackageFiles(nil))
			fixture.commit("base")
			fixture.writeFile("README.md", "head\n")
			fixture.commit("head")
			mutateHarnessProducer(t, fixture, func(original string) string { return preparation + original })
			assertHarnessInfrastructureFailure(t, fixture)
		})
	}
}

func normalMetadataCommand(format string) string {
	return `printf '` + format + `' "$fingerprint_dir" "$fingerprint_import_path" > "$fingerprint_metadata_tmp"; `
}
