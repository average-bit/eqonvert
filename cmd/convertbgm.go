package cmd

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/average-bit/eqonvert/pkg/eqoa"
)

// .BGM files (BGM/, BGM/VO1, BGM/VO2, MUSIC/MUSIC0, MUSIC/MUSIC1) are
// headerless STEREO PS2 VAG-ADPCM streams, block-interleaved in 0x4000-byte
// chunks: [16KB left][16KB right][16KB left]…  Each channel is decoded with
// its OWN predictor history (matching the VAG reference decoder's stereo
// path). Decoding the file as one continuous mono stream instead plays every
// ~0.65s of music twice (L then R) — an audible constant echo, and double the
// true track length. Verified by stride sweep (L/R correlation peaks sharply
// at 0x4000 across BGM files) and by ear.
//
// Sample rate is not stored in the file; 44100 Hz is assumed.  If pitch sounds
// off by a fixed factor, this constant is the knob.
const (
	bgmSampleRate = 44100
	bgmInterleave = 0x4000  // stereo channel-chunk stride in bytes
	bgmBankAlign  = 0x10000 // streamed .BGM preload-bank size in bytes
)

func isBGMExt(path string) bool {
	return strings.ToUpper(filepath.Ext(path)) == ".BGM"
}

// deinterleaveBGMStereo splits a 0x4000-block-interleaved stereo VAG stream into
// its left and right ADPCM byte streams (even chunks → left, odd → right).
func deinterleaveBGMStereo(data []byte) (left, right []byte) {
	for start, ci := 0, 0; start < len(data); start, ci = start+bgmInterleave, ci+1 {
		end := start + bgmInterleave
		if end > len(data) {
			end = len(data)
		}
		if ci%2 == 0 {
			left = append(left, data[start:end]...)
		} else {
			right = append(right, data[start:end]...)
		}
	}
	return left, right
}

// bgmBlockSilent reports whether a 16-byte VAG-ADPCM block carries no sample
// data (bytes 2..15 all zero) — the init / silent-lead-in blocks that separate
// the concatenated VAG banks in a streamed .BGM.
func bgmBlockSilent(block []byte) bool {
	for _, b := range block[2:16] {
		if b != 0 {
			return false
		}
	}
	return true
}

// decodeBGMMono decodes a mono .BGM stream, discarding the streamed preload
// bank at the front. Streamed .BGM files begin with a fixed 0x10000-byte
// "preload" bank — a redundant copy of the track's opening that the engine
// plays instantly while the full stream buffers — followed by an init block, a
// short silent lead-in, and then the COMPLETE track. Decoding the file linearly
// therefore plays the first ~2.6s and then restarts the whole song from the
// beginning: the "~2s disjointed clips / replay" heard in every track.
//
// We detect the preload boundary (an all-zero init block landing exactly on a
// 0x10000-byte boundary), drop everything up to and including its silent
// lead-in, and decode only the full track that follows. Verified across all 45
// FRONTIERSBETA MUSIC tracks: the boundary is always at 0x10000, the trailing
// bank is full-length, and its opening cross-correlates with the preload
// (aligned at offset ~0). Files without this structure decode unchanged.
func decodeBGMMono(data []byte) []int16 {
	n := len(data) / 16
	start := 0
	for b := 1; b < n; b++ {
		if (b*16)%bgmBankAlign != 0 || !bgmBlockSilent(data[b*16:b*16+16]) {
			continue
		}
		// Only treat the leading bank as a discardable preload if the data that
		// follows is larger than it — the real track dwarfs the 64KB preload. A
		// shorter tail means this is a coincidental mid-track silence landing on
		// a bank boundary, not a preload copy, so decode the whole stream intact.
		if len(data)-b*16 <= b*16 {
			break
		}
		// Preload boundary: skip the init block + its silent lead-in, then
		// decode the complete track that follows.
		j := b
		for j < n && bgmBlockSilent(data[j*16:j*16+16]) {
			j++
		}
		start = j
		break
	}
	return eqoa.DecodeADPCM(data[start*16:])
}

// convertBGMData decodes a raw .BGM stream to stereo FLAC and also writes the
// original ADPCM verbatim as a sibling .vag — the lossless disc-format export
// that preserves the exact interleaved bytes (a stereo-aware VAG tool can
// re-decode it; FLAC bakes the channels).  outPath is the .flac path; the .vag
// is written next to it.
func convertBGMData(data []byte, outPath string) error {
	// Original ADPCM → .vag (VAGp header + verbatim disc bytes).
	vagPath := strings.TrimSuffix(outPath, filepath.Ext(outPath)) + ".vag"
	vag := append(eqoa.VAGHeader(bgmSampleRate, uint32(len(data)), filepath.Base(vagPath)), data...)
	os.WriteFile(vagPath, vag, 0644)

	// Not every .BGM is stereo: some builds (e.g. FRONTIERSBETA music) store
	// continuous MONO, while others (EQOABETA3 music) and all voice-over files
	// are 0x4000-interleaved stereo. Blindly de-interleaving a mono stream
	// splits it into two half-chunks played back-to-back — an audible echo.
	// Detect the stereo signature per file (L/R correlation at 0x4000) and pick
	// the matching decode.
	left, right := deinterleaveBGMStereo(data)
	if bgmChannelsCorrelated(left, right) {
		lpcm := eqoa.DecodeADPCM(left)
		rpcm := eqoa.DecodeADPCM(right)
		n := len(lpcm)
		if len(rpcm) < n {
			n = len(rpcm)
		}
		if n > 0 {
			return writeFLACChannels(outPath, [][]int16{lpcm[:n], rpcm[:n]}, bgmSampleRate)
		}
	}
	// Mono: decode the full track, discarding the streamed preload bank that
	// otherwise makes the song restart ~2.6s in (see decodeBGMMono).
	pcm := decodeBGMMono(data)
	if len(pcm) == 0 {
		return fmt.Errorf("no decodable ADPCM data")
	}
	return writeFLAC(outPath, pcm, bgmSampleRate)
}

// bgmChannelsCorrelated reports whether the de-interleaved left/right ADPCM
// streams decode to correlated audio — the signature of genuine 0x4000-stereo
// (real stereo r≈0.35–0.6, dual-mono voice r≈1.0) versus a mono stream wrongly
// split (r≈0). Uses a bounded prefix for speed. Threshold 0.25 sits in the wide
// measured gap between mono (≤0.06) and stereo (≥0.37).
func bgmChannelsCorrelated(left, right []byte) bool {
	const probe = 1 << 20 // ~1MB per channel is plenty to classify
	lb, rb := left, right
	if len(lb) > probe {
		lb = lb[:probe]
	}
	if len(rb) > probe {
		rb = rb[:probe]
	}
	l := eqoa.DecodeADPCM(lb)
	r := eqoa.DecodeADPCM(rb)
	n := len(l)
	if len(r) < n {
		n = len(r)
	}
	if n < 20000 {
		return false
	}
	var sl, sr, sll, srr, slr float64
	for i := 0; i < n; i++ {
		x, y := float64(l[i]), float64(r[i])
		sl += x
		sr += y
		sll += x * x
		srr += y * y
		slr += x * y
	}
	fn := float64(n)
	cov := slr/fn - (sl/fn)*(sr/fn)
	vl := sll/fn - (sl/fn)*(sl/fn)
	vr := srr/fn - (sr/fn)*(sr/fn)
	if vl < 1 || vr < 1 {
		return false // silence / degenerate → treat as mono
	}
	return cov/(math.Sqrt(vl)*math.Sqrt(vr)) > 0.25
}
