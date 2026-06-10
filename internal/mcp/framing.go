package mcp

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const maxFrameBytes = 16 * 1024 * 1024

func readFrame(reader *bufio.Reader) ([]byte, error) {
	contentLength, err := readContentLengthHeader(reader)
	if err != nil {
		return nil, err
	}

	body := make([]byte, contentLength)
	if _, err := io.ReadFull(reader, body); err != nil {
		return nil, err
	}
	return body, nil
}

func readContentLengthHeader(reader *bufio.Reader) (int, error) {
	contentLength := -1
	sawHeader := false

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return 0, frameReadError(err, sawHeader)
		}
		sawHeader = true
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}

		parsed, ok, err := parseContentLengthHeader(line)
		if err != nil {
			return 0, err
		}
		if ok {
			contentLength = parsed
		}
	}

	return validateContentLength(contentLength)
}

func frameReadError(err error, sawHeader bool) error {
	if errors.Is(err, io.EOF) && !sawHeader {
		return io.EOF
	}
	return err
}

func parseContentLengthHeader(line string) (int, bool, error) {
	name, value, ok := strings.Cut(line, ":")
	if !ok || !strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
		return 0, false, nil
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0, false, fmt.Errorf("invalid Content-Length: %w", err)
	}
	return parsed, true, nil
}

func validateContentLength(contentLength int) (int, error) {
	if contentLength < 0 {
		return 0, fmt.Errorf("missing Content-Length header")
	}
	if contentLength > maxFrameBytes {
		return 0, fmt.Errorf("frame exceeds %d byte limit", maxFrameBytes)
	}
	return contentLength, nil
}

func writeFrame(writer io.Writer, payload []byte) error {
	if _, err := fmt.Fprintf(writer, "Content-Length: %d\r\n\r\n", len(payload)); err != nil {
		return err
	}
	_, err := writer.Write(payload)
	return err
}
