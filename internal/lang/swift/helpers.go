package swift

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"github.com/ben-ranford/lopper/internal/lang/shared"
)

var (
	swiftImportPattern          = regexp.MustCompile(`^\s*(?:@[A-Za-z_][A-Za-z0-9_]*(?:\([^)]*\))?\s+)*import\s+(?:(?:typealias|struct|class|enum|protocol|let|var|func|operator)\s+)?([A-Za-z_][A-Za-z0-9_]*)(?:\.[A-Za-z_][A-Za-z0-9_]*)*`)
	swiftUpperIdentifierPattern = regexp.MustCompile(`\b[A-Z][A-Za-z0-9_]*\b`)
	swiftTypeDeclarationPattern = regexp.MustCompile(`\b(?:actor|class|enum|protocol|struct|typealias)\s+([A-Za-z_][A-Za-z0-9_]*)`)
	stringFieldPattern          = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\s*:\s*"((?:\\.|[^"])*)"`)
	podDeclarationPattern       = regexp.MustCompile(`^\s*pod\s*(?:\(\s*)?['"]([^'"]+)['"]`)

	swiftSkippedDirs = map[string]bool{
		".build":      true,
		".swiftpm":    true,
		"carthage":    true,
		"deriveddata": true,
		"pods":        true,
	}

	standardSwiftSymbols = toLookupSet([]string{
		"Swift",
		"Foundation",
		"FoundationNetworking",
		"PackageDescription",
		"PackagePlugin",
		"CompilerPluginSupport",
		"Dispatch",
		"Darwin",
		"Glibc",
		"XCTest",
		"SwiftUI",
		"Combine",
		"UIKit",
		"AppKit",
		"CoreGraphics",
		"CoreFoundation",
		"CoreData",
		"AVFoundation",
		"Security",
		"MapKit",
		"WebKit",
		"StoreKit",
		"CloudKit",
		"UserNotifications",
		"CryptoKit",
		"Observation",
		"SwiftData",
		"OSLog",
		"os",
		"String",
		"Substring",
		"Character",
		"Int",
		"Int8",
		"Int16",
		"Int32",
		"Int64",
		"UInt",
		"UInt8",
		"UInt16",
		"UInt32",
		"UInt64",
		"Double",
		"Float",
		"Bool",
		"Array",
		"Dictionary",
		"Set",
		"Optional",
		"Result",
		"Any",
		"AnyObject",
		"Data",
		"Date",
		"URL",
		"UUID",
		"Decimal",
		"Error",
		"Never",
	})
)

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func maybeSkipSwiftDir(name string) error {
	if shouldSkipDir(name) {
		return filepath.SkipDir
	}
	return nil
}

func normalizeDependencyID(value string) string {
	value = shared.NormalizeDependencyID(value)
	value = strings.ReplaceAll(value, "_", "-")
	return strings.Trim(value, "-")
}

func shouldSkipDir(name string) bool {
	if shared.ShouldSkipCommonDir(name) {
		return true
	}
	return swiftSkippedDirs[strings.ToLower(name)]
}

func setLookup(target map[string]string, key string, depID string) {
	_ = setLookupWithStatus(target, key, depID)
}

func setLookupWithStatus(target map[string]string, key string, depID string) bool {
	if key == "" || depID == "" {
		return false
	}
	if existing, ok := target[key]; ok {
		if existing != depID {
			target[key] = ambiguousDependencyKey
			return true
		}
		return false
	}
	target[key] = depID
	return false
}

func resolveLookup(target map[string]string, key string) (string, bool) {
	value, ok := target[key]
	if !ok || value == "" || value == ambiguousDependencyKey {
		return "", false
	}
	return value, true
}

func lookupKey(value string) string {
	trimmed := strings.TrimSpace(strings.ToLower(value))
	if trimmed == "" {
		return ""
	}
	builder := strings.Builder{}
	builder.Grow(len(trimmed))
	for _, r := range trimmed {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func dedupeWarnings(warnings []string) []string {
	if len(warnings) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(warnings))
	result := make([]string, 0, len(warnings))
	for _, warning := range warnings {
		warning = strings.TrimSpace(warning)
		if warning == "" {
			continue
		}
		if _, ok := seen[warning]; ok {
			continue
		}
		seen[warning] = struct{}{}
		result = append(result, warning)
	}
	return result
}

func dedupeStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, trimmed)
	}
	return result
}

func toLookupSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		key := lookupKey(value)
		if key == "" {
			continue
		}
		result[key] = struct{}{}
	}
	return result
}
