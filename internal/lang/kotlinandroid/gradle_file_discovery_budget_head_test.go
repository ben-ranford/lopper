package kotlinandroid

import (
	"errors"
	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/safeio"
	"testing"
)

func TestAggregateGradleContentLimitPreservesOtherReadErrors(t *testing.T) {
	remainingBytes := int64(shared.GradleManifestByteLimit - 1)
	if !isAggregateGradleContentLimitError(remainingBytes, safeio.ErrFileTooLarge) {
		t.Fatal("expected pure size error to identify aggregate content limit")
	}
	if isAggregateGradleContentLimitError(remainingBytes, errors.Join(safeio.ErrFileTooLarge, errors.New("close failed"))) {
		t.Fatal("joined close error was incorrectly classified as only an aggregate size limit")
	}
	if isAggregateGradleContentLimitError(int64(shared.GradleManifestByteLimit), safeio.ErrFileTooLarge) {
		t.Fatal("per-file size error was incorrectly classified as aggregate limit")
	}
}
