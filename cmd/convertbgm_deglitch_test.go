package cmd

import (
	"os"
	"testing"

	"github.com/average-bit/eqonvert/pkg/eqoa"
)

// maxJumpToZero scans PCM for the largest sample-to-sample step that lands on
// (or near) zero within a window — the signature of the hard-zero click at a
// bank seam. Returns the jump magnitude and its sample index.
func maxJumpToZero(pcm []int16, lo, hi int) (int, int) {
	if hi > len(pcm) {
		hi = len(pcm)
	}
	best, at := 0, -1
	for i := lo; i < hi-1; i++ {
		if pcm[i+1] == 0 {
			d := int(pcm[i])
			if d < 0 {
				d = -d
			}
			if d > best {
				best, at = d, i
			}
		}
	}
	return best, at
}

// longestZeroRun returns the longest run of exact-zero samples in [lo,hi).
func longestZeroRun(pcm []int16, lo, hi int) int {
	if hi > len(pcm) {
		hi = len(pcm)
	}
	best, cur := 0, 0
	for i := lo; i < hi; i++ {
		if pcm[i] == 0 {
			cur++
			if cur > best {
				best = cur
			}
		} else {
			cur = 0
		}
	}
	return best
}

// TestDecodeBGMMonoDeglitch verifies decodeBGMMono removes the hard-zero click
// and the ~100ms dead-air gap present at the 0x10000 preload-bank seam
// (~2.601s) that a plain linear decode reproduces.
func TestDecodeBGMMonoDeglitch(t *testing.T) {
	const vagPath = "../_dev/output/frontiersbeta_v0.3.0/MUSIC/MUSIC0/COMBAT_1.vag"
	raw, err := os.ReadFile(vagPath)
	if err != nil {
		t.Skipf("source .vag not present (%v)", err)
	}
	if len(raw) <= 48 {
		t.Fatalf(".vag too small: %d bytes", len(raw))
	}
	data := raw[48:] // strip VAGp header → verbatim ADPCM

	// Preload seam is at byte 0x10000 = block 4096 = sample 114688 (~2.601s).
	const seam = 4096 * 28

	// Premise: the raw linear decode carries the preload seam — a hard-zero
	// click plus a dead-air gap where the song restarts.
	raw2 := eqoa.DecodeADPCM(data)
	rawJump, _ := maxJumpToZero(raw2, seam-4000, seam+8000)
	rawGap := longestZeroRun(raw2, seam-4000, seam+8000)
	if rawJump < 2000 || rawGap < 1000 {
		t.Fatalf("expected raw decode to show the preload seam (jump=%d gap=%d) — test premise broken", rawJump, rawGap)
	}

	// decodeBGMMono discards the ~2.6s preload bank (plus its silent lead-in),
	// so the output is the full track alone: shorter by roughly the preload,
	// and free of the seam click/gap near its front.
	fixed := decodeBGMMono(data)
	dropped := len(raw2) - len(fixed)
	fixJump, at := maxJumpToZero(fixed, 0, 4*44100)
	fixGap := longestZeroRun(fixed, 0, 4*44100)
	t.Logf("raw: jump=%d gap=%d len=%d ; fixed: jump=%d(@%d) gap=%d len=%d ; dropped=%d samples (%.2fs)",
		rawJump, rawGap, len(raw2), fixJump, at, fixGap, len(fixed), dropped, float64(dropped)/44100)

	// Dropped amount ≈ preload bank (2.601s) + short silent lead-in.
	if dropped < int(2.4*44100) || dropped > int(3.6*44100) {
		t.Errorf("dropped preload span out of expected range: %d samples (%.2fs)", dropped, float64(dropped)/44100)
	}
	// No hard-zero seam click near the start of the de-preloaded track.
	if fixJump > 3000 {
		t.Errorf("unexpected hard-zero click near start of fixed track: jump=%d", fixJump)
	}
	if fixGap > 4000 { // ~90ms — a bank-lead-in gap would exceed this
		t.Errorf("unexpected dead-air gap near start of fixed track: %d zero samples", fixGap)
	}
}
