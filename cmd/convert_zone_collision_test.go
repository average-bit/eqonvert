package cmd

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/average-bit/eqonvert/pkg/eqoa"
	"github.com/average-bit/eqonvert/pkg/gltf"
)

// arenaZonePath is a real beta-disc zone ESF with animated (clockwork) props
// whose collision lives at the sprite level. The test skips when it is absent so
// CI stays green without game assets.
const arenaZonePath = "/Users/justinjanes/Development/elfconv/EQOABETADISC/DATA/ARENA.ESF"

// readGLBDoc extracts and unmarshals the JSON chunk of a binary glTF.
func readGLBDoc(t *testing.T, path string) *gltf.GLTF {
	t.Helper()
	d, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read glb %s: %v", path, err)
	}
	if len(d) < 12 || string(d[:4]) != "glTF" {
		t.Fatalf("%s is not a glb", path)
	}
	off := 12
	for off+8 <= len(d) {
		ln := int(binary.LittleEndian.Uint32(d[off:]))
		typ := string(d[off+4 : off+8])
		if off+8+ln > len(d) {
			break
		}
		body := d[off+8 : off+8+ln]
		if typ == "JSON" {
			var g gltf.GLTF
			if err := json.Unmarshal(body, &g); err != nil {
				t.Fatalf("glb json: %v", err)
			}
			return &g
		}
		off += 8 + ln
	}
	t.Fatalf("%s: no JSON chunk", path)
	return nil
}

// collisionBBox returns the world-space AABB of the {"collision":true} node's
// POSITION accessor. The collision node carries no transform, so its accessor
// min/max are already world space.
func collisionBBox(g *gltf.GLTF) (min, max [3]float32, ok bool) {
	for _, n := range g.Nodes {
		if n.Mesh == nil {
			continue
		}
		if n.Name != "collision" && (n.Extras == nil || !bytes.Contains(n.Extras, []byte("collision"))) {
			continue
		}
		for _, p := range g.Meshes[*n.Mesh].Primitives {
			ai, has := p.Attributes["POSITION"]
			if !has {
				continue
			}
			a := g.Accessors[ai]
			if len(a.Min) < 3 || len(a.Max) < 3 {
				continue
			}
			return [3]float32{a.Min[0], a.Min[1], a.Min[2]},
				[3]float32{a.Max[0], a.Max[1], a.Max[2]}, true
		}
	}
	return min, max, false
}

// TestZoneCollisionWithinVisualBounds is the regression guard for the floating-
// prop-collision bug: sprite-level CollBuffers emitted at raw definition
// coordinates (instead of the actor's world transform) leave collision hulls
// floating far outside the zone. It converts a real zone and asserts every
// collision node's world AABB stays within the zone's visual bounds (from the
// manifest) expanded by one axis-extent. Pre-fix, the ARENA clockwork collision
// floated to Y≈23 against a visual Y-max of ≈5 and would fail here.
func TestZoneCollisionWithinVisualBounds(t *testing.T) {
	data, err := os.ReadFile(arenaZonePath)
	if err != nil {
		t.Skipf("real zone asset not available: %v", err)
	}

	dir := t.TempDir()
	registry := eqoa.NewSurfaceRegistry()
	registry.PopulateFromESFData(data)
	// nil libraries: ARENA's animated props resolve from its own ZoneResources,
	// which is enough to exercise both zone-level and sprite-level collision.
	if n := convertZoneESFData(data, "ARENA.ESF", dir, false, nil, nil, nil, registry); n == 0 {
		t.Fatal("convertZoneESFData produced no zones")
	}

	manifests, _ := filepath.Glob(filepath.Join(dir, "*_zones.json"))
	if len(manifests) == 0 {
		t.Fatal("no zone manifest written")
	}
	var manifest struct {
		Zones []struct {
			GLB       string      `json:"glb"`
			Collision string      `json:"collision"`
			MinPos    *[3]float32 `json:"min_pos"`
			MaxPos    *[3]float32 `json:"max_pos"`
		} `json:"zones"`
	}
	mb, err := os.ReadFile(manifests[0])
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if err := json.Unmarshal(mb, &manifest); err != nil {
		t.Fatalf("manifest json: %v", err)
	}

	axis := []string{"East", "Height", "North"}
	checked := 0
	for _, z := range manifest.Zones {
		if z.MinPos == nil || z.MaxPos == nil || z.Collision == "" {
			continue
		}
		// Collision now lives in a sidecar GLB referenced by the manifest.
		cmin, cmax, ok := collisionBBox(readGLBDoc(t, filepath.Join(dir, z.Collision)))
		if !ok {
			continue
		}
		checked++
		vmin, vmax := *z.MinPos, *z.MaxPos
		for k := 0; k < 3; k++ {
			// Allow the collision hull to bevel beyond the rendered surface by up
			// to a full axis-extent (with a small floor for near-flat axes); a
			// prop floating at un-transformed definition coords blows past this.
			margin := vmax[k] - vmin[k]
			if margin < 5 {
				margin = 5
			}
			lo, hi := vmin[k]-margin, vmax[k]+margin
			if cmin[k] < lo || cmax[k] > hi {
				t.Errorf("zone %s: collision %s axis [%.1f,%.1f] escapes visual [%.1f,%.1f]±%.1f — likely un-transformed sprite collision floating outside the zone",
					z.GLB, axis[k], cmin[k], cmax[k], vmin[k], vmax[k], margin)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no collision nodes found to validate")
	}
}
