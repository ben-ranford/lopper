package dashboard

import (
	"bufio"
	"encoding/csv"
	"errors"
	"io"
	"reflect"
	"testing"
	"unsafe"
)

type failAfterNWrites struct {
	remaining int
}

func (f *failAfterNWrites) Write(p []byte) (int, error) {
	if f.remaining == 0 {
		return 0, errors.New("boom")
	}
	f.remaining--
	return len(p), nil
}

func writerWithBufferSize(t *testing.T, out io.Writer, size int) *csv.Writer {
	t.Helper()

	writer := csv.NewWriter(out)
	field := reflect.ValueOf(writer).Elem().FieldByName("w")
	if !field.IsValid() {
		t.Fatal("csv.Writer is missing the internal buffer field")
	}
	reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem().Set(reflect.ValueOf(bufio.NewWriterSize(out, size)))
	return writer
}

func TestWriteDashboardCrossRepoRowsCSVHeaderError(t *testing.T) {
	writer := writerWithBufferSize(t, &failAfterNWrites{remaining: 1}, 1)

	err := writeDashboardCrossRepoRowsCSV(writer, []CrossRepoDependency{{
		Name:         "shared-dep",
		Count:        3,
		Repositories: []string{"api", "web", "worker"},
	}})
	if err == nil {
		t.Fatal("expected cross-repo header write to fail")
	}
}
