//go:build windows

package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"strings"
)

const pythonExecutableName = "python.exe"

func captureProvider(ctx context.Context, root, bin string) (receipt, error) {
	if err := checkProviderLocation(root, bin); err != nil {
		return receipt{}, err
	}
	private, err := inventory(ctx, root, false)
	if err != nil {
		return receipt{}, err
	}
	environment, err := probeEnvironment()
	if err != nil {
		return receipt{}, err
	}
	observed, err := probe(ctx, private, environment)
	if err != nil {
		return receipt{}, err
	}
	complete := private
	aliases, err := captureAliases(ctx, private, bin)
	if err != nil {
		return receipt{}, err
	}
	complete.Records = append(complete.Records, aliases...)
	for _, path := range []string{proofBash, proofCygpath} {
		row, err := moduleRecord(ctx, path)
		if err != nil {
			return receipt{}, err
		}
		row.Kind = "discovery"
		complete.Records = append(complete.Records, row)
	}
	complete.Records = append(complete.Records, observed.Modules...)
	observer, build, err := observerRecords(ctx)
	if err != nil {
		return receipt{}, err
	}
	complete.Records = append(complete.Records, observer, build)
	evidence, err := observationRecord(observed)
	if err != nil {
		return receipt{}, err
	}
	complete.Records = append(complete.Records, evidence)
	return complete, nil
}

func captureAliases(ctx context.Context, private receipt, bin string) ([]record, error) {
	var aliases []record
	for _, name := range []string{pythonExecutableName, "python3.exe"} {
		path := filepath.Join(bin, name)
		if err := requireAMD64File(path); err != nil {
			return nil, err
		}
		row, err := moduleRecord(ctx, path)
		if err != nil {
			return nil, err
		}
		if row.Hash == interpreterDigest(private) {
			return nil, errors.New("raw interpreter cannot serve as transport alias")
		}
		row.Kind = "adapter"
		aliases = append(aliases, row)
	}
	return aliases, nil
}

