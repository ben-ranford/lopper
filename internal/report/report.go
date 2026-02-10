package report

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

type Format string

const (
	FormatTable Format = "table"
	FormatJSON  Format = "json"
)

const SchemaVersion = "0.1.0"

var ErrUnknownFormat = errors.New("unknown format")

func ParseFormat(value string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", string(FormatTable):
		return FormatTable, nil
	case string(FormatJSON):
		return FormatJSON, nil
	default:
		return "", fmt.Errorf("%w: %s", ErrUnknownFormat, value)
	}
}

type Report struct {
	SchemaVersion        string             `json:"schemaVersion"`
	GeneratedAt          time.Time          `json:"generatedAt"`
	RepoPath             string             `json:"repoPath"`
	Dependencies         []DependencyReport `json:"dependencies"`
	Summary              *Summary           `json:"summary,omitempty"`
	Warnings             []string           `json:"warnings,omitempty"`
	WasteIncreasePercent *float64           `json:"wasteIncreasePercent,omitempty"`
}

type Summary struct {
	DependencyCount   int     `json:"dependencyCount"`
	UsedExportsCount  int     `json:"usedExportsCount"`
	TotalExportsCount int     `json:"totalExportsCount"`
	UsedPercent       float64 `json:"usedPercent"`
}

type DependencyReport struct {
	Name                 string        `json:"name"`
	UsedExportsCount     int           `json:"usedExportsCount"`
	TotalExportsCount    int           `json:"totalExportsCount"`
	UsedPercent          float64       `json:"usedPercent"`
	EstimatedUnusedBytes int64         `json:"estimatedUnusedBytes"`
	TopUsedSymbols       []SymbolUsage `json:"topUsedSymbols,omitempty"`
	UsedImports          []ImportUse   `json:"usedImports,omitempty"`
	UnusedImports        []ImportUse   `json:"unusedImports,omitempty"`
	UnusedExports        []SymbolRef   `json:"unusedExports,omitempty"`
}

type SymbolUsage struct {
	Name   string `json:"name"`
	Module string `json:"module,omitempty"`
	Count  int    `json:"count"`
}

type ImportUse struct {
	Name      string     `json:"name"`
	Module    string     `json:"module"`
	Locations []Location `json:"locations,omitempty"`
}

type SymbolRef struct {
	Name   string `json:"name"`
	Module string `json:"module"`
}

type Location struct {
	File   string `json:"file"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
}
