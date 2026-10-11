package cmd

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/average-bit/eqonvert/pkg/eqoa"
)

// .BGM files (BGM/, BGM/VO1, BGM/VO2, MUSIC/MUSIC0, MUSIC/MUSIC1) are headerless
// STEREO PS2 VAG-ADPCM streams, block-interleaved: a run of left-channel bytes,
// then the same-length run of right-channel bytes, repeating. Each channel is
// decoded with its OWN predictor history (matching the VAG reference decoder's
// stereo path).
//
// ALL .BGM are stereo — do NOT try to classify mono vs stereo (the client's
// sound engine, RE'd in the Beta3 CLIENT, has a single VISoundDevice BGM
// streaming path with no per-file branch, and PCSX2 ground-truth captures of the
// char-select themes confirm stereo). What DOES vary is the INTERLEAVE STRIDE:
// the original/vanilla and Beta3 discs interleave in 0x4000-byte chunks, but the
// Frontiers disc uses 0x10000. Using the wrong stride de-interleaves the right
// LENGTH but SCRAMBLED chunk order — grainy clicks throughout and L/R that
// correlate ~0 — which earlier looked like "decorrelated stereo" or (when the
// stream was then misread as one mono run) a "~2.6s replay" (0x10000 ≈ 2.6s) and
// a doubled length. So auto-detect the stride per file: the correct one is the
// one whose de-interleaved L/R channels actually correlate (bgmDetectStride).
//
// Sample rate is not stored in the file; 44100 Hz is assumed.  If pitch sounds
// off by a fixed factor, this constant is the knob.
const bgmSampleRate = 44100

// bgmCandidateStrides are the interleave strides seen on disc, tried largest
// correlation wins. 0x4000 = vanilla/Beta3, 0x10000 = Frontiers; the others
// bracket them so an unseen build still resolves rather than silently scrambling.
var bgmCandidateStrides = []int{0x2000, 0x4000, 0x8000, 0x10000, 0x20000}

func isBGMExt(path string) bool {
	return strings.ToUpper(filepath.Ext(path)) == ".BGM"
}

// deinterleaveBGMStereo splits a stride-block-interleaved stereo VAG stream into
// its left and right ADPCM byte streams (even chunks → left, odd → right).
func deinterleaveBGMStereo(data []byte, stride int) (left, right []byte) {
	for start, ci := 0, 0; start < len(data); start, ci = start+stride, ci+1 {
		end := start + stride
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

// bgmStrideCorr de-interleaves at stride, decodes a bounded prefix of each
// channel, and returns their Pearson correlation. The correct stride yields real
// stereo correlation (measured 0.34–0.75 across discs); a wrong stride scrambles
// the chunks and lands near 0. Returns -2 when there is too little data to judge.
func bgmStrideCorr(data []byte, stride int) float64 {
	left, right := deinterleaveBGMStereo(data, stride)
	const probe = 1 << 20 // ~1MB/channel is plenty to classify
	if len(left) > probe {
		left = left[:probe]
	}
	if len(right) > probe {
		right = right[:probe]
	}
	l := eqoa.DecodeADPCM(left)
	r := eqoa.DecodeADPCM(right)
	n := len(l)
	if len(r) < n {
		n = len(r)
	}
	if n < 20000 {
		return -2
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
		return -2 // silence / degenerate
	}
	return cov / (math.Sqrt(vl) * math.Sqrt(vr))
}

// bgmDetectStride returns the interleave stride whose de-interleaved L/R channels
// correlate best — the stride the stream was actually encoded with. Falls back to
// 0x4000 (the most common) when nothing can be measured (e.g. a tiny file).
func bgmDetectStride(data []byte) int {
	best, bestCorr := 0x4000, -1.0
	for _, s := range bgmCandidateStrides {
		if c := bgmStrideCorr(data, s); c > bestCorr {
			bestCorr, best = c, s
		}
	}
	return best
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

	// Auto-detect the interleave stride (0x4000 vanilla/Beta3, 0x10000 Frontiers),
	// then de-interleave and decode each channel with its own predictor history.
	stride := bgmDetectStride(data)
	left, right := deinterleaveBGMStereo(data, stride)
	lpcm := eqoa.DecodeADPCM(left)
	rpcm := eqoa.DecodeADPCM(right)
	n := len(lpcm)
	if len(rpcm) < n {
		n = len(rpcm)
	}
	if n == 0 {
		return fmt.Errorf("no decodable ADPCM data")
	}
	return writeFLACChannels(outPath, [][]int16{lpcm[:n], rpcm[:n]}, bgmSampleRate)
}
