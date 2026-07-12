package eqoa

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// EQOA Xm (object 0xB030) holds music converted from FastTracker II modules
// into a runtime binary format: all text stripped, structures padded into
// fixed-size arrays (256 pattern slots, 128 instrument slots, 128 sample
// headers — unused slots hold MSVC 0xCD fill), pattern data kept in standard
// XM packing verbatim, and samples re-encoded as PS2 SPU ADPCM.
//
// XmHeader (0xB040) body layout:
//
//	+0x000 u32 dictID          +0x004 f32 rate (1.0)
//	+0x008 u32 ×2 (zero)       +0x010 u32 songLength
//	+0x014 u16 channels, patterns, instruments, flags, tempo, bpm
//	+0x020 u32 packedPatternBytes, u32 0
//	+0x028 u8  orderTable[256]
//	+0x128 PatternDir[256]:  { u8, pad3, u16 rows, u16 packedSize, u32 offset }
//	+0xD28 Instrument[128]:  224 bytes — numSamples u16, keymap[96]@+4,
//	                         volEnv[48]@+100, panEnv[48]@+148, counts[14]@+196,
//	                         fadeout u16 @+210
//	+0x7D28 SampleHeader[128]: { u32 lenBlocks, u32 loopStartBlk, u32 loopLenBlk,
//	                         u32 flags (bit0 loop, bit4 16-bit), u8 vol, u8 pan,
//	                         i8 finetune, i8 relnote, u32 adpcmOffset }
//	+0x8928 pattern blob (XM-packed, verbatim)
//
// XmSampleData (0xB060): PS2 SPU ADPCM (16-byte blocks → 28 samples).
//
// RebuildXM reverses the conversion into a playable .xm: decodes the ADPCM,
// delta-encodes the PCM, and synthesizes the stripped 60-byte text header.
// Output validated by libopenmpt (openmpt123) on all 21 beta-disc modules.

const (
	xmOrderTableOff  = 0x28
	xmPatternDirOff  = 0x128
	xmInstrumentsOff = 0xD28
	xmSampleHdrsOff  = 0x7D28
	xmPatternBlobOff = 0x8928
	xmInstrumentSize = 224
	xmSampleHdrSize  = 24
)

