package cmd

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/average-bit/eqonvert/pkg/eqoa"
	"github.com/average-bit/eqonvert/pkg/gltf"
)

// Particle definitions (0xC000: 0xC010 dictID + 0xC020 parameter blob) and
// spell effects (0xC200: 0xC210 dictID + 0xC220 parameter blob) are runtime
// emitter/effect parameter structures.  Their full semantics are not yet
// reverse engineered, so convert exports them faithfully as JSON: identity,
// version, the raw payload (base64), and — the navigable part — every
// cross-reference found in the payload (spell effects reference particle
// definitions; particle definitions reference textures).  This makes the
// effect graph explorable and preserves everything for future decoding.

type particleEntry struct {
	Type    string   `json:"type"` // "particle" | "spell_effect"
	ID      string   `json:"id"`
	Version int      `json:"version"`
	Size    int      `json:"size"`
	Refs    []string `json:"refs,omitempty"` // dictIDs of known objects found in the payload
	// Structured decode (particle defs).  Layout confirmed by Ghidra
	// decompile of the 0xC020 parser FUN_00412cd8 — byte-exact against
	// every observed blob size (24-byte header + layers of 676 bytes,
	// extra layers prefixed by a 32-byte name):
	//   u32 texture, u32 blendMode (0-4, configures the raster blend —
	//   FUN_0042b360), u32 flagB, u32 flagC, u32 intD, u32 extraLayers,
	//   then per layer: f32[13], vec4 A, vec4 B, vec4 ramp[32]
	//   (over-lifetime keyframe table), u32, vec3[6], u32.
	// Float/vector semantic names pending the emitter-update decompile;
	// raw payload preserved in data_base64.
	Texture    string          `json:"texture,omitempty"`
	BlendMode  *int            `json:"blend_mode,omitempty"`
	LayerCount int             `json:"layer_count,omitempty"`
	Layers     []particleLayer `json:"layers,omitempty"`
	Data       string          `json:"data_base64"`
}

// particleLayer per the parser read order.  Semantics established by the
// engine's bounds estimator (FUN_0042cb00 — explicit base+|variance| pair
// math) and statistics over all 506 layers on the beta disc:
//
//	floats[1]          emission rate per second (median 202, range 0–1000)
//	floats[3], [4]     particle size base ± variance (world units, 0–2)
//	floats[5], [6]     lifetime base ± variance (seconds)
//	floats[0,2,7..12]  distributions documented in FORMATS.md; exact roles
//	                   pending the spawn-function decompile
//	ramp[32]           RGBA over-lifetime keyframe curves (columns=R,G,B,A,
//	                   each spanning 0..1 — the effect's color gradient)
//	vectors[0]         emission direction (unit-ish)
//	vectors[1]         spread / rotation range
//	vectors[2], [3]    positional offsets (rarely used)
//	vectors[4]         unit axis — gravity/orientation (505/506 unit vectors)
//	vectors[5]         uniform scalar triple — drag or gravity magnitude
//	vec4A/vec4B        almost always zero (4 nonzero cases on the disc)
type particleLayer struct {
	Name    string        `json:"name,omitempty"`
	Floats  []float32     `json:"floats"`
	Vec4A   [4]float32    `json:"vec4_a"`
	Vec4B   [4]float32    `json:"vec4_b"`
	Ramp    [][4]float32  `json:"ramp"`
	FloatA  float32       `json:"float_a"`
	Vectors [6][3]float32 `json:"vectors"` // emission volume / velocity / gravity vec3s
	IntB    uint32        `json:"int_b"`
}

