package main

import (
	"errors"
	"io"

	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestAMD64HeaderRejectsWrongMachineAndUnboundedOffset(t *testing.T) {
	data := make([]byte, 90)
	copy(data, "MZ")
	binary.LittleEndian.PutUint32(data[60:], 64)
	copy(data[64:], "PE\x00\x00")
	binary.LittleEndian.PutUint16(data[68:], 0x8664)
	binary.LittleEndian.PutUint16(data[88:], 0x20b)
	if err := readAMD64Header(bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func([]byte){func(b []byte) { binary.LittleEndian.PutUint16(b[68:], 0x14c) }, func(b []byte) { binary.LittleEndian.PutUint32(b[60:], 0xffffffff) }, func(b []byte) { binary.LittleEndian.PutUint16(b[88:], 0x10b) }} {
		bad := append([]byte{}, data...)
		change(bad)
		if err := readAMD64Header(bytes.NewReader(bad), int64(len(bad))); err == nil {
			t.Fatal("unsupported PE identity accepted")
		}
	}
}

func TestPEAdmissionUsesBoundedActualFile(t *testing.T) {
	root := providerTestRoot(t)
	path := filepath.Join(root, "candidate.exe")
	if err := os.WriteFile(path, make([]byte, 90), 0600); err != nil {
		t.Fatal(err)
	}
	if err := requireAMD64File(path); err == nil {
		t.Fatal("non-PE file admitted")
	}
	if err := os.Truncate(path, maxFileBytes+1); err != nil {
		t.Fatal(err)
	}
	if err := requireAMD64File(path); err == nil {
		t.Fatal("oversized executable admitted")
	}
}

func TestPETruncatedReaderAndMissingFileErrors(t *testing.T) {
	if err := readAMD64Header(bytes.NewReader([]byte("MZ")), 90); !errors.Is(err, io.EOF) {
		t.Fatalf("DOS truncation error lost: %v", err)
	}
	dos := make([]byte, 64)
	copy(dos, "MZ")
	binary.LittleEndian.PutUint32(dos[60:], 64)
	if err := readAMD64Header(bytes.NewReader(dos), 90); !errors.Is(err, io.EOF) {
		t.Fatalf("COFF truncation error lost: %v", err)
	}
	missing := filepath.Join(providerTestRoot(t), "missing.exe")
	if err := requireAMD64File(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing executable error lost: %v", err)
	}
}
