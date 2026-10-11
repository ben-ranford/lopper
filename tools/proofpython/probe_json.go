package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

func validateProbeJSON(data []byte) error {
	if err := checkJSONStrings(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := consumeProbeValue(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing probe JSON")
	}
	return nil
}

func consumeProbeValue(decoder *json.Decoder, depth int) error {
	if depth >= maxReceiptDepth {
		return errors.New("probe JSON nesting limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, nested := token.(json.Delim)
	if !nested {
		return nil
	}
	if delimiter != '{' && delimiter != '[' {
		return errors.New("unexpected probe delimiter")
	}
	return consumeProbeMembers(decoder, delimiter, depth)
}

func consumeProbeMembers(decoder *json.Decoder, delimiter json.Delim, depth int) error {
	seen := map[string]bool{}
	count := 0
	for decoder.More() {
		if count >= maxFields {
			return errors.New("probe JSON field limit")
		}
		count++
		if delimiter == '{' {
			if err := consumeProbeKey(decoder, seen); err != nil {
				return err
			}
		}
		if err := consumeProbeValue(decoder, depth+1); err != nil {
			return err
		}
	}
	end, err := decoder.Token()
	if err != nil {
		return err
	}
	expected := json.Delim(']')
	if delimiter == '{' {
		expected = '}'
	}
	if end != expected {
		return errors.New("incomplete probe JSON")
	}
	return nil
}

func consumeProbeKey(decoder *json.Decoder, seen map[string]bool) error {
	key, err := decoder.Token()
	if err != nil {
		return err
	}
	name, ok := key.(string)
	if !ok || seen[name] {
		return errors.New("duplicate or invalid probe key")
	}
	seen[name] = true
	return nil
}