// decodeParticleLayers parses the confirmed 0xC020 layout.
func decodeParticleLayers(blob []byte, order binary.ByteOrder) (blend int, layers []particleLayer) {
	f32 := func(off int) float32 {
		return math.Float32frombits(order.Uint32(blob[off:]))
	}
	readLayer := func(off int, name string) (particleLayer, int) {
		var l particleLayer
		l.Name = name
		for i := 0; i < 13; i++ {
			l.Floats = append(l.Floats, f32(off+i*4))
		}
		off += 52
		for i := 0; i < 4; i++ {
			l.Vec4A[i] = f32(off + i*4)
			l.Vec4B[i] = f32(off + 16 + i*4)
		}
		off += 32
		for k := 0; k < 32; k++ {
			var v [4]float32
			for i := 0; i < 4; i++ {
				v[i] = f32(off + k*16 + i*4)
			}
			l.Ramp = append(l.Ramp, v)
		}
		off += 512
		l.FloatA = f32(off)
		off += 4
		for k := 0; k < 6; k++ {
			for i := 0; i < 3; i++ {
				l.Vectors[k][i] = f32(off + k*12 + i*4)
			}
		}
		off += 72
		l.IntB = order.Uint32(blob[off:])
		off += 4
		return l, off
	}

	blend = int(order.Uint32(blob[4:]))
	extra := int(order.Uint32(blob[20:]))
	off := 24
	if off+676 > len(blob) {
		return blend, nil
	}
	l, off := readLayer(off, "")
	layers = append(layers, l)
	for k := 0; k < extra && off+708 <= len(blob); k++ {
		raw := blob[off : off+32]
		name := string(raw)
		if i := strings.IndexByte(name, 0); i >= 0 {
			name = name[:i] // NUL-terminated; the rest is uninitialized fill
		}
		l, off = readLayer(off+32, name)
		layers = append(layers, l)
	}
	return blend, layers
}

