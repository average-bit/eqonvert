package eqoa

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"io"
)

// CSFHeader represents the 40-byte CSF header structure.
type CSFHeader struct {
	Magic                 [4]byte // 0-3: "CESF"
	NumberOfBlocks        int32   // 4-7: Number of blocks (Little Endian)
	TotalCompressedSize   int64   // 8-15: Total size of compressed data (Little Endian)
	TotalDecompressedSize int64   // 16-23: Total size of decompressed data (Little Endian)
	FirstBlockOffset      int64   // 24-31: Offset to the first block info table (Little Endian, typically 40)
	MaxCompressedBlock    int32   // 32-35: Maximum size of a single compressed block (Little Endian)
	Unknown               int32   // 36-39: Unknown field (Little Endian, often 0x77f534ac)
}

const (
	CSFHeaderSize = 40
	BlockInfoSize = 8
	MagicCESF     = "CESF"
)

// BlockInfo describes the 8-byte structure preceding each compressed block's data.
type BlockInfo struct {
	CompressedBlockSize   int32 // Little Endian (Bytes 0-3)
	DecompressedBlockSize int32 // Little Endian (Bytes 4-7)
}

// DecompressCSF reads a CSF file stream and returns a reader for the decompressed ESF data.
func DecompressCSF(csfReader io.ReadSeeker) (io.Reader, *CSFHeader, error) {
	var header CSFHeader
	if err := binary.Read(csfReader, binary.LittleEndian, &header); err != nil {
		return nil, nil, fmt.Errorf("failed to read CSF header: %w", err)
	}

	if string(header.Magic[:]) != MagicCESF {
		return nil, nil, fmt.Errorf("invalid CSF magic: expected '%s', got '%s'", MagicCESF, string(header.Magic[:]))
	}

	if header.NumberOfBlocks <= 0 {
		if header.TotalDecompressedSize == 0 {
			return bytes.NewReader([]byte{}), &header, nil
		}
		return nil, nil, fmt.Errorf("invalid number of blocks: %d", header.NumberOfBlocks)
	}

	if _, err := csfReader.Seek(header.FirstBlockOffset, io.SeekStart); err != nil {
		return nil, nil, fmt.Errorf("failed to seek to first block offset 0x%X: %w", header.FirstBlockOffset, err)
	}

	esfData := new(bytes.Buffer)
	if header.TotalDecompressedSize > 0 && header.TotalDecompressedSize < (1024*1024*1024) { // 1GB limit
		esfData.Grow(int(header.TotalDecompressedSize))
	}

	for i := 0; i < int(header.NumberOfBlocks); i++ {
		var block BlockInfo
		if err := binary.Read(csfReader, binary.LittleEndian, &block); err != nil {
			return nil, nil, fmt.Errorf("block %d: failed to read block info: %w", i, err)
		}

		if block.CompressedBlockSize < 0 {
			return nil, nil, fmt.Errorf("block %d: invalid negative compressed size: %d", i, block.CompressedBlockSize)
		}

		if block.CompressedBlockSize == 0 {
			continue
		}

		compressedBytes := make([]byte, block.CompressedBlockSize)
		if _, err := io.ReadFull(csfReader, compressedBytes); err != nil {
			return nil, nil, fmt.Errorf("block %d: failed to read compressed data: %w", i, err)
		}

		zr, err := zlib.NewReader(bytes.NewReader(compressedBytes))
		if err != nil {
			return nil, nil, fmt.Errorf("block %d: failed to create zlib reader: %w", i, err)
		}

		if _, err := io.Copy(esfData, zr); err != nil {
			zr.Close()
			return nil, nil, fmt.Errorf("block %d: failed during decompression: %w", i, err)
		}
		zr.Close()
	}

	return bytes.NewReader(esfData.Bytes()), &header, nil
}
