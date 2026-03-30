package safeio

import (
	"os"
	"path/filepath"
)

type atomicWriteSession struct {
	root      *os.Root
	targetRel string
	tempRel   string
	tempFile  *os.File
}

func newAtomicWriteSession(root *os.Root, targetRel string, perm os.FileMode) (*atomicWriteSession, error) {
	tempRel, tempFile, err := createAtomicTempFile(root, filepath.Dir(targetRel), perm)
	if err != nil {
		return nil, err
	}

	return &atomicWriteSession{
		root:      root,
		targetRel: targetRel,
		tempRel:   tempRel,
		tempFile:  tempFile,
	}, nil
}

func (s *atomicWriteSession) writeAndCommit(data []byte, perm os.FileMode) error {
	if _, err := writeFileFn(s.tempFile, data); err != nil {
		return err
	}
	if err := chmodFileFn(s.tempFile, perm); err != nil {
		return err
	}
	if err := s.closeTempFile(); err != nil {
		return err
	}
	if err := renameFileFn(s.root, s.tempRel, s.targetRel); err != nil {
		return err
	}
	s.tempRel = ""
	return nil
}

func (s *atomicWriteSession) closeTempFile() error {
	if s.tempFile == nil {
		return nil
	}
	if err := closeFileFn(s.tempFile); err != nil {
		return err
	}
	s.tempFile = nil
	return nil
}

func (s *atomicWriteSession) cleanup() error {
	return cleanupTempFileFn(s.root, s.tempRel, s.tempFile)
}
