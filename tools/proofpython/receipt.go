package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Records are flat, typed JSON lines; no generic recursive object allocation.
type record struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	Hash   string `json:"hash"`
	Bytes  uint64 `json:"bytes"`
	Mode   uint32 `json:"mode"`
	Target string `json:"target"`
}

type receipt struct {
	Version string
	Root    string
	Records []record
}

func validDigest(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func boundedBytes(reader io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("provider input exceeds limit")
	}
	return data, nil
}

func readAuthenticatedReceipt(reader io.Reader, expected string) (receipt, error) {
	if !validDigest(expected) {
		return receipt{}, errors.New("missing or invalid independent receipt digest")
	}
	data, err := boundedBytes(reader, maxReceiptBytes)
	if err != nil {
		return receipt{}, err
	}
	if fmt.Sprintf("%x", sha256.Sum256(data)) != expected {
		return receipt{}, errors.New("receipt differs from independent expectation")
	}
	return parseReceipt(data)
}

func parseReceipt(data []byte) (receipt, error) {
	if len(data) > maxReceiptBytes {
		return receipt{}, errors.New("receipt limit exceeded")
	}
	reader := bufio.NewReaderSize(bytes.NewReader(data), maxHandshakeBytes)
	result := receipt{}
	var metadata uint64
	seen := map[string]bool{}
	records := 0
	for {
		line, err := reader.ReadSlice('\n')
		if errors.Is(err, io.EOF) && len(line) == 0 {
			break
		}
		if err != nil {
			return receipt{}, errors.New("receipt has incomplete or oversized record")
		}
		if records >= maxEntries {
			return receipt{}, errors.New("receipt record limit exceeded")
		}
		records++
		row, err := decodeRecord(line, &metadata)
		if err != nil {
			return receipt{}, err
		}
		if err := appendReceiptRow(&result, seen, row); err != nil {
			return receipt{}, err
		}
	}
	if result.Version == "" || len(result.Records) == 0 {
		return receipt{}, errors.New("empty receipt")
	}
	return result, nil
}

func checkJSONStrings(data []byte) error {
	state := jsonTokenState{}
	for _, value := range data {
		state.consume(value)
		if state.size > maxStringBytes {
			return errors.New("receipt token limit exceeded")
		}
	}
	if state.inside {
		return errors.New("unterminated receipt string")
	}
	return nil
}

type jsonTokenState struct {
	inside, escaped bool
	size            int
}

func (js *jsonTokenState) consume(value byte) {
	if !js.inside {
		js.consumeOutside(value)
		return
	}
	if value == '"' && !js.escaped {
		js.inside = false
		js.size = 0
		return
	}
	js.size++
	if js.escaped {
		js.escaped = false
	} else if value == '\\' {
		js.escaped = true
	}
}

func (js *jsonTokenState) consumeOutside(value byte) {
	if value == '"' {
		js.inside = true
		js.size = 0
		return
	}
	if strings.ContainsRune("{}[],: \t\r\n", rune(value)) {
		js.size = 0
	} else {
		js.size++
	}
}

func decodeRecord(data []byte, metadata *uint64) (record, error) {
	if err := checkJSONStrings(data); err != nil {
		return record{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return record{}, errors.New("receipt record must be object")
	}
	values := map[string]any{}
	for decoder.More() {
		if err := decodeField(decoder, values, metadata); err != nil {
			return record{}, err
		}
	}
	if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
		return record{}, errors.New("incomplete receipt object")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return record{}, errors.New("receipt trailing tokens")
	}
	return typedRecord(values)
}

func knownField(name string) bool {
	switch name {
	case "kind", "path", "hash", "bytes", "mode", "target":
		return true
	}
	return false
}

func typedRecord(values map[string]any) (record, error) {
	var row record
	for name, destination := range map[string]*string{"kind": &row.Kind, "path": &row.Path, "hash": &row.Hash, "target": &row.Target} {
		value, ok := values[name].(string)
		if !ok {
			return record{}, errors.New("receipt requires string field")
		}
		*destination = value
	}
	size, err := recordNumber(values["bytes"], 64)
	if err != nil {
		return record{}, err
	}
	row.Bytes = size
	mode, err := recordNumber(values["mode"], 64)
	if err != nil || mode > math.MaxUint32 {
		_, err = recordNumber(values["mode"], 32)
		return record{}, err
	}
	row.Mode = uint32(mode)
	return row, nil
}

func recordNumber(value any, bits int) (uint64, error) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, errors.New("receipt requires unsigned integer")
	}
	return strconv.ParseUint(string(number), 10, bits)

}

