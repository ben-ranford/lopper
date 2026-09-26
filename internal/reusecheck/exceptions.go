package reusecheck

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"regexp"
	"strings"
)

// Exception is exact-source scoped so edits require renewed review. The tracked
// file and linked issue are reviewed in the PR; no inline suppression is accepted.
type Exception struct {
	Path     string `json:"path"`
	Function string `json:"function"`
	Rule     string `json:"rule"`
	SHA256   string `json:"sha256"`
	Reason   string `json:"reason"`
	Issue    string `json:"issue"`
}

func ReadExceptions(reader io.Reader) ([]Exception, error) {
	decoder := json.NewDecoder(reader)
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return nil, err
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, fmt.Errorf("exception file must contain a JSON array")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("exception file must contain one JSON array")
	}
	var exceptions []Exception
	exceptionDecoder := json.NewDecoder(bytes.NewReader(raw))
	exceptionDecoder.DisallowUnknownFields()
	if err := exceptionDecoder.Decode(&exceptions); err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	for _, item := range exceptions {
		key := item.Path + "\x00" + item.Function + "\x00" + item.Rule
		if !validException(item) || seen[key] {
			return nil, fmt.Errorf("invalid or duplicate helper exception for %s", item.Path)
		}
		seen[key] = true
	}
	return exceptions, nil
}

func Approved(finding Finding, source []byte, exceptions []Exception) bool {
	digest := fmt.Sprintf("%x", sha256.Sum256(source))
	for _, item := range exceptions {
		if item.Path == finding.Path && item.Function == finding.Function && item.Rule == finding.Rule && item.SHA256 == digest {
			return true
		}
	}
	return false
}

var exceptionIssue = regexp.MustCompile(`^https://github\.com/ben-ranford/lopper/issues/[1-9][0-9]*$`)

func validException(item Exception) bool {
	digest, err := hex.DecodeString(item.SHA256)
	return fs.ValidPath(item.Path) && strings.HasSuffix(item.Path, ".go") && !strings.Contains(item.Path, `\`) &&
		strings.TrimSpace(item.Function) != "" && knownRule(item.Rule) && strings.TrimSpace(item.Reason) != "" &&
		exceptionIssue.MatchString(item.Issue) && err == nil && len(digest) == sha256.Size
}

func knownRule(rule string) bool {
	if rule == "dependency-report-mapping" {
		return true
	}
	for _, owner := range collectionOwners {
		if owner.rule == rule {
			return true
		}
	}
	return false
}
