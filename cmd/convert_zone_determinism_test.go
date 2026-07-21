package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestZoneExportDeterministic guards reproducibility: assembling the same zone
// twice must produce a byte-identical GLB. Zone output was previously
// nondeterministic because fallback surface embedding iterated a map in random
// order (ZoneAssembler.embedSurfaces), so fallback images/textures landed in
// different array slots each run — identical geometry, different bytes. This
// asserts the fix (sorted embed order) stays in place so a future stray map
// iteration can't silently reintroduce it. Real-asset-gated: skips when the disc
// zone is absent, like the collision regression test.
func TestZoneExportDeterministic(t *testing.T) {
	data, err := os.ReadFile(arenaZonePath)
	if err != nil {
		t.Skipf("real zone asset not available: %v", err)
	}

	// convertOnce assembles ARENA into a fresh temp dir and returns the visual
	// zone_0 GLB bytes (excluding the collision sidecar).
	convertOnce := func() []byte {
		dir := t.TempDir()
		if n := convertZoneESFData(data, "ARENA.ESF", dir, false, nil, nil, nil, nil); n == 0 {
			t.Fatal("convertZoneESFData produced no zones")
		}
		glbs, _ := filepath.Glob(filepath.Join(dir, "zones", "zone_0*.glb"))
		var main string
		for _, g := range glbs {
			if !strings.HasSuffix(g, "_collision.glb") {
				main = g
			}
		}
		if main == "" {
			t.Fatal("zone_0 GLB not found")
		}
		b, err := os.ReadFile(main)
		if err != nil {
			t.Fatalf("read glb: %v", err)
		}
		return b
	}

	a := convertOnce()
	b := convertOnce()
	if !bytes.Equal(a, b) {
		t.Errorf("zone export is nondeterministic: two runs produced %d vs %d bytes",
			len(a), len(b))
	}
}