// RebuildXM converts an XmHeader body plus XmSampleData into a standard
// FastTracker II .xm file.  name becomes the (stripped) module name field.
func RebuildXM(hb []byte, sampleData []byte, order binary.ByteOrder, name string) ([]byte, error) {
	if len(hb) < xmPatternBlobOff {
		return nil, fmt.Errorf("XmHeader body too short: %d bytes", len(hb))
	}
	songLen := order.Uint32(hb[0x10:])
	channels := order.Uint16(hb[0x14:])
	nPat := order.Uint16(hb[0x16:])
	nIns := order.Uint16(hb[0x18:])
	flags := order.Uint16(hb[0x1A:])
	tempo := order.Uint16(hb[0x1C:])
	bpm := order.Uint16(hb[0x1E:])
	orderTable := hb[xmOrderTableOff : xmOrderTableOff+256]

	if channels == 0 || channels > 32 || nPat > 256 || nIns > 128 {
		return nil, fmt.Errorf("implausible XM header: ch=%d pat=%d ins=%d", channels, nPat, nIns)
	}

	out := new(bytes.Buffer)
	out.WriteString("Extended Module: ")
	writePadded(out, name, 20)
	out.WriteByte(0x1A)
	writePadded(out, "eqonvert convert", 20)
	binary.Write(out, binary.LittleEndian, uint16(0x0104))
	binary.Write(out, binary.LittleEndian, uint32(276)) // header size
	binary.Write(out, binary.LittleEndian, uint16(songLen))
	binary.Write(out, binary.LittleEndian, uint16(0)) // restart position
	binary.Write(out, binary.LittleEndian, channels)
	binary.Write(out, binary.LittleEndian, nPat)
	binary.Write(out, binary.LittleEndian, nIns)
	binary.Write(out, binary.LittleEndian, flags)
	binary.Write(out, binary.LittleEndian, tempo)
	binary.Write(out, binary.LittleEndian, bpm)
	out.Write(orderTable)

	// Patterns: 9-byte header + verbatim packed data from the blob.
	for i := 0; i < int(nPat); i++ {
		o := xmPatternDirOff + i*12
		rows := order.Uint16(hb[o+4:])
		psz := order.Uint16(hb[o+6:])
		off := order.Uint32(hb[o+8:])
		start := xmPatternBlobOff + int(off)
		if start+int(psz) > len(hb) {
			return nil, fmt.Errorf("pattern %d data out of range", i)
		}
		binary.Write(out, binary.LittleEndian, uint32(9))
		out.WriteByte(0) // packing type
		binary.Write(out, binary.LittleEndian, rows)
		binary.Write(out, binary.LittleEndian, psz)
		out.Write(hb[start : start+int(psz)])
	}

	// Instruments; sample slots are allocated sequentially per instrument.
	sampleBase := 0
	for ii := 0; ii < int(nIns); ii++ {
		io := xmInstrumentsOff + ii*xmInstrumentSize
		nSmp := int(order.Uint16(hb[io:]))

		iname := fmt.Sprintf("inst%d", ii)
		if nSmp == 0 {
			binary.Write(out, binary.LittleEndian, uint32(29))
			writePadded(out, iname, 22)
			out.WriteByte(0) // type
			binary.Write(out, binary.LittleEndian, uint16(0))
			continue
		}

		// Part 1 (29 bytes) + part 2 (234 bytes) = 263.
		binary.Write(out, binary.LittleEndian, uint32(263))
		writePadded(out, iname, 22)
		out.WriteByte(0) // type
		binary.Write(out, binary.LittleEndian, uint16(nSmp))
		binary.Write(out, binary.LittleEndian, uint32(40)) // sample header size
		out.Write(hb[io+4 : io+100])                       // keymap[96]
		out.Write(hb[io+100 : io+148])                     // volume envelope
		out.Write(hb[io+148 : io+196])                     // panning envelope
		// counts[14] — scrub 0xCD fill from unused slots
		for _, b := range hb[io+196 : io+210] {
			if b == 0xCD {
				b = 0
			}
			out.WriteByte(b)
		}
		fadeout := order.Uint16(hb[io+210:])
		if fadeout == 0xCDCD {
			fadeout = 0
		}
		binary.Write(out, binary.LittleEndian, fadeout)
		out.Write(make([]byte, 22)) // reserved — pads part 2 to 234

		// Sample headers + collect PCM for the data section.
		pcms := make([][]int16, nSmp)
		for si := 0; si < nSmp; si++ {
			so := xmSampleHdrsOff + (sampleBase+si)*xmSampleHdrSize
			lenBlocks := order.Uint32(hb[so:])
			loopStart := order.Uint32(hb[so+4:])
			loopLen := order.Uint32(hb[so+8:])
			sflags := order.Uint32(hb[so+12:])
			vol := hb[so+16]
			pan := hb[so+17]
			fine := int8(hb[so+18])
			rel := int8(hb[so+19])
			adpcmOff := order.Uint32(hb[so+20:])

			var pcm []int16
			if lenBlocks > 0 && int(adpcmOff)+int(lenBlocks)*16 <= len(sampleData) {
				pcm = DecodeADPCM(sampleData[adpcmOff : adpcmOff+lenBlocks*16])
			}
			pcms[si] = pcm

			nBytes := uint32(len(pcm) * 2)
			looped := sflags&1 != 0 && loopLen != 0 && loopLen != 0xFFFFFFFF
			var lS, lL uint32
			stype := byte(0x10) // 16-bit
			if looped {
				stype |= 1
				lS = min32(loopStart*28*2, nBytes)
				lL = min32(loopLen*28*2, nBytes)
			}
			if vol > 64 {
				vol = 64
			}
			binary.Write(out, binary.LittleEndian, nBytes)
			binary.Write(out, binary.LittleEndian, lS)
			binary.Write(out, binary.LittleEndian, lL)
			out.WriteByte(vol)
			out.WriteByte(byte(fine))
			out.WriteByte(stype)
			out.WriteByte(pan)
			out.WriteByte(byte(rel))
			out.WriteByte(0) // reserved
			writePadded(out, fmt.Sprintf("s%d", si), 22)
		}
		// Sample data: delta-encoded 16-bit PCM.
		for _, pcm := range pcms {
			prev := int16(0)
			for _, s := range pcm {
				binary.Write(out, binary.LittleEndian, uint16(s-prev))
				prev = s
			}
		}
		sampleBase += nSmp
	}
	return out.Bytes(), nil
}

func writePadded(b *bytes.Buffer, s string, n int) {
	if len(s) > n {
		s = s[:n]
	}
	b.WriteString(s)
	for i := len(s); i < n; i++ {
		b.WriteByte(' ')
	}
}

func min32(a, b uint32) uint32 {
	if a < b {
		return a
	}
	return b
}
