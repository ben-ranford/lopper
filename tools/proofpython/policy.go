package main

import (
	"errors"
	"path"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

const (
	policyVersion     = "proof-python-1"
	maxEntries        = 65536
	maxDirectories    = 8192
	maxFiles          = 57343
	maxFileBytes      = 512 << 20
	maxTotalBytes     = 4 << 30
	maxReceiptBytes   = 32 << 20
	maxMetadataBytes  = 32 << 20
	maxStringBytes    = 8192
	maxPathUnits      = 2048
	maxDepth          = 32
	maxFields         = 16
	maxReceiptDepth   = 8
	maxModules        = 512
	maxOSModules      = 256
	maxOSFileBytes    = 64 << 20
	maxOSBytes        = 512 << 20
	maxStreamBytes    = 1 << 20
	maxRawBytes       = 4 << 20
	maxHandshakeBytes = 64 << 10
	maxErrors         = 256
	maxErrorBytes     = 4096
	maxDiagnostics    = 1 << 20
	ioBlockBytes      = 64 << 10
)

func reserve(used *uint64, count, limit uint64) error {
	if *used > limit || count > limit-*used {
		return errors.New("python provider resource limit exceeded")
	}
	*used += count
	return nil
}

func validatePathSize(value string) error {
	if !utf8.ValidString(value) || len(value) > maxStringBytes || len(utf16.Encode([]rune(value))) > maxPathUnits || strings.ContainsAny(value, "\x00\r\n") {
		return errors.New("invalid or oversized Python provider path")
	}
	return nil
}

func validateRelative(value string) error {
	if err := validatePathSize(value); err != nil {
		return err
	}
	if value == "." {
		return nil
	}
	if value == "" || path.IsAbs(value) || path.Clean(value) != value || strings.ContainsAny(value, `\:`) {
		return errors.New("noncanonical inventory path")
	}
	parts := strings.Split(value, "/")
	if len(parts) > maxDepth {
		return errors.New("inventory depth exceeded")
	}
	for _, part := range parts {
		if part == ".." || part == "." || len(utf16.Encode([]rune(part))) > 255 || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return errors.New("invalid inventory component")
		}
	}
	return nil
}
