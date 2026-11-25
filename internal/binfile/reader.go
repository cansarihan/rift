package binfile

import (
	"bytes"
	"io"
)

func readerAt(data []byte) io.ReaderAt {
	return bytes.NewReader(data)
}
