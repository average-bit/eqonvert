package eqoa

import (
	"encoding/binary"
	"fmt"
)

// AdpcmHeader represents the internal EQOA ADPCM header (0xb010).
type AdpcmHeader struct {
	DictID     uint32 // 0-3
	Unknown1   uint32 // 4-7
	BlockCount uint32 // 8-11 (DataSize = BlockCount * 16)
	SampleRate uint32 // 12-15
	Unknown2   uint32 // 16-19
	Unknown3   uint32 // 20-23
	Unknown4   uint32 // 24-27
}

// ParseAdpcmHeader parses the 28-byte AdpcmHeader object body.
func ParseAdpcmHeader(data []byte, order binary.ByteOrder) (*AdpcmHeader, error) {
	if len(data) < 28 {
		return nil, fmt.Errorf("adpcm header too short: %d bytes", len(data))
	}

	header := &AdpcmHeader{
		DictID:     order.Uint32(data[0:4]),
		Unknown1:   order.Uint32(data[4:8]),
		BlockCount: order.Uint32(data[8:12]),
		SampleRate: order.Uint32(data[12:16]),
		Unknown2:   order.Uint32(data[16:20]),
		Unknown3:   order.Uint32(data[20:24]),
		Unknown4:   order.Uint32(data[24:28]),
	}

	return header, nil
}

// vagCoef are the fixed prediction coefficients of the PS2 SPU2 ADPCM codec
// (identical to PS1 SPU ADPCM), in 1/64 units.
var vagCoef = [5][2]int32{
	{0, 0},
	{60, 0},
	{115, -52},
	{98, -55},
	{122, -60},
}

// DecodeADPCM decodes PS2 VAG-format ADPCM sample data to 16-bit PCM.
// Data is a sequence of 16-byte blocks:
//
//	byte 0: predictor (high nibble) | shift (low nibble)
//	byte 1: flags (0x07 = end marker, no audio in this block)
//	bytes 2..15: 28 4-bit samples, low nibble first
func DecodeADPCM(data []byte) []int16 {
	pcm := make([]int16, 0, (len(data)/16)*28)
	var hist1, hist2 int32

	for off := 0; off+16 <= len(data); off += 16 {
		pred := int(data[off] >> 4)
		shift := uint(data[off] & 0x0F)
		flags := data[off+1]
		if flags == 0x07 {
			break
		}
		if pred > 4 {
			pred = 0
		}
		c0, c1 := vagCoef[pred][0], vagCoef[pred][1]

		for i := 0; i < 14; i++ {
			b := data[off+2+i]
			for _, nibble := range [2]uint8{b & 0x0F, b >> 4} {
				// Sign-extend the nibble into the top of a 16-bit value,
				// then shift down into range.
				s := int32(int16(uint16(nibble)<<12)) >> shift
				s += (hist1*c0 + hist2*c1) >> 6
				if s > 32767 {
					s = 32767
				} else if s < -32768 {
					s = -32768
				}
				hist2 = hist1
				hist1 = s
				pcm = append(pcm, int16(s))
			}
		}
	}
	return pcm
}

// VAGHeader builds a 48-byte big-endian PS2 "VAGp" header for a raw ADPCM
// stream of dataSize bytes at sampleRate.  Appending the original ADPCM after
// this header yields a standard .vag file that preserves the exact disc bytes
// — including the loop flags encoded in the ADPCM blocks (0x06 loop-start,
// 0x03 body, 0x07 end) — and plays in vgmstream, foobar2000, VLC, etc.  This
// is the lossless "original format" export that sits alongside the FLAC.
func VAGHeader(sampleRate, dataSize uint32, name string) []byte {
	vag := make([]byte, 48)
	copy(vag[0:4], "VAGp")
	binary.BigEndian.PutUint32(vag[4:8], 0x00000003) // version (PS2)
	binary.BigEndian.PutUint32(vag[12:16], dataSize)
	binary.BigEndian.PutUint32(vag[16:20], sampleRate)
	if len(name) > 15 {
		name = name[:15]
	}
	copy(vag[32:47], name)
	return vag
}

// GenerateVAGHeader creates a 48-byte Big Endian VAG header from an
// AdpcmHeader (real sample rate + block count from the 0xB010 object).
func (h *AdpcmHeader) GenerateVAGHeader(name string) []byte {
	return VAGHeader(h.SampleRate, h.BlockCount*16, name)
}
