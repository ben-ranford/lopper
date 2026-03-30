package thresholds

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	remotePolicyPinKey   = "sha256"
	maxRemotePolicyBytes = 1 << 20
)

var remotePolicyHTTPClient = &http.Client{Timeout: 10 * time.Second}

func parseRemoteURL(raw string) (*url.URL, bool) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, false
	}
	if strings.TrimSpace(parsed.Host) == "" {
		return nil, false
	}
	return parsed, true
}

func canonicalRemotePolicyURL(raw string) (string, error) {
	parsed, ok := parseRemoteURL(raw)
	if !ok {
		return "", fmt.Errorf("invalid remote policy URL: %s", raw)
	}
	pin, err := extractRemotePolicyPin(parsed.Fragment)
	if err != nil {
		return "", err
	}
	parsed.Fragment = remotePolicyPinKey + "=" + pin
	return parsed.String(), nil
}

func extractRemotePolicyPin(fragment string) (string, error) {
	trimmed := strings.TrimSpace(fragment)
	if trimmed == "" {
		return "", fmt.Errorf("remote policy packs must include a sha256 pin (example: #sha256=<hex>)")
	}
	key, value, ok := strings.Cut(trimmed, "=")
	if !ok {
		return "", fmt.Errorf("invalid remote policy pin %q; expected sha256=<hex>", fragment)
	}
	if strings.ToLower(strings.TrimSpace(key)) != remotePolicyPinKey {
		return "", fmt.Errorf("unsupported remote policy pin key %q; expected sha256", key)
	}
	normalized := strings.ToLower(strings.TrimSpace(value))
	if len(normalized) != 64 {
		return "", fmt.Errorf("invalid remote policy sha256 pin length: got %d, expected 64", len(normalized))
	}
	if _, err := hex.DecodeString(normalized); err != nil {
		return "", fmt.Errorf("invalid remote policy sha256 pin: %w", err)
	}
	return normalized, nil
}

func readRemotePolicyFile(location string) (_ []byte, err error) {
	parsed, err := url.Parse(location)
	if err != nil {
		return nil, fmt.Errorf("parse remote policy URL: %w", err)
	}
	expectedHash, err := extractRemotePolicyPin(parsed.Fragment)
	if err != nil {
		return nil, err
	}
	parsed.Fragment = ""

	response, err := remotePolicyHTTPClient.Get(parsed.String())
	if err != nil {
		return nil, fmt.Errorf("fetch remote policy: %w", err)
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, fmt.Errorf("fetch remote policy: unexpected status %d", response.StatusCode)
	}

	limited := io.LimitReader(response.Body, maxRemotePolicyBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read remote policy response: %w", err)
	}
	if len(data) > maxRemotePolicyBytes {
		return nil, fmt.Errorf("remote policy exceeded size limit of %d bytes", maxRemotePolicyBytes)
	}

	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if got != expectedHash {
		return nil, fmt.Errorf("remote policy sha256 mismatch: expected %s, got %s", expectedHash, got)
	}
	return data, nil
}
