package jsoncheck

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

var ErrInvalidObject = errors.New("invalid bounded JSON object")

type Limits struct {
	MaxBytes int
	MaxDepth int
}

func ValidateObject(raw json.RawMessage, limits Limits) error {
	if limits.MaxBytes < 2 || limits.MaxDepth < 1 || len(raw) > limits.MaxBytes {
		return ErrInvalidObject
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	first, err := decoder.Token()
	if err != nil {
		return ErrInvalidObject
	}
	root, ok := first.(json.Delim)
	if !ok || root != '{' {
		return ErrInvalidObject
	}
	depth := 1
	for depth > 0 {
		token, err := decoder.Token()
		if err != nil {
			return ErrInvalidObject
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			continue
		}
		switch delimiter {
		case '{', '[':
			depth++
			if depth > limits.MaxDepth {
				return ErrInvalidObject
			}
		case '}', ']':
			depth--
		}
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return ErrInvalidObject
	}
	return nil
}
