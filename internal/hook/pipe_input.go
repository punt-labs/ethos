package hook

import (
	"bytes"
	"errors"
	"io"
)

// PipeInput forwards one bounded hook payload without waiting for EOF.
func PipeInput(input io.Reader, output io.Writer) error {
	data, readErr := readRaw(input, InputTimeout)
	_, writeErr := io.Copy(output, bytes.NewReader(data))
	return errors.Join(readErr, writeErr)
}