func checkProviderLocation(root, bin string) error {
	if err := canonicalDirectory(bin); err != nil {
		return err
	}
	if filepath.Base(root) != "lopper-proof-python" || filepath.Dir(root) != bin {
		return errors.New("private Python root must be fixed child of captured Go bin")
	}
	goRoot := filepath.Dir(bin)
	lower := strings.ToLower(goRoot)
	if !strings.HasPrefix(lower, `c:\hostedtoolcache\windows\go\`) && !strings.HasPrefix(lower, `d:\hostedtoolcache\windows\go\`) {
		return errors.New("provider Go root is outside captured hosted cache policy")
	}
	if filepath.Base(bin) != "bin" {
		return errors.New("provider requires captured Go bin")
	}
	return nil
}

func probeEnvironment() ([]string, error) {
	root, err := windows.GetWindowsDirectory()
	if err != nil {
		return nil, err
	}
	if err := canonicalDirectory(root); err != nil {
		return nil, err
	}
	return []string{"SystemRoot=" + root, "WINDIR=" + root}, nil
}

func loadExpectedReceipt(path, digest string) (value receipt, returnErr error) {
	if err := canonicalRegular(path); err != nil {
		return receipt{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return receipt{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	return readAuthenticatedReceipt(file, digest)
}

func verifyProvider(ctx context.Context, expected receipt) error {
	bin := filepath.Dir(expected.Root)
	if err := checkProviderLocation(expected.Root, bin); err != nil {
		return err
	}
	private, external, err := splitProviderReceipt(expected)
	if err != nil {
		return err
	}
	if err := verifyInventory(ctx, private); err != nil {
		return err
	}
	for _, row := range external {

		info, err := os.Lstat(row.Path)
		if err != nil {
			return err
		}
		if info.Size() < 0 || uint64(info.Size()) != row.Bytes || uint32(info.Mode().Perm()) != row.Mode {
			return errors.New("external provider file identity changed")
		}
		if err := authenticateFileContext(ctx, row.Path, row.Hash); err != nil {
			return err
		}
	}
	actual, err := captureProvider(ctx, expected.Root, bin)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(withoutObservation(actual), withoutObservation(expected)) {
		return errors.New("complete provider inventory changed")
	}
	return nil
}

func splitProviderReceipt(expected receipt) (receipt, []record, error) {
	private := receipt{Version: expected.Version, Root: expected.Root}
	var external []record
	aliases := map[string]bool{}
	var osCount, observations, observers, builds, discovery int
	var osBytes uint64
	for _, row := range expected.Records {
		if err := validateEvidenceRecord(row); err != nil {
			return receipt{}, nil, err
		}
		switch row.Kind {
		case "discovery":
			discovery++
			external = append(external, row)
		case "observer":
			observers++
			external = append(external, row)
		case "buildinfo":
			builds++
		case "observation":
			observations++
		case "directory", "file":
			private.Records = append(private.Records, row)
		case "adapter":
			if err := admitAdapterRecord(row, expected.Root, aliases); err != nil {
				return receipt{}, nil, err
			}
			external = append(external, row)
		case "os":
			if err := admitOSReceipt(row, &osCount, &osBytes); err != nil {
				return receipt{}, nil, err
			}
			external = append(external, row)
		default:
			return receipt{}, nil, fmt.Errorf("unrecognised provider record %q", row.Kind)
		}
	}
	if len(aliases) != 2 || len(private.Records) == 0 || observations != 1 || observers != 1 || builds != 1 || discovery != 2 {
		return receipt{}, nil, errors.New("incomplete provider receipt")
	}
	return private, external, nil
}

func validateEvidenceRecord(row record) error {
	if row.Kind == "discovery" && row.Path != proofBash && row.Path != proofCygpath {
		return errors.New("unknown native discovery tool")
	}
	if row.Kind == "observation" && row.Hash != fmt.Sprintf("%x", sha256.Sum256([]byte(probeProgram))) {
		return errors.New("probe program digest mismatch")
	}
	return nil
}

func admitAdapterRecord(row record, root string, aliases map[string]bool) error {
	if filepath.Dir(row.Path) != filepath.Dir(root) {
		return errors.New("adapter escaped fixed Go bin")
	}
	name := filepath.Base(row.Path)
	if (name != pythonExecutableName && name != "python3.exe") || aliases[name] {
		return errors.New("invalid or duplicate adapter")
	}
	aliases[name] = true
	return nil
}

func observationRecord(observed probeResult) (record, error) {
	data, err := json.Marshal(struct {
		Ready    json.RawMessage `json:"ready"`
		Creation string          `json:"creation"`
	}{Ready: json.RawMessage(observed.Ready), Creation: observed.Creation})
	if err != nil {
		return record{}, err
	}
	if len(data) > maxStringBytes {
		return record{}, errors.New("probe evidence token limit")
	}
	return record{Kind: "observation", Path: "@held-probe", Hash: fmt.Sprintf("%x", sha256.Sum256([]byte(probeProgram))), Bytes: uint64(observed.PID), Target: string(data)}, nil
}

func withoutObservation(value receipt) receipt {
	filtered := receipt{Version: value.Version, Root: value.Root}
	for _, row := range value.Records {
		if row.Kind != "observation" {
			filtered.Records = append(filtered.Records, row)
		}
	}
	return filtered
}

func admitOSReceipt(row record, count *int, total *uint64) error {
	if *count >= maxOSModules || row.Bytes > maxOSFileBytes {
		return errors.New("system receipt limit exceeded")
	}
	if err := reserve(total, row.Bytes, maxOSBytes); err != nil {
		return err
	}
	*count++
	return nil
}

func observerRecords(ctx context.Context) (record, record, error) {
	path, err := os.Executable()
	if err != nil {
		return record{}, record{}, err
	}
	binary, err := moduleRecord(ctx, path)
	if err != nil {
		return record{}, record{}, err
	}
	binary.Kind = "observer"
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return record{}, record{}, errors.New("observer build information missing")
	}
	text := info.String()
	if len(text) > maxStringBytes {
		return record{}, record{}, errors.New("observer build metadata limit exceeded")
	}
	build := record{Kind: "buildinfo", Path: "@observer-build", Hash: fmt.Sprintf("%x", sha256.Sum256([]byte(text))), Bytes: uint64(len(text)), Target: text}
	return binary, build, nil
}

func interpreterDigest(private receipt) string {
	for _, row := range private.Records {
		if row.Path == pythonExecutableName {
			return row.Hash
		}
	}
	return ""
}
