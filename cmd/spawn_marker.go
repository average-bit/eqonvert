package cmd

import (
	_ "embed"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"

	"github.com/average-bit/eqonvert/pkg/eqoa"
	"github.com/average-bit/eqonvert/pkg/gltf"
)

// spawnMarkerGLB is the designed spawn-marker model (a downward "location pin"),
// baked into the binary. Regenerate with the one-off generator if the shape
// changes; the .glb is committed (see .gitignore exception).
//
//go:embed assets/spawn_marker.glb
var spawnMarkerGLB []byte

var (
	cachedMarker      *eqoa.Asset
	cachedMarkerColor = [3]float32{1, 0, 1}
)

// spawnMarkerAsset returns the embedded designed marker as an in-memory
// eqoa.Asset (the zone assembler can place it like any sprite) plus its marker
// colour, parsed once from the go:embed'd .glb. Falls back to a procedural
// octahedron if the embedded asset can't be read.
func spawnMarkerAsset() (*eqoa.Asset, [3]float32) {
	if cachedMarker == nil {
		if a, col, err := loadMarkerGLB(spawnMarkerGLB); err == nil && a != nil {
			cachedMarker, cachedMarkerColor = a, col
		} else {
			cachedMarker = fallbackMarker()
		}
	}
	return cachedMarker, cachedMarkerColor
}

// loadMarkerGLB parses a single-mesh GLB into an eqoa.Asset (POSITION + NORMAL +
// indices) and returns its marker colour (emissive, else base colour).
func loadMarkerGLB(data []byte) (*eqoa.Asset, [3]float32, error) {
	col := [3]float32{1, 0, 1}
	if len(data) < 12 || string(data[0:4]) != "glTF" {
		return nil, col, fmt.Errorf("not a glb")
	}
	var jsonBytes, binBytes []byte
	for off := 12; off+8 <= len(data); {
		clen := int(binary.LittleEndian.Uint32(data[off:]))
		ctype := string(data[off+4 : off+8])
		if off+8+clen > len(data) {
			break
		}
		body := data[off+8 : off+8+clen]
		switch ctype {
		case "JSON":
			jsonBytes = body
		case "BIN\x00":
			binBytes = body
		}
		off += 8 + clen
	}
	var doc gltf.GLTF
	if err := json.Unmarshal(jsonBytes, &doc); err != nil {
		return nil, col, err
	}
	if len(doc.Meshes) == 0 || len(doc.Meshes[0].Primitives) == 0 {
		return nil, col, fmt.Errorf("no mesh in marker glb")
	}
	prim := doc.Meshes[0].Primitives[0]

	readVec3 := func(accIdx int) [][3]float32 {
		a := doc.Accessors[accIdx]
		bv := doc.BufferViews[a.BufferView]
		base := bv.ByteOffset + a.ByteOffset
		out := make([][3]float32, a.Count)
		for i := 0; i < a.Count; i++ {
			o := base + i*12
			out[i] = [3]float32{
				math.Float32frombits(binary.LittleEndian.Uint32(binBytes[o:])),
				math.Float32frombits(binary.LittleEndian.Uint32(binBytes[o+4:])),
				math.Float32frombits(binary.LittleEndian.Uint32(binBytes[o+8:])),
			}
		}
		return out
	}

	posIdx, ok := prim.Attributes["POSITION"]
	if !ok {
		return nil, col, fmt.Errorf("marker glb has no POSITION")
	}
	positions := readVec3(posIdx)
	var normals [][3]float32
	if ni, ok := prim.Attributes["NORMAL"]; ok {
		normals = readVec3(ni)
	}

	var indices []uint32
	if prim.Indices != nil {
		a := doc.Accessors[*prim.Indices]
		bv := doc.BufferViews[a.BufferView]
		base := bv.ByteOffset + a.ByteOffset
		for i := 0; i < a.Count; i++ {
			switch a.ComponentType {
			case 5125: // uint32
				indices = append(indices, binary.LittleEndian.Uint32(binBytes[base+i*4:]))
			case 5123: // uint16
				indices = append(indices, uint32(binary.LittleEndian.Uint16(binBytes[base+i*2:])))
			}
		}
	}

	if prim.Material != nil && *prim.Material < len(doc.Materials) {
		m := doc.Materials[*prim.Material]
		if len(m.EmissiveFactor) >= 3 {
			col = [3]float32{m.EmissiveFactor[0], m.EmissiveFactor[1], m.EmissiveFactor[2]}
		} else if m.PBRMetallicRoughness != nil && len(m.PBRMetallicRoughness.BaseColorFactor) >= 3 {
			bc := m.PBRMetallicRoughness.BaseColorFactor
			col = [3]float32{bc[0], bc[1], bc[2]}
		}
	}

	fg := eqoa.FaceGroup{Indices: indices}
	for i, p := range positions {
		v := eqoa.Vertex{Pos: p}
		if i < len(normals) {
			v.Normal = normals[i]
		}
		fg.Vertices = append(fg.Vertices, v)
	}
	return &eqoa.Asset{Meshes: []*eqoa.Mesh{{FaceGroups: []eqoa.FaceGroup{fg}}}}, col, nil
}

// fallbackMarker builds a minimal procedural octahedron if the embedded .glb
// can't be parsed — so spawn marking never crashes.
func fallbackMarker() *eqoa.Asset {
	const h, r = 3.0, 1.5
	top := [3]float32{0, h, 0}
	bot := [3]float32{0, -h, 0}
	px := [3]float32{r, 0, 0}
	nx := [3]float32{-r, 0, 0}
	pz := [3]float32{0, 0, r}
	nz := [3]float32{0, 0, -r}
	tris := [8][3][3]float32{
		{top, px, pz}, {top, pz, nx}, {top, nx, nz}, {top, nz, px},
		{bot, pz, px}, {bot, nx, pz}, {bot, nz, nx}, {bot, px, nz},
	}
	var fg eqoa.FaceGroup
	for _, t := range tris {
		n := triNormal(t[0], t[1], t[2])
		base := uint32(len(fg.Vertices))
		for _, p := range t {
			fg.Vertices = append(fg.Vertices, eqoa.Vertex{Pos: p, Normal: n})
		}
		fg.Indices = append(fg.Indices, base, base+1, base+2)
	}
	return &eqoa.Asset{Meshes: []*eqoa.Mesh{{FaceGroups: []eqoa.FaceGroup{fg}}}}
}

// triNormal returns the unit normal of triangle (a,b,c).
func triNormal(a, b, c [3]float32) [3]float32 {
	u := [3]float32{b[0] - a[0], b[1] - a[1], b[2] - a[2]}
	v := [3]float32{c[0] - a[0], c[1] - a[1], c[2] - a[2]}
	n := [3]float32{u[1]*v[2] - u[2]*v[1], u[2]*v[0] - u[0]*v[2], u[0]*v[1] - u[1]*v[0]}
	l := n[0]*n[0] + n[1]*n[1] + n[2]*n[2]
	if l <= 0 {
		return [3]float32{0, 1, 0}
	}
	inv := 1.0 / float32(math.Sqrt(float64(l)))
	return [3]float32{n[0] * inv, n[1] * inv, n[2] * inv}
}