// writeParticleFX exports all particle definitions and spell effects in the
// object tree to PREFIX_particlefx.json.
func writeParticleFX(r io.ReadSeeker, objects []*eqoa.ESFObject, order binary.ByteOrder, prefix string, outDir string, verbose bool) {
	// First pass: collect every dictID in the file so payload cross-references
	// can be recognized (textures, particle defs, sounds...).
	known := map[uint32]bool{}
	var collectIDs func(o *eqoa.ESFObject)
	collectIDs = func(o *eqoa.ESFObject) {
		// Real dictIDs are name hashes — small values are counters/indices
		// that would produce false cross-reference matches.
		if o.DictID >= 0x10000 {
			known[o.DictID] = true
		}
		for _, c := range o.Children {
			collectIDs(c)
		}
	}
	for _, o := range objects {
		collectIDs(o)
	}

	// Second pass: collect particle/effect entries.  Their own IDs join the
	// known set so spell_effect → particle cross-references resolve (the ID
	// children 0xC010/0xC210 don't carry a generic DictID in the tree).
	type rawEntry struct {
		kind    string
		id      uint32
		version int
		blob    []byte
	}
	var raws []rawEntry
	var walk func(o *eqoa.ESFObject)
	walk = func(o *eqoa.ESFObject) {
		t := uint16(o.Header.ObjectType)
		if t == 0xC000 || t == 0xC200 {
			kind, idType, blobType := "particle", uint16(0xC010), uint16(0xC020)
			if t == 0xC200 {
				kind, idType, blobType = "spell_effect", 0xC210, 0xC220
			}
			var id uint32
			var blob []byte
			for _, c := range o.Children {
				switch uint16(c.Header.ObjectType) {
				case idType:
					if body, err := c.ReadRaw(r); err == nil && len(body) >= 4 {
						id = order.Uint32(body)
					}
				case blobType:
					blob, _ = c.ReadRaw(r)
				}
			}
			if id != 0 && len(blob) > 0 {
				raws = append(raws, rawEntry{kind, id, int(o.Header.ObjectVersion), blob})
				if id >= 0x10000 {
					known[id] = true
				}
			}
		}
		for _, c := range o.Children {
			walk(c)
		}
	}
	for _, o := range objects {
		walk(o)
	}

	var entries []particleEntry
	for _, re := range raws {
		refSet := map[uint32]bool{}
		for off := 0; off+4 <= len(re.blob); off += 4 {
			v := order.Uint32(re.blob[off:])
			if v != re.id && known[v] {
				refSet[v] = true
			}
		}
		var refs []string
		for rid := range refSet {
			refs = append(refs, fmt.Sprintf("0x%X", rid))
		}
		pe := particleEntry{
			Type:    re.kind,
			ID:      fmt.Sprintf("0x%X", re.id),
			Version: re.version,
			Size:    len(re.blob),
			Refs:    refs,
			Data:    base64.StdEncoding.EncodeToString(re.blob),
		}
		if re.kind == "particle" && len(re.blob) >= 24 {
			pe.Texture = fmt.Sprintf("0x%X", order.Uint32(re.blob[0:]))
			pe.LayerCount = int(order.Uint32(re.blob[20:])) + 1
			blend, layers := decodeParticleLayers(re.blob, order)
			pe.BlendMode = &blend
			pe.Layers = layers
		}
		entries = append(entries, pe)
	}

	if len(entries) == 0 {
		return
	}
	path := filepath.Join(outDir, prefix+"_particlefx.json")
	f, err := os.Create(path)
	if err != nil {
		return
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", " ")
	enc.Encode(entries)
	f.Close()
	if verbose {
		fmt.Printf("  → %d particle/effect definition(s) → %s\n", len(entries), filepath.Base(path))
	}

	writeEffectGLBs(r, objects, order, prefix, outDir, verbose, entries)
}

// writeEffectGLBs exports each spell effect as a standalone GLB that can be
// attached to any exported character (parent it to a named joint node):
// one alpha-blended billboard card per referenced particle texture, so the
// effect is visible in any glTF viewer, plus the complete parameter payload
// (effect + its particle definitions) in scene extras for engines that
// implement a real emitter.  glTF has no standard particle representation —
// parameters + sprite textures is the portable industry shape.
func writeEffectGLBs(r io.ReadSeeker, objects []*eqoa.ESFObject, order binary.ByteOrder, prefix string, outDir string, verbose bool, entries []particleEntry) {

	// Index surfaces and particle entries for reference resolution.
	surfObjs := map[uint32]*eqoa.ESFObject{}
	var walkS func(o *eqoa.ESFObject)
	walkS = func(o *eqoa.ESFObject) {
		if uint16(o.Header.ObjectType) == 0x1000 && o.DictID >= 0x10000 {
			if _, ok := surfObjs[o.DictID]; !ok {
				surfObjs[o.DictID] = o
			}
		}
		for _, c := range o.Children {
			walkS(c)
		}
	}
	for _, o := range objects {
		walkS(o)
	}

	entryByID := map[uint32]*particleEntry{}
	for i := range entries {
		var id uint32
		fmt.Sscanf(entries[i].ID, "0x%X", &id)
		entryByID[id] = &entries[i]
	}

	effectsDir := filepath.Join(outDir, "effects")
	count := 0
	for i := range entries {
		e := &entries[i]
		if e.Type != "spell_effect" {
			continue
		}

		// Gather this effect's particle definitions and every texture they
		// (or the effect itself) reference.
		var particles []*particleEntry
		texIDs := map[uint32]bool{}
		var collectTex = func(pe *particleEntry) {
			for _, rs := range pe.Refs {
				var rid uint32
				fmt.Sscanf(rs, "0x%X", &rid)
				if _, isSurf := surfObjs[rid]; isSurf {
					texIDs[rid] = true
				}
			}
		}
		collectTex(e)
		for _, rs := range e.Refs {
			var rid uint32
			fmt.Sscanf(rs, "0x%X", &rid)
			if pe, ok := entryByID[rid]; ok && pe.Type == "particle" {
				particles = append(particles, pe)
				collectTex(pe)
			}
		}
		if len(texIDs) == 0 {
			continue
		}

		b := gltf.NewBuilder()
		cardIdx := 0
		for tid := range texIDs {
			body, err := surfObjs[tid].ReadBody(r)
			if err != nil {
				continue
			}
			s, err := eqoa.ParseSurface(body, order)
			if err != nil {
				continue
			}
			img, err := s.ToImage(0)
			if err != nil {
				continue
			}
			pngBuf := new(bytes.Buffer)
			if png.Encode(pngBuf, img) != nil {
				continue
			}
			imgBV := b.AddBufferView(pngBuf.Bytes(), 0)
			imgIdx := len(b.Doc.Images)
			b.Doc.Images = append(b.Doc.Images, gltf.Image{BufferView: imgBV, MimeType: "image/png"})
			texIdx := len(b.Doc.Textures)
			b.Doc.Textures = append(b.Doc.Textures, gltf.Texture{Source: imgIdx})
			matIdx := len(b.Doc.Materials)
			b.Doc.Materials = append(b.Doc.Materials, gltf.Material{
				Name: fmt.Sprintf("fx_0x%X", tid),
				PBRMetallicRoughness: &gltf.PBR{
					BaseColorTexture: &gltf.TextureInfo{Index: texIdx},
					MetallicFactor:   0,
					RoughnessFactor:  1,
				},
				EmissiveFactor: []float32{1, 1, 1},
				AlphaMode:      "BLEND",
				DoubleSided:    true,
			})

			// Unit billboard quad, cards fanned along +X.
			x := float32(cardIdx) * 1.1
			pos := []float32{x - 0.5, -0.5, 0, x + 0.5, -0.5, 0, x - 0.5, 0.5, 0, x + 0.5, 0.5, 0}
			uv := []float32{0, 1, 1, 1, 0, 0, 1, 0}
			idx := []uint32{0, 1, 2, 2, 1, 3}

			posBuf := new(bytes.Buffer)
			binary.Write(posBuf, binary.LittleEndian, pos)
			posBV := b.AddBufferView(posBuf.Bytes(), 34962)
			posAcc := b.AddAccessor(posBV, 0, 5126, 4, "VEC3", false)
			b.Doc.Accessors[posAcc].Min = []float32{x - 0.5, -0.5, 0}
			b.Doc.Accessors[posAcc].Max = []float32{x + 0.5, 0.5, 0}
			uvBuf := new(bytes.Buffer)
			binary.Write(uvBuf, binary.LittleEndian, uv)
			uvAcc := b.AddAccessor(b.AddBufferView(uvBuf.Bytes(), 34962), 0, 5126, 4, "VEC2", false)
			idxBuf := new(bytes.Buffer)
			binary.Write(idxBuf, binary.LittleEndian, idx)
			idxAcc := b.AddAccessor(b.AddBufferView(idxBuf.Bytes(), 34963), 0, 5125, 6, "SCALAR", false)

			mIdx := b.AddMesh(gltf.Mesh{
				Name: fmt.Sprintf("card_0x%X", tid),
				Primitives: []gltf.Primitive{{
					Attributes: map[string]int{"POSITION": posAcc, "TEXCOORD_0": uvAcc},
					Indices:    &idxAcc,
					Material:   &matIdx,
				}},
			})
			nIdx := b.AddNode(gltf.Node{Mesh: &mIdx, Name: fmt.Sprintf("fx_card_%d", cardIdx)})
			b.AddSceneNode(nIdx)
			cardIdx++
		}
		if cardIdx == 0 {
			continue
		}

		// Full machine-readable payload in scene extras.
		extras := map[string]any{
			"eqoa_spell_effect": e,
			"eqoa_particles":    particles,
			"note": "EQOA effect parameters (semantics not yet decoded). " +
				"Attach this GLB to a character joint node to position the effect.",
		}
		if raw, err := json.Marshal(extras); err == nil {
			b.Doc.Scenes[0].Extras = raw
		}

		if err := os.MkdirAll(effectsDir, 0755); err != nil {
			return
		}
		var eid uint32
		fmt.Sscanf(e.ID, "0x%X", &eid)
		outPath := filepath.Join(effectsDir, fmt.Sprintf("%s_effect_0x%X.glb", prefix, eid))
		if f, err := os.Create(outPath); err == nil {
			b.WriteGLB(f)
			f.Close()
			count++
		}
	}
	if verbose && count > 0 {
		fmt.Printf("  → %d spell-effect GLB(s) → effects/\n", count)
	}
}
