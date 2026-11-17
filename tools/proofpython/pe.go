package main

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
)

// Only fixed DOS/COFF/optional-header bytes are needed for admission. Avoid a
// general PE parser whose file-supplied table sizes could allocate before policy.
func requireAMD64File(path string) (returnErr error) {
	if err := canonicalRegular(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Size() < 90 || info.Size() > maxFileBytes {
		return errors.New("invalid PE file size")
	}
	file, err := openConfined(path, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	if err := verifyOpenedPath(file, path); err != nil {
		return err
	}
	return readAMD64Header(file, info.Size())
}

func readAMD64Header(reader io.ReaderAt, size int64) error {
	var dos [64]byte
	if _, err := reader.ReadAt(dos[:], 0); err != nil {
		return err
	}
	if string(dos[:2]) != "MZ" {
		return errors.New("missing DOS executable header")
	}
	offset := int64(binary.LittleEndian.Uint32(dos[60:]))
	if offset < 64 || offset > size-26 {
		return errors.New("executable PE header offset outside file")
	}
	var coff [26]byte
	if _, err := reader.ReadAt(coff[:], offset); err != nil {
		return err
	}
	if string(coff[:4]) != "PE\x00\x00" || binary.LittleEndian.Uint16(coff[4:]) != 0x8664 || binary.LittleEndian.Uint16(coff[24:]) != 0x20b {
		return errors.New("provider requires AMD64 PE32+ executable")
	}
	return nil
}
