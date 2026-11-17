package main

import (
	"errors"
	"io"

	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestReceiptAuthenticatesBeforeParsing(t *testing.T) {
	invalid := []byte("not json\n")
	_, err := readAuthenticatedReceipt(bytes.NewReader(invalid), strings.Repeat("0", 64))
	if err == nil || !strings.Contains(err.Error(), "independent expectation") {
		t.Fatalf("unauthenticated parser input accepted: %v", err)
	}
	_, err = readAuthenticatedReceipt(bytes.NewReader(invalid), fmt.Sprintf("%x", sha256.Sum256(invalid)))
	if err == nil || strings.Contains(err.Error(), "independent expectation") {
		t.Fatalf("authenticated invalid input did not reach parser: %v", err)
	}
}

func TestReceiptRejectsAmbiguousAndNestedRecords(t *testing.T) {
	for name, fixture := range map[string]struct{ input, category string }{
		"duplicate": {`{"kind":"file","kind":"directory","path":"x","hash":"","bytes":0,"mode":0,"target":""}`, "duplicate"},
		"nested":    {`{"kind":"file","path":{"x":[[[[]]]]},"hash":"","bytes":0,"mode":0,"target":""}`, "nested"},
		"missing":   {`{"kind":"file"}`, "requires string"},
		"overflow":  {`{"kind":"file","path":"x","hash":"","bytes":18446744073709551616,"mode":0,"target":""}`, "value out of range"},
		"trailing":  {`{"kind":"file","path":"x","hash":"","bytes":0,"mode":0,"target":""} {}`, "trailing"},
		"null":      {`{"kind":"file","path":null,"hash":"","bytes":0,"mode":0,"target":""}`, "null"},
		"unknown":   {`{"kind":"file","path":"x","hash":"","bytes":0,"mode":0,"target":"","other":true}`, "unknown"},
	} {
		t.Run(name, func(t *testing.T) {
			var metadata uint64
			_, err := decodeRecord([]byte(fixture.input), &metadata)
			if err == nil || !strings.Contains(err.Error(), fixture.category) {
				t.Fatalf("wanted %s rejection, got %v", fixture.category, err)
			}
		})
	}
}

func TestReceiptRejectsMalformedSyntaxSeparately(t *testing.T) {
	for _, input := range []string{`{"kind":`, `{"kind":"file",`, `{"kind" "file"}`, `[`, ``} {
		var metadata uint64
		if _, err := decodeRecord([]byte(input), &metadata); err == nil {
			t.Fatalf("malformed JSON accepted: %q", input)
		}
	}
}

func TestReceiptWholeFramingAndIndependentDigest(t *testing.T) {
	original := receipt{Root: providerTestRoot(t), Records: []record{{Kind: "directory", Path: ".", Mode: 0700, Target: "0000000000000001:0000000000000002"}}}
	data, err := encodeReceipt(original)
	if err != nil {
		t.Fatal(err)
	}
	expected := fmt.Sprintf("%x", sha256.Sum256(data))
	got, err := readAuthenticatedReceipt(bytes.NewReader(data), expected)
	if err != nil {
		t.Fatal(err)
	}
	if got.Root != original.Root || len(got.Records) != 1 || got.Records[0] != original.Records[0] {
		t.Fatalf("receipt changed: %+v", got)
	}
	for _, changed := range [][]byte{data[:len(data)-1], append(append([]byte{}, data...), data...)} {
		if _, err := readAuthenticatedReceipt(bytes.NewReader(changed), expected); err == nil {
			t.Fatal("changed receipt accepted under original digest")
		}
	}
}

func TestJointRuntimeAndReceiptSubstitutionCannotReplaceExpectation(t *testing.T) {
	root := providerTestRoot(t)
	path := filepath.Join(root, "python.exe")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	captured, err := inventory(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	original, err := encodeReceipt(captured)
	if err != nil {
		t.Fatal(err)
	}
	expected := fmt.Sprintf("%x", sha256.Sum256(original))
	if err := os.WriteFile(path, []byte("substitute"), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := inventory(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := encodeReceipt(changed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readAuthenticatedReceipt(bytes.NewReader(replacement), expected); err == nil {
		t.Fatal("joint runtime+receipt substitution replaced independent expectation")
	}
	old, err := readAuthenticatedReceipt(bytes.NewReader(original), expected)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyInventory(context.Background(), old); err == nil {
		t.Fatal("unchanged receipt concealed substituted runtime")
	}
}

func TestBoundedReadConsumesOnlySingleOverflowProbe(t *testing.T) {
	input := bytes.NewReader([]byte("abcdEFG"))
	if _, err := boundedBytes(input, 3); err == nil {
		t.Fatal("overflow accepted")
	}
	if input.Len() != 3 {
		t.Fatalf("read beyond one overflow probe: remaining %d", input.Len())
	}
	exact, err := boundedBytes(bytes.NewReader([]byte("abc")), 3)
	if err != nil || string(exact) != "abc" {
		t.Fatalf("exact boundary failed: %q %v", exact, err)
	}
}

func TestReceiptTokenLimitCoversNumbersAndExactStrings(t *testing.T) {
	if err := checkJSONStrings([]byte(`"` + strings.Repeat("x", maxStringBytes) + `"`)); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{`"` + strings.Repeat("x", maxStringBytes+1) + `"`, strings.Repeat("1", maxStringBytes+1)} {
		if err := checkJSONStrings([]byte(token)); err == nil {
			t.Fatal("oversized JSON token accepted before decode")
		}
	}
}

func TestObserverMetadataMustMatchRecordedBytes(t *testing.T) {
	text := "go1.27.2\npath\tproofpython\n"
	row := record{Kind: "buildinfo", Path: "@observer-build", Hash: fmt.Sprintf("%x", sha256.Sum256([]byte(text))), Bytes: uint64(len(text)), Target: text}
	if err := validateStoredRecord(row); err != nil {
		t.Fatal(err)
	}
	row.Target += "changed"
	if err := validateStoredRecord(row); err == nil {
		t.Fatal("observer build metadata changed without its identity")
	}
}

func TestReceiptRejectsInvalidStoredMetadata(t *testing.T) {
	identity := "0000000000000001:0000000000000002"
	digest := strings.Repeat("a", 64)
	for name, row := range map[string]record{
		"mode":                  {Kind: "file", Path: "x", Hash: digest, Target: identity, Mode: 01000},
		"size":                  {Kind: "file", Path: "x", Hash: digest, Target: identity, Bytes: maxFileBytes + 1},
		"directory hash":        {Kind: "directory", Path: ".", Hash: digest, Target: identity},
		"file hash":             {Kind: "file", Path: "x", Hash: "invalid", Target: identity},
		"external relative":     {Kind: "adapter", Path: "python.exe", Hash: digest, Target: identity},
		"unknown":               {Kind: "foreign", Path: "x", Hash: digest, Target: identity},
		"observation pid":       {Kind: "observation", Path: "@held-probe", Hash: digest, Target: `{}`, Bytes: 0},
		"observation malformed": {Kind: "observation", Path: "@held-probe", Hash: digest, Target: `{`, Bytes: 1},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateStoredRecord(row); err == nil {
				t.Fatal("invalid metadata accepted")
			}
		})
	}
	for _, row := range []record{
		{Kind: "directory", Path: ".", Target: identity},
		{Kind: "file", Path: "x", Hash: digest, Target: identity},
		{Kind: "adapter", Path: filepath.Join(providerTestRoot(t), "python.exe"), Hash: digest, Target: identity},
		{Kind: "observation", Path: "@held-probe", Hash: digest, Target: `{"ready":{},"creation":"1"}`, Bytes: 1},
	} {
		if err := validateStoredRecord(row); err != nil {
			t.Fatalf("valid stored record rejected: %v", err)
		}
	}
}

func TestReceiptFramingAndHeaderPolicies(t *testing.T) {
	for name, input := range map[string][]byte{
		"empty": {}, "unterminated": []byte(`{"kind":"header"}`), "oversized line": []byte(strings.Repeat("x", maxStringBytes*8+1) + "\n"),
		"invalid header": []byte(`{"kind":"header","path":"/private","hash":"wrong","bytes":0,"mode":0,"target":""}` + "\n"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseReceipt(input); err == nil {
				t.Fatal("invalid receipt framing accepted")
			}
		})
	}
	for _, root := range []string{"relative", "/private\x00"} {
		if _, err := encodeReceipt(receipt{Root: root}); err == nil {
			t.Fatal("invalid root encoded")
		}
	}
	value := receipt{Root: providerTestRoot(t), Records: []record{{Kind: "directory", Path: ".", Target: "0000000000000001:0000000000000002"}}}
	value.Records = append(value.Records, value.Records[0])
	if _, err := encodeReceipt(value); err == nil {
		t.Fatal("duplicate inventory encoded")
	}
	if _, err := readAuthenticatedReceipt(bytes.NewReader(nil), "invalid"); err == nil {
		t.Fatal("invalid independent digest accepted")
	}
	if err := authenticateFile("missing", "invalid"); err == nil {
		t.Fatal("invalid independent file digest accepted")
	}
	if err := authenticateFile(filepath.Join(providerTestRoot(t), "missing"), strings.Repeat("0", 64)); err == nil {
		t.Fatal("missing file authenticated")
	}
}

type providerErrorReader struct{ err error }

func (r *providerErrorReader) Read([]byte) (int, error) { return 0, r.err }

func TestReceiptReaderFailureAndActualByteCeiling(t *testing.T) {
	failure := errors.New("owned receipt read failure")
	if _, err := readAuthenticatedReceipt(&providerErrorReader{failure}, strings.Repeat("0", 64)); !errors.Is(err, failure) {
		t.Fatalf("read failure lost: %v", err)
	}
	for _, extra := range []int{0, 1} {
		reader := strings.NewReader(strings.Repeat("x", maxReceiptBytes+extra+1))
		data, err := boundedBytes(io.LimitReader(reader, int64(maxReceiptBytes+extra)), maxReceiptBytes)
		if extra == 0 {
			if err != nil || len(data) != maxReceiptBytes {
				t.Fatalf("exact byte limit rejected: %v", err)
			}
		} else if err == nil {
			t.Fatal("one-over byte limit admitted")
		}
		if reader.Len() != 1 {
			t.Fatalf("bounded reader consumed beyond supplied limit: %d", reader.Len())
		}
	}
}

func TestReceiptNumericAndEscapedStringBoundaries(t *testing.T) {
	base := `{"kind":"file","path":"x","hash":"","bytes":0,"mode":0,"target":""}`
	for name, fixture := range map[string]struct{ old, replacement, category string }{
		"boolean bytes": {`"bytes":0`, `"bytes":true`, "unsigned integer"},
		"string bytes":  {`"bytes":0`, `"bytes":"0"`, "unsigned integer"},
		"missing mode":  {`,"mode":0`, "", "unsigned integer"},
		"mode overflow": {`"mode":0`, `"mode":4294967296`, "value out of range"},
	} {
		t.Run(name, func(t *testing.T) {
			var metadata uint64
			_, err := decodeRecord([]byte(strings.Replace(base, fixture.old, fixture.replacement, 1)), &metadata)
			if err == nil || !strings.Contains(err.Error(), fixture.category) {
				t.Fatalf("wrong numeric rejection: %v", err)
			}
		})
	}
	escaped := strings.Replace(base, `"path":"x"`, `"path":"quote\"slash\\"`, 1)
	var metadata uint64
	row, err := decodeRecord([]byte(escaped), &metadata)
	if err != nil || row.Path != "quote\"slash\\" {
		t.Fatalf("escaped string changed: %q %v", row.Path, err)
	}
	if err := checkJSONStrings([]byte(`"unterminated`)); err == nil || !strings.Contains(err.Error(), "unterminated") {
		t.Fatalf("unterminated string accepted: %v", err)
	}
	if validIdentity("0000000000000001:INVALID000000002") {
		t.Fatal("invalid stored identity accepted")
	}
}

func TestReceiptUniqueRecordBoundaryCountsHeader(t *testing.T) {
	var input strings.Builder
	fmt.Fprintf(&input, "{\"kind\":\"header\",\"path\":%q,\"hash\":%q,\"bytes\":0,\"mode\":0,\"target\":\"\"}\n", providerTestRoot(t), policyVersion)
	for i := 0; i < maxEntries-1; i++ {
		fmt.Fprintf(&input, "{\"kind\":\"directory\",\"path\":\"d%d\",\"hash\":\"\",\"bytes\":0,\"mode\":0,\"target\":\"0000000000000001:0000000000000002\"}\n", i)
	}
	got, err := parseReceipt([]byte(input.String()))
	if err != nil || len(got.Records) != maxEntries-1 {
		t.Fatalf("exact record limit rejected: %d %v", len(got.Records), err)
	}
	input.WriteString("{\"kind\":\"directory\",\"path\":\"extra\",\"hash\":\"\",\"bytes\":0,\"mode\":0,\"target\":\"0000000000000001:0000000000000002\"}\n")
	if _, err := parseReceipt([]byte(input.String())); err == nil || !strings.Contains(err.Error(), "record limit") {
		t.Fatalf("header not counted in record limit: %v", err)
	}
}

func TestReceiptEncodingRejectsEscapedExpansionBeyondByteLimit(t *testing.T) {
	value := receipt{Root: providerTestRoot(t)}
	component := strings.Repeat(`"`, 240)
	for i := 0; i < 35000; i++ {
		row := record{Kind: "directory", Path: component + "/" + component + fmt.Sprint(i), Target: "0000000000000001:0000000000000002"}
		if err := validateStoredRecord(row); err != nil {
			t.Fatalf("expansion fixture violates record policy: %v", err)
		}
		value.Records = append(value.Records, row)
	}
	data, err := encodeReceipt(value)
	if err == nil || !strings.Contains(err.Error(), "size exceeds policy") || len(data) != 0 {
		t.Fatalf("encoded byte overflow returned partial success: bytes=%d err=%v", len(data), err)
	}
}

func decodedExpansionReceipt(t *testing.T, files int) []byte {
	t.Helper()
	var input bytes.Buffer
	root := providerTestRoot(t)
	fmt.Fprintf(&input, "{\"kind\":\"header\",\"path\":%q,\"hash\":%q,\"bytes\":0,\"mode\":0,\"target\":\"\"}\n", root, policyVersion)
	identity := "0000000000000001:0000000000000002"
	fmt.Fprintf(&input, "{\"kind\":\"directory\",\"path\":\".\",\"hash\":\"\",\"bytes\":0,\"mode\":0,\"target\":%q}\n", identity)
	component := strings.Repeat(string([]byte{0xff}), 240)
	prefix := ""
	for depth := 1; depth <= 7; depth++ {
		if prefix != "" {
			prefix += "/"
		}
		prefix += component
		fmt.Fprintf(&input, "{\"kind\":\"directory\",\"path\":\"%s\",\"hash\":\"\",\"bytes\":0,\"mode\":0,\"target\":%q}\n", prefix, identity)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(nil))
	for i := 0; i < files; i++ {
		path := prefix + "/" + component + fmt.Sprintf("%06d", i)
		fmt.Fprintf(&input, "{\"kind\":\"file\",\"path\":\"%s\",\"hash\":%q,\"bytes\":0,\"mode\":0,\"target\":%q}\n", path, digest, identity)
	}
	if input.Len() >= maxReceiptBytes || files+9 >= maxEntries || files >= maxFiles {
		t.Fatal("expansion corpus exceeds earlier limits")
	}
	return input.Bytes()
}

func TestReceiptDecodedExpansionEnforcesFreshMetadataBudget(t *testing.T) {
	accepted := decodedExpansionReceipt(t, 5000)
	got, err := readAuthenticatedReceipt(bytes.NewReader(accepted), fmt.Sprintf("%x", sha256.Sum256(accepted)))
	if err != nil || len(got.Records) != 5008 {
		t.Fatalf("bounded expanded layout rejected: records=%d err=%v", len(got.Records), err)
	}
	for _, row := range got.Records {
		if err := validateStoredRecord(row); err != nil {
			t.Fatalf("decoded corpus violates stored path policy: %v", err)
		}
	}
	rejected := decodedExpansionReceipt(t, 6000)
	got, err = readAuthenticatedReceipt(bytes.NewReader(rejected), fmt.Sprintf("%x", sha256.Sum256(rejected)))
	if err == nil || !strings.Contains(err.Error(), "resource limit") || len(got.Records) != 0 {
		t.Fatalf("decoded metadata overflow admitted: records=%d err=%v", len(got.Records), err)
	}
}

func TestReceiptClosingTokensByteDefenseAndStoredIdentity(t *testing.T) {
	var metadata uint64
	malformed := `{"kind":"file","path":"x","hash":"","bytes":0,"mode":0,"target":""]`
	if _, err := decodeRecord([]byte(malformed), &metadata); err == nil || !strings.Contains(err.Error(), "incomplete receipt object") {
		t.Fatalf("closing-token error not retained: %v", err)
	}
	if err := validateProbeJSON([]byte(`[}`)); err == nil {
		t.Fatal("mismatched probe closer accepted")
	}
	if _, err := parseReceipt(bytes.Repeat([]byte{' '}, maxReceiptBytes+1)); err == nil || !strings.Contains(err.Error(), "receipt limit") {
		t.Fatalf("parser byte defense lost: %v", err)
	}
	if _, err := decodeRecord([]byte(`{"kind":"`+strings.Repeat("x", maxStringBytes+1)+`"}`), &metadata); err == nil || !strings.Contains(err.Error(), "token limit") {
		t.Fatalf("oversized token admitted: %v", err)
	}
	root := providerTestRoot(t)
	data := []byte(fmt.Sprintf("{\"kind\":\"header\",\"path\":%q,\"hash\":%q,\"bytes\":0,\"mode\":0,\"target\":\"\"}\n{\"kind\":\"directory\",\"path\":\".\",\"hash\":\"\",\"bytes\":0,\"mode\":0,\"target\":\"short\"}\n", root, policyVersion))
	if _, err := readAuthenticatedReceipt(bytes.NewReader(data), fmt.Sprintf("%x", sha256.Sum256(data))); err == nil || !strings.Contains(err.Error(), "record metadata") {
		t.Fatalf("authenticated invalid identity accepted: %v", err)
	}
}

// These sequential helper entry states do not arise from validateProbeJSON.
func TestProbeValueRejectsAlreadyOpenedContainerCloser(t *testing.T) {
	for _, tc := range []struct {
		input            string
		opening, closing json.Delim
	}{
		{input: "[]", opening: '[', closing: ']'},
		{input: "{}", opening: '{', closing: '}'},
	} {
		t.Run(tc.input, func(t *testing.T) {
			decoder := openedEmptyProbeDecoder(t, tc.input, tc.opening, tc.closing)
			if err := consumeProbeValue(decoder, 0); err == nil || err.Error() != "unexpected probe delimiter" {
				t.Fatalf("already-opened value rejection = %v, want unexpected probe delimiter", err)
			}
			assertProbeDecoderEOF(t, decoder)
			positive := json.NewDecoder(strings.NewReader(tc.input))
			if err := consumeProbeValue(positive, 0); err != nil {
				t.Fatalf("fresh container value rejected: %v", err)
			}
			assertProbeDecoderEOF(t, positive)
		})
	}
}

func TestProbeMembersRejectsWrongExpectedContainerKind(t *testing.T) {
	for _, tc := range []struct {
		input                   string
		opening, closing, wrong json.Delim
	}{
		{input: "[]", opening: '[', closing: ']', wrong: '{'},
		{input: "{}", opening: '{', closing: '}', wrong: '['},
	} {
		t.Run(tc.input, func(t *testing.T) {
			decoder := openedEmptyProbeDecoder(t, tc.input, tc.opening, tc.closing)
			if err := consumeProbeMembers(decoder, tc.wrong, 0); err == nil || err.Error() != "incomplete probe JSON" {
				t.Fatalf("wrong member-kind rejection = %v, want incomplete probe JSON", err)
			}
			assertProbeDecoderEOF(t, decoder)
			positive := openedEmptyProbeDecoder(t, tc.input, tc.opening, tc.closing)
			if err := consumeProbeMembers(positive, tc.opening, 0); err != nil {
				t.Fatalf("matching member kind rejected: %v", err)
			}
			assertProbeDecoderEOF(t, positive)
		})
	}
}

func openedEmptyProbeDecoder(t *testing.T, input string, opening, closing json.Delim) *json.Decoder {
	t.Helper()
	// Independently witness the actual matched Token pair before exercising a
	// fresh decoder. The helper's later EOF proves it consumed that closer.
	witness := json.NewDecoder(strings.NewReader(input))
	for _, want := range []json.Delim{opening, closing} {
		token, err := witness.Token()
		if err != nil || token != want {
			t.Fatalf("independent container token = %v, %v; want %q", token, err, want)
		}
	}
	assertProbeDecoderEOF(t, witness)
	decoder := json.NewDecoder(strings.NewReader(input))
	token, err := decoder.Token()
	if err != nil || token != opening {
		t.Fatalf("helper opening token = %v, %v; want %q", token, err, opening)
	}
	if decoder.More() {
		t.Fatal("empty container unexpectedly has members")
	}
	return decoder
}

func assertProbeDecoderEOF(t *testing.T, decoder *json.Decoder) {
	t.Helper()
	token, err := decoder.Token()
	if token != nil || !errors.Is(err, io.EOF) {
		t.Fatalf("after container token = %v, %v; want nil, EOF", token, err)
	}
}

func TestReceiptModeConversionRetainsBoundsAndPermissionPolicy(t *testing.T) {
	for _, value := range []string{"0", "511", "4294967295"} {
		values := map[string]any{"kind": "directory", "path": ".", "hash": "", "target": "0000000000000000:0000000000000000", "bytes": json.Number("0"), "mode": json.Number(value)}
		row, err := typedRecord(values)
		if err != nil || fmt.Sprint(row.Mode) != value {
			t.Fatalf("mode truncated: %s %+v %v", value, row, err)
		}
		if err := validateStoredRecord(row); (err != nil) != (value == "4294967295") {
			t.Fatalf("stored permission policy changed: %s %v", value, err)
		}
	}
	for _, value := range []string{"-1", "4294967296", "18446744073709551615"} {
		values := map[string]any{"kind": "directory", "path": ".", "hash": "", "target": "", "bytes": json.Number("0"), "mode": json.Number(value)}
		if _, err := typedRecord(values); err == nil {
			t.Fatalf("out-of-range mode accepted: %s", value)
		}
	}
}

func TestReceiptModePreservesOriginalParserErrors(t *testing.T) {
	for _, value := range []string{
		"0", "511", "4294967295", "4294967296", "18446744073709551615",
		"18446744073709551616", "-1", "1.5", "42949672960.5", "184467440737095516160.5",
	} {
		t.Run(value, func(t *testing.T) {
			assertReceiptModeParserResult(t, value)
		})
	}
	for _, value := range []any{nil, "511", 511, true} {
		values := map[string]any{"kind": "directory", "path": ".", "hash": "", "target": "", "bytes": json.Number("0"), "mode": value}
		row, err := typedRecord(values)
		if err == nil || err.Error() != "receipt requires unsigned integer" || row != (record{}) {
			t.Fatalf("nonnumeric error changed for %T: %+v %v", value, row, err)
		}
	}
}

func assertReceiptModeParserResult(t *testing.T, value string) {
	t.Helper()
	want, wantErr := strconv.ParseUint(value, 10, 32)
	values := map[string]any{"kind": "directory", "path": ".", "hash": "", "target": "", "bytes": json.Number("0"), "mode": json.Number(value)}
	row, err := typedRecord(values)
	if wantErr == nil {
		if err != nil || uint64(row.Mode) != want {
			t.Fatalf("valid mode changed: %+v %v, want %d", row, err, want)
		}
		return
	}
	var gotNumber, wantNumber *strconv.NumError
	if !errors.As(err, &gotNumber) || !errors.As(wantErr, &wantNumber) {
		t.Fatalf("numeric error type changed: %T %v, want %T %v", err, err, wantErr, wantErr)
	}
	if gotNumber.Func != wantNumber.Func || gotNumber.Num != wantNumber.Num || !errors.Is(gotNumber.Err, wantNumber.Err) || err.Error() != wantErr.Error() {
		t.Fatalf("numeric error changed: %+v, want %+v", gotNumber, wantNumber)
	}
	if row != (record{}) {
		t.Fatalf("invalid mode returned partial record: %+v", row)
	}
}
