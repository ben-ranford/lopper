package thresholds

import (
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
)

const (
	remotePolicyPinKey   = "sha256"
	maxRemotePolicyBytes = 1 << 20
)

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
