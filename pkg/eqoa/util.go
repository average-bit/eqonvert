package eqoa

import (
	"bytes"
	"compress/zlib"
	"io"
)

// HasZlibHeader checks if the data starts with a common zlib header (0x78 0xDA, 0x78 0x9C, etc.).
func HasZlibHeader(data []byte) bool {
	if len(data) < 2 {
		return false
	}
	// 0x78 0x01 - No Compression
	// 0x78 0x5E - Fixed Compression
	// 0x78 0x9C - Default Compression
	// 0x78 0xDA - Best Compression
	return data[0] == 0x78 && (data[1] == 0x01 || data[1] == 0x5E || data[1] == 0x9C || data[1] == 0xDA)
}

// DecompressZlib decompresses a zlib-encoded body.
func DecompressZlib(data []byte) ([]byte, error) {
	zr, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return io.ReadAll(zr)
}
