// Package strictjson decodes one JSON value and rejects schema drift.
package strictjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
)

func Decode(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON content")
	}
	return nil
}

// maxDecodeFileBytes bounds DecodeFile the same way every one of this
// repo's own callers independently bounded their own file reads before
// this helper existed - fail-closed against an unbounded read, not a
// claim that 1 MiB is meaningful for every possible caller.
const maxDecodeFileBytes = 1 << 20

// DecodeFile applies Decode to at most 1 MiB read from path - the
// canonical "read a small, trusted-format JSON file strictly" helper
// used throughout this repo's CLI and application layers.
func DecodeFile(path string, target any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxDecodeFileBytes))
	if err != nil {
		return err
	}
	return Decode(data, target)
}
