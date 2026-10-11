package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestDeinterleaveBGMStereoSplit checks the stride split itself (no ADPCM): even
// chunks go left, odd go right, and a trailing short chunk is kept.
func TestDeinterleaveBGMStereoSplit(t *testing.T) {
	// 2.5 chunks at stride 4: L0 R0 L1(short)
	data := []byte{
		0, 0, 0, 0, // L0
		1, 1, 1, 1, // R0
		2, 2, // L1 (short tail)
	}
	l, r := deinterleaveBGMStereo(data, 4)
	if !bytes.Equal(l, []byte{0, 0, 0, 0, 2, 2}) {
		t.Errorf("left = %v", l)
	}
	if !bytes.Equal(r, []byte{1, 1, 1, 1}) {
		t.Errorf("right = %v", r)
	}
}

// TestBGMDetectStride verifies stride auto-detection against real disc data: the
// vanilla/Beta3 discs interleave at 0x4000, Frontiers at 0x10000. Skips when the
// source .BGM are not present (CI). This is the regression guard for the Frontiers
// "grainy/scrambled" artifact that a hardcoded 0x4000 stride produced.
func TestBGMDetectStride(t *testing.T) {
	home, _ := os.UserHomeDir()
	root := filepath.Join(home, "Development/elfconv")
	cases := []struct {
		label string
		path  string
		want  int
	}{
		{"Beta3 BARBARIA", filepath.Join(root, "EQOABETA3/BGM/BARBARIA.BGM"), 0x4000},
		{"Beta3 FREEPORT", filepath.Join(root, "EQOABETA3/BGM/FREEPORT.BGM"), 0x4000},
		{"Frontiers BARBARIA", filepath.Join(root, "FRONTIERSBETA/BGM/BARBARIA.BGM"), 0x10000},
		{"Frontiers FREEPORT", filepath.Join(root, "FRONTIERSBETA/BGM/FREEPORT.BGM"), 0x10000},
		{"Frontiers GNOMES", filepath.Join(root, "FRONTIERSBETA/BGM/GNOMES.BGM"), 0x10000},
	}
	ran := false
	for _, c := range cases {
		data, err := os.ReadFile(c.path)
		if err != nil {
			continue // source disc not present
		}
		ran = true
		if got := bgmDetectStride(data); got != c.want {
			t.Errorf("%s: bgmDetectStride = 0x%X, want 0x%X (corr@0x4000=%.3f corr@0x10000=%.3f)",
				c.label, got, c.want, bgmStrideCorr(data, 0x4000), bgmStrideCorr(data, 0x10000))
		}
	}
	if !ran {
		t.Skip("no source .BGM present")
	}
}