func encodeReceipt(value receipt) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	if err := encoder.Encode(record{Kind: "header", Path: value.Root, Hash: policyVersion}); err != nil {
		return nil, err
	}
	for _, row := range value.Records {
		data, err := json.Marshal(row)
		if err != nil {
			return nil, err
		}
		if output.Len() > maxReceiptBytes-len(data)-1 {
			return nil, errors.New("receipt size exceeds policy")
		}
		output.Write(data)
		output.WriteByte('\n')
	}
	if _, err := parseReceipt(output.Bytes()); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func authenticateFile(path, expected string) error {
	return authenticateFileContext(context.Background(), path, expected)
}

func authenticateFileContext(ctx context.Context, path, expected string) error {
	if !validDigest(expected) {
		return errors.New("invalid independent file digest")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	actual, err := hashRegular(ctx, path, info.Size())
	if err != nil {
		return err
	}
	if actual != expected {
		return errors.New("independent file digest mismatch")
	}
	return nil
}

func validateStoredRecord(row record) error {
	if row.Kind == "buildinfo" {
		return validateBuildRecord(row)
	}
	if row.Kind == "observation" {
		return validateObservationRecord(row)
	}
	if row.Mode > 0777 || row.Bytes > maxFileBytes || !validIdentity(row.Target) {
		return errors.New("invalid record metadata")
	}
	switch row.Kind {
	case "directory":
		if row.Hash != "" || row.Bytes != 0 {
			return errors.New("invalid directory record")
		}
		return validateRelative(row.Path)
	case "file":
		if !validDigest(row.Hash) {
			return errors.New("invalid file digest")
		}
		return validateRelative(row.Path)
	case "adapter", "os", "observer", "discovery":
		if !validDigest(row.Hash) || !filepath.IsAbs(row.Path) {
			return errors.New("invalid external record")
		}
		return validatePathSize(row.Path)
	default:
		return errors.New("unknown record kind")
	}
}

func appendReceiptRow(result *receipt, seen map[string]bool, row record) error {
	if result.Version == "" {
		if row.Kind != "header" || row.Hash != policyVersion || row.Bytes != 0 || row.Mode != 0 || row.Target != "" {
			return errors.New("invalid receipt header")
		}
		if err := validatePathSize(row.Path); err != nil {
			return err
		}
		if !filepath.IsAbs(row.Path) {
			return errors.New("receipt root is not absolute")
		}
		result.Version, result.Root = row.Hash, row.Path
		return nil
	}
	if err := validateStoredRecord(row); err != nil {
		return err
	}
	key := strings.ToLower(row.Path)
	if seen[key] {
		return errors.New("duplicate receipt path")
	}
	seen[key] = true
	result.Records = append(result.Records, row)
	return nil
}

func decodeField(decoder *json.Decoder, values map[string]any, metadata *uint64) error {
	key, err := decoder.Token()
	if err != nil {
		return err
	}
	name, ok := key.(string)
	if !ok || !knownField(name) {
		return errors.New("unknown receipt field")
	}
	if _, exists := values[name]; exists {
		return errors.New("duplicate receipt field")
	}
	value, err := decoder.Token()
	if err != nil {
		return err
	}
	if _, nested := value.(json.Delim); nested || value == nil {
		return errors.New("nested or null receipt field")
	}
	if err := reserve(metadata, uint64(len(name)+len(fmt.Sprint(value))), maxMetadataBytes); err != nil {
		return err
	}
	values[name] = value
	return nil
}

func validIdentity(value string) bool {
	if len(value) != 33 || value[16] != ':' {
		return false
	}
	_, first := strconv.ParseUint(value[:16], 16, 64)
	_, second := strconv.ParseUint(value[17:], 16, 64)
	return first == nil && second == nil && value == strings.ToLower(value)
}

func validateObservationRecord(row record) error {
	if row.Path != "@held-probe" || row.Mode != 0 || row.Bytes == 0 || row.Bytes > 0xffffffff || !validDigest(row.Hash) || len(row.Target) > maxStringBytes {
		return errors.New("invalid held-probe evidence")
	}
	return validateProbeJSON([]byte(row.Target))
}

func validateBuildRecord(row record) error {
	if row.Path != "@observer-build" || row.Mode != 0 || row.Bytes != uint64(len(row.Target)) || len(row.Target) > maxStringBytes || row.Hash != fmt.Sprintf("%x", sha256.Sum256([]byte(row.Target))) {
		return errors.New("invalid observer build record")
	}
	return nil
}
