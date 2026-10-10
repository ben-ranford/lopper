package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
)

// The caller owns destination exclusively. Failure keeps partial output for
// diagnosis; it never removes a runtime whose ownership/join is uncertain.
func materialize(ctx context.Context, source receipt, destination string) (receipt, error) {
	if err := canonicalDirectory(filepath.Dir(destination)); err != nil {
		return receipt{}, err
	}
	if err := os.Mkdir(destination, 0700); err != nil {
		return receipt{}, err
	}
	var budget inventoryBudget
	for _, row := range source.Records {
		if err := materializeRow(ctx, source.Root, destination, &budget, row); err != nil {
			return receipt{}, err
		}
	}
	copied, err := inventory(ctx, destination, false)
	if err != nil {
		return receipt{}, err
	}
	if err := compareMaterialisedInventory(source, copied); err != nil {
		return receipt{}, err
	}
	current, err := inventory(ctx, source.Root, true)
	if err != nil {
		return receipt{}, err
	}
	if !reflect.DeepEqual(current, source) {
		return receipt{}, errors.New("source inventory changed during materialisation")
	}
	return copied, nil
}

func compareMaterialisedInventory(source, copied receipt) error {
	if len(copied.Records) != len(source.Records) {
		return errors.New("materialised inventory count differs")
	}
	for i, row := range copied.Records {
		original := source.Records[i]
		if row.Path != original.Path || row.Bytes != original.Bytes || row.Hash != original.Hash {
			return errors.New("materialised inventory differs")
		}
	}
	return nil
}

func copyRecorded(ctx context.Context, source, destination string, row record) (returnErr error) {
	if row.Bytes > maxFileBytes || !validDigest(row.Hash) {
		return errors.New("invalid copy identity")
	}
	if err := canonicalRegular(source); err != nil {
		return err
	}
	input, err := openConfined(source, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, input.Close()) }()
	if err := verifyOpenedPath(input, source); err != nil {
		return err
	}
	before, err := input.Stat()
	if err != nil {
		return err
	}
	size := before.Size()
	if size < 0 || uint64(size) != row.Bytes {
		return errors.New("copy source size changed")
	}
	output, err := openConfined(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, output.Close()) }()
	hasher := sha256.New()
	writer := io.MultiWriter(output, hasher)
	if err := streamRecorded(ctx, input, writer, row.Bytes); err != nil {
		return err
	}
	if fmt.Sprintf("%x", hasher.Sum(nil)) != row.Hash {
		return errors.New("copy source digest changed")
	}
	return finishRecordedCopy(input, output, before)
}

// finishRecordedCopy borrows both files; the caller closes output before input.
func finishRecordedCopy(input, output *os.File, before os.FileInfo) error {
	after, err := input.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(before, after) || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return errors.New("copy source identity changed")
	}
	return output.Sync()
}

func materializeRow(ctx context.Context, source, destination string, budget *inventoryBudget, row record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := budget.admit(row.Kind == "directory"); err != nil {
		return err
	}
	if err := validateRelative(row.Path); err != nil {
		return err
	}
	target := filepath.Join(destination, filepath.FromSlash(row.Path))
	if row.Kind == "directory" {
		if row.Path != "." {
			return os.Mkdir(target, 0700)
		}
		return nil
	}
	if err := reserve(&budget.bytes, row.Bytes, maxTotalBytes); err != nil {
		return err
	}
	from := filepath.Join(source, filepath.FromSlash(row.Path))
	if row.Kind == "alias" {
		from = row.Target
	}
	return copyRecorded(ctx, from, target, row)
}
