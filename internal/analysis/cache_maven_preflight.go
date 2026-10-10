package analysis

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"unicode/utf8"

	"github.com/ben-ranford/lopper/internal/report/model"
)

type mavenJSONShape uint8

const (
	mavenPointerJSON mavenJSONShape = iota
	mavenPayloadJSON
	mavenEnvelopeJSON
	mavenDocumentJSON
	mavenPropertiesJSON
	mavenDeclarationJSON
	mavenEntriesJSON
	mavenDeclarationsJSON
	mavenStringJSON
	mavenNumberJSON
	mavenOpaqueJSON
)

var mavenJSONFields = map[mavenJSONShape]map[string]mavenJSONShape{
	mavenPointerJSON:     {"inputDigest": mavenStringJSON, "objectDigest": mavenStringJSON},
	mavenPayloadJSON:     {"maven": mavenEnvelopeJSON, "report": mavenOpaqueJSON, "pythonManifests": mavenOpaqueJSON, "pythonManifestCatalog": mavenOpaqueJSON, "usageIncompleteReport": mavenOpaqueJSON, "usageIncompleteDependencies": mavenOpaqueJSON, "suppressedUnusedImportsByDependency": mavenOpaqueJSON},
	mavenEnvelopeJSON:    {"version": mavenNumberJSON, "policy": mavenStringJSON, "entries": mavenEntriesJSON},
	mavenDocumentJSON:    {"path": mavenStringJSON, "properties": mavenPropertiesJSON, "dependencies": mavenDeclarationsJSON, "managed": mavenDeclarationsJSON, "stage": mavenStringJSON, "kind": mavenStringJSON},
	mavenDeclarationJSON: {"groupId": mavenStringJSON, "artifactId": mavenStringJSON, "version": mavenStringJSON, "type": mavenStringJSON, "scope": mavenStringJSON},
}

type mavenJSONPreflight struct {
	decoder            *json.Decoder
	values             int
	recordDeclarations int
	canonicalBytes     int
	inEvidence         bool
}

func preflightMavenJSON(data []byte, shape mavenJSONShape) error {
	if !utf8.Valid(data) || !validMavenJSONSurrogates(data) {
		return errors.New("lossy Maven cache JSON encoding")
	}
	scanner := mavenJSONPreflight{decoder: json.NewDecoder(bytes.NewReader(data)), inEvidence: shape != mavenPointerJSON && shape != mavenPayloadJSON}
	scanner.decoder.UseNumber()
	if err := scanner.value(shape); err != nil {
		return err
	}
	return mavenJSONTrailing(scanner.decoder)
}
func (s *mavenJSONPreflight) value(shape mavenJSONShape) error {
	switch shape {
	case mavenOpaqueJSON:
		var raw json.RawMessage
		return s.decoder.Decode(&raw)
	case mavenStringJSON:
		_, err := s.stringValue()
		return err
	case mavenNumberJSON:
		token, err := s.decoder.Token()
		if err != nil {
			return err
		}
		if number, ok := token.(json.Number); !ok || string(number) != strconv.Itoa(mavenCacheEnvelopeVersion) {
			return errors.New("invalid Maven cache version")
		}
		return s.charge(len(strconv.Itoa(mavenCacheEnvelopeVersion)))
	case mavenEntriesJSON:
		return s.array(mavenDocumentJSON, model.MavenAdapterEntryLimit)
	case mavenDeclarationsJSON:
		return s.array(mavenDeclarationJSON, model.MavenEvidenceRecordLimit)
	default:
		return s.object(shape)
	}
}
func (s *mavenJSONPreflight) delimiter(want json.Delim) error {
	token, err := s.decoder.Token()
	if err != nil {
		return err
	}
	if token != want {
		return fmt.Errorf("expected Maven cache delimiter %q", want)
	}
	return s.charge(1)
}
func (s *mavenJSONPreflight) array(shape mavenJSONShape, limit int) error {
	if err := s.delimiter('['); err != nil {
		return err
	}
	count := 0
	for s.decoder.More() {
		if err := s.separator(count); err != nil {
			return err
		}
		count++
		if count > limit {
			return model.ErrMavenEvidenceLimit
		}
		if shape == mavenDeclarationJSON {
			if err := s.countDeclaration(); err != nil {
				return err
			}
		}
		if err := s.value(shape); err != nil {
			return err
		}
	}
	return s.delimiter(']')
}
func (s *mavenJSONPreflight) countDeclaration() error {
	s.recordDeclarations++
	if s.recordDeclarations > model.MavenEvidenceRecordLimit {
		return model.ErrMavenEvidenceLimit
	}
	return s.countValue()
}
func (s *mavenJSONPreflight) countValue() error {
	s.values++
	if s.values > model.MavenEvidenceValueLimit {
		return model.ErrMavenEvidenceLimit
	}
	return nil
}

type mavenJSONRecordState struct{ path, stage, kind string }

