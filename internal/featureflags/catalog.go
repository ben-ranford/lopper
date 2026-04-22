package featureflags

import (
	"bytes"
	// Import embed for the feature catalog go:embed directive.
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
)

//go:embed features.json
var embeddedCatalog []byte

//go:embed release_locks.json
var embeddedReleaseLocks []byte

var defaultRegistry, defaultRegistryErr = newDefaultRegistry()

func ParseCatalog(data []byte) ([]Flag, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var flags []Flag
	if err := decoder.Decode(&flags); err != nil {
		return nil, fmt.Errorf("invalid feature catalog JSON: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return nil, fmt.Errorf("invalid feature catalog JSON: multiple JSON values")
	}
	registry, err := NewRegistry(flags)
	if err != nil {
		return nil, err
	}
	return registry.Flags(), nil
}

func FormatCatalog(flags []Flag) ([]byte, error) {
	registry, err := NewRegistry(flags)
	if err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(registry.Flags(), "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal feature catalog: %w", err)
	}
	return append(data, '\n'), nil
}

func newDefaultRegistry() (*Registry, error) {
	flags, err := ParseCatalog(embeddedCatalog)
	if err != nil {
		return emptyRegistry(), err
	}
	return NewRegistry(flags)
}