func (s *mavenJSONPreflight) object(shape mavenJSONShape) error {
	if shape == mavenEnvelopeJSON {
		s.inEvidence = true
		defer func() { s.inEvidence = false }()
	}
	if shape == mavenDocumentJSON {
		s.recordDeclarations = 0
	}
	valuesBefore := s.values
	state := mavenJSONRecordState{}
	if err := s.delimiter('{'); err != nil {
		return err
	}
	seen := make(map[string]bool)
	for s.decoder.More() {
		if err := s.objectField(shape, seen, &state); err != nil {
			return err
		}
	}
	if err := s.delimiter('}'); err != nil {
		return err
	}
	if err := validateMavenJSONKeys(shape, seen); err != nil {
		return err
	}
	if shape == mavenDocumentJSON {
		return model.ValidateMavenManifestState(state.path, state.stage, state.kind, s.values-valuesBefore)
	}
	return nil
}
func (s *mavenJSONPreflight) objectField(shape mavenJSONShape, seen map[string]bool, state *mavenJSONRecordState) error {
	if err := s.separator(len(seen)); err != nil {
		return err
	}
	token, err := s.decoder.Token()
	if err != nil {
		return err
	}
	key, ok := token.(string)
	if !ok || seen[key] {
		return errors.New("duplicate/invalid Maven cache key")
	}
	child, ok := mavenJSONFields[shape][key]
	if shape == mavenPropertiesJSON {
		child, ok = mavenStringJSON, true
		if err := s.propertyCount(len(seen)); err != nil {
			return err
		}
	}
	if !ok {
		return errors.New("unknown Maven cache key")
	}
	seen[key] = true
	if err := s.charge(model.MavenJSONStringSize(key) + 1); err != nil {
		return err
	}
	return s.fieldValue(shape, child, key, state)
}
func (s *mavenJSONPreflight) propertyCount(previous int) error {
	if previous >= model.MavenEvidenceRecordLimit {
		return model.ErrMavenEvidenceLimit
	}
	return s.countValue()
}
func (s *mavenJSONPreflight) fieldValue(shape, child mavenJSONShape, key string, state *mavenJSONRecordState) error {
	if child != mavenStringJSON {
		return s.value(child)
	}
	value, err := s.stringValue()
	if err != nil {
		return err
	}
	if shape == mavenEnvelopeJSON && value != mavenCacheIdentityPolicy {
		return errors.New("invalid Maven cache policy")
	}
	if shape == mavenDocumentJSON {
		switch key {
		case "path":
			state.path = value
		case "stage":
			state.stage = value
		case "kind":
			state.kind = value
		}
	}
	return nil
}
func (s *mavenJSONPreflight) stringValue() (string, error) {
	token, err := s.decoder.Token()
	if err != nil {
		return "", err
	}
	value, ok := token.(string)
	if !ok {
		return "", errors.New("expected Maven cache string")
	}
	return value, s.charge(model.MavenJSONStringSize(value))
}
func (s *mavenJSONPreflight) separator(previous int) error {
	if previous == 0 {
		return nil
	}
	return s.charge(1)
}
func (s *mavenJSONPreflight) charge(size int) error {
	if !s.inEvidence {
		return nil
	}
	if size > model.MavenEvidenceByteLimit-s.canonicalBytes {
		return model.ErrMavenEvidenceLimit
	}
	s.canonicalBytes += size
	return nil
}
func validateMavenJSONKeys(shape mavenJSONShape, seen map[string]bool) error {
	if shape == mavenPayloadJSON {
		if !seen["maven"] || !seen["report"] {
			return errors.New("missing Maven cache envelope/report")
		}
		return nil
	}
	for key := range mavenJSONFields[shape] {
		if !seen[key] {
			return errors.New("missing Maven cache field")
		}
	}
	return nil
}

// Reject unpaired surrogate escapes before encoding/json can replace them.
func validMavenJSONSurrogates(data []byte) bool {
	for i := 0; i < len(data); i++ {
		if data[i] != '\\' {
			continue
		}
		next, ok := mavenJSONEscapeEnd(data, i+1)
		if !ok {
			return false
		}
		i = next
	}
	return true
}
func mavenJSONEscapeEnd(data []byte, index int) (int, bool) {
	if index >= len(data) {
		return index, false
	}
	if data[index] != 'u' {
		return index, true
	}
	value, ok := mavenJSONHex(data, index+1)
	if !ok {
		return index, false
	}
	index += 4
	if value >= 0xdc00 && value <= 0xdfff {
		return index, false
	}
	if value < 0xd800 || value > 0xdbff {
		return index, true
	}
	if index+6 >= len(data) || data[index+1] != '\\' || data[index+2] != 'u' {
		return index, false
	}
	low, ok := mavenJSONHex(data, index+3)
	return index + 6, ok && low >= 0xdc00 && low <= 0xdfff
}

func mavenJSONHex(data []byte, start int) (uint64, bool) {
	if start+4 > len(data) {
		return 0, false
	}
	value, err := strconv.ParseUint(string(data[start:start+4]), 16, 16)
	return value, err == nil
}
