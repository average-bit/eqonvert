package gltf

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"math"
	"sort"

	"github.com/average-bit/eqonvert/pkg/eqoa"
)

// PreTranslation is one entry in the ZonePreTranslations (0x3250) array.
// Each vertex in a pbtype-4 mesh carries a VGroup index that selects the
// translation to add to its local position, resolving seam gaps at sub-block
// boundaries without per-sprite approximation.
type PreTranslation struct {
	East, HeightRef, North float32
}

// ZoneAssembler accumulates terrain sprites from a single Zone object into one
// shared GLB. Surfaces are embedded once (deduplicated by DictID) and materials
// are indexed globally across all palettes so all sprites share the same texture
// set without redundant copies.
//
// Usage:
//  1. Call SetPreTranslations with the full unfiltered 0x3250 array.
//  2. Call LoadZoneResources once per zone.
//  3. Call AddSpriteMeshes for each sprite — geometry is accumulated by material.
//  4. Call FinalizeZoneMesh once to emit a single mesh with one primitive per
//     material, matching the reference GLB structure.
type ZoneAssembler struct {
	b                *Builder
	surfaceToIndex   map[uint32]int
	surfaceAlphaMode map[uint32]string
	// fallbackSurfaces provides pre-parsed surfaces from the whole ESF file.
	// Used when a sprite material references a DictID absent from the zone's
	// own terrain palette — e.g. the leaf texture on the broad-leafed tree
	// exists only in other zones' palettes, not in zone 24's palette.
	fallbackSurfaces map[uint32]*eqoa.Surface
	// paletteStart[i] is the index of the first GLTF material from palette i.
	// Palette i corresponds to sub-block i in the zone's 0x3250 list.
	paletteStart []int
	// preTranslations is the full unfiltered ZonePreTranslations (0x3250) array.
	// Vertex VGroup values index directly into this slice.
	preTranslations []PreTranslation
	// matGroups accumulates vertex+index data keyed by GLTF material index.
	// Key -1 holds geometry that has no valid material assignment.
	matGroups map[int]*mergedGeom
	MinPos    [3]float32
	MaxPos    [3]float32
	hasPos    bool
	// texOverrideIdx caches embedded replacement textures (see texoverride.go)
	// keyed by the source surface DictID they stand in for.
	texOverrideIdx map[uint32]int
}

// overrideTexIndex returns a glTF texture index for a substitute image when
// texID is in the override table (embedding it once), else (0,false).
func (za *ZoneAssembler) overrideTexIndex(texID uint32) (int, bool) {
	img, ok := texOverrides[texID]
	if !ok {
		return 0, false
	}
	if za.texOverrideIdx == nil {
		za.texOverrideIdx = map[uint32]int{}
	}
	if idx, done := za.texOverrideIdx[texID]; done {
		return idx, true
	}
	buf := new(bytes.Buffer)
	png.Encode(buf, img)
	bvIdx := za.b.AddBufferView(buf.Bytes(), 0)
	imgIdx := len(za.b.Doc.Images)
	za.b.Doc.Images = append(za.b.Doc.Images, Image{BufferView: bvIdx, MimeType: "image/png"})
	texIdx := len(za.b.Doc.Textures)
	za.b.Doc.Textures = append(za.b.Doc.Textures, Texture{Source: imgIdx})
	za.texOverrideIdx[texID] = texIdx
	return texIdx, true
}

// SetPreTranslations stores the full unfiltered ZonePreTranslations (0x3250)
// array. Must be called before AddSpriteMeshes. For pbtype-4 meshes, each
// vertex's VGroup field indexes into this array to determine its world offset,
// eliminating seam gaps at sub-block boundaries.
func (za *ZoneAssembler) SetPreTranslations(pts []PreTranslation) {
	za.preTranslations = pts
}

// SetFallbackSurfaces provides a pre-parsed surface pool covering the whole ESF
// file. When embedSurfaces cannot find a needed DictID in the zone's own palette
// arrays, it falls back to this pool. Surfaces are parsed once and shared across
// all zone assemblers created from the same file.
func (za *ZoneAssembler) SetFallbackSurfaces(pool map[uint32]*eqoa.Surface) {
	za.fallbackSurfaces = pool
}

// accVertex holds one vertex in world-space GLB coordinates (X=east, Y=height, Z=north).
// This is Y-up (Height on Y), matching the single-model export convention so
// zones and character/item models share one world frame.
type accVertex struct {
	pos    [3]float32
	uv     [2]float32
	normal [3]float32
	// weld marks a vertex as eligible for seam welding. Terrain sub-block
	// vertices set this (their sub-block boundaries need closing); placed props
	// set it false so their self-contained, finely-detailed geometry is never
	// collapsed by the terrain-tuned weld distance.
	weld bool
}

// mergedGeom holds accumulated geometry for a single material across all sprites.
type mergedGeom struct {
	vertices []accVertex
	indices  []uint32
}

func NewZoneAssembler() *ZoneAssembler {
	return &ZoneAssembler{
		b:                NewBuilder(),
		surfaceToIndex:   make(map[uint32]int),
		surfaceAlphaMode: make(map[uint32]string),
		matGroups:        make(map[int]*mergedGeom),
	}
}

// LoadZoneResources processes a ZoneResources (0x3100) object, embedding all
// surfaces and materials into the shared builder. Handles two zone layouts:
//
//	TUNARIA-style: surfaces are inside each MaterialPalette (0x1110) child
//	ARENA-style:   surfaces are directly under ZoneResources (global pool)
//
// Each MaterialPalette (0x1110) corresponds positionally to one sub-block in
// the zone's 0x3250 list. Sprites from sub-block i use materials from palette i.
// paletteStart[i] records the first GLTF material index for palette i.
//
// Call this once per zone before AddSpriteMeshes.
func (za *ZoneAssembler) LoadZoneResources(r io.ReadSeeker, zoneRes *eqoa.ESFObject, order binary.ByteOrder) {
	// Only collect surface/material arrays from MaterialPalette (0x1110) subtrees.
	// Per-sprite 0x1001/0x1101 objects are handled separately by LoadSpriteMaterials;
	// including them here would create spurious palette entries with wrong indices.
	var surfaceArrays []*eqoa.ESFObject
	var matArrs []*eqoa.ESFObject
	var walk func(obj *eqoa.ESFObject)
	walk = func(obj *eqoa.ESFObject) {
		if uint16(obj.Header.ObjectType) == 0x1110 {
			for _, child := range obj.Children {
				switch uint16(child.Header.ObjectType) {
				case 0x1001:
					if len(child.Children) > 0 {
						surfaceArrays = append(surfaceArrays, child)
					}
				case 0x1101:
					if len(child.Children) > 0 {
						matArrs = append(matArrs, child)
					}
				}
			}
			return
		}
		for _, child := range obj.Children {
			walk(child)
		}
	}
	walk(zoneRes)

	// Collect TexIDs needed by the zone palettes.
	neededTexIDs := map[uint32]bool{}
	for _, matArr := range matArrs {
		for _, mObj := range matArr.Children {
			body, _ := mObj.ReadBody(r)
			m, _ := eqoa.ParseMaterialBody(body, mObj.Header.ObjectVersion, order)
			if m != nil {
				for _, layer := range m.Layers {
					if layer.TexID != 0 {
						neededTexIDs[layer.TexID] = true
					}
				}
			}
		}
	}

	za.embedSurfaces(r, surfaceArrays, neededTexIDs, order)
	za.buildMaterials(r, matArrs, "p", order)
}

// embedSurfaces loads surfaces from the given arrays, deduplicating by DictID,
// embedding only surfaces whose DictID is in neededIDs (pass nil to embed all).
// For any neededID not found in the provided arrays, falls back to the pre-parsed
// pool set by SetFallbackSurfaces — this recovers textures (e.g. leaf textures)
// that are absent from the current zone's palette but exist in other zones.
func (za *ZoneAssembler) embedSurfaces(r io.ReadSeeker, surfaceArrays []*eqoa.ESFObject, neededIDs map[uint32]bool, order binary.ByteOrder) {
	for _, sa := range surfaceArrays {
		for _, sObj := range sa.Children {
			body, _ := sObj.ReadBody(r)
			s, err := eqoa.ParseSurface(body, order)
			if err != nil {
				continue
			}
			if neededIDs != nil && !neededIDs[s.DictID] {
				continue
			}
			if _, already := za.surfaceToIndex[s.DictID]; already {
				continue
			}
			za.embedOneSurface(s)
		}
	}
	// Fallback: for any needed DictID still missing, try the global pool.
	if za.fallbackSurfaces != nil && neededIDs != nil {
		for id := range neededIDs {
			if _, ok := za.surfaceToIndex[id]; ok {
				continue
			}
			s, ok := za.fallbackSurfaces[id]
			if !ok {
				continue
			}
			za.embedOneSurface(s)
		}
	}
}

func (za *ZoneAssembler) embedOneSurface(s *eqoa.Surface) {
	img, err := s.ToImage(0)
	if err != nil {
		return
	}
	buf := new(bytes.Buffer)
	png.Encode(buf, img)
	bvIdx := za.b.AddBufferView(buf.Bytes(), 0)
	imgIdx := len(za.b.Doc.Images)
	za.b.Doc.Images = append(za.b.Doc.Images, Image{BufferView: bvIdx, MimeType: "image/png"})
	texIdx := len(za.b.Doc.Textures)
	za.b.Doc.Textures = append(za.b.Doc.Textures, Texture{Source: imgIdx})
	za.surfaceToIndex[s.DictID] = texIdx
	// Route through alphaModeFor (blendGradients=false) so predominantly-
	// translucent zone surfaces (glass, water) BLEND instead of getting shattered
	// by MASK's hard cutoff, while foliage cutouts stay MASK (no colored halos).
	za.surfaceAlphaMode[s.DictID] = alphaModeFor(s, false)
}

// buildMaterials creates GLTF materials for each entry in each matArr and appends
// them to the palette table. prefix is used for naming ("p" for zone palette,
// "s" for sprite). Returns the starting material index of the first palette.
func (za *ZoneAssembler) buildMaterials(r io.ReadSeeker, matArrs []*eqoa.ESFObject, prefix string, order binary.ByteOrder) {
	for p, matArr := range matArrs {
		start := len(za.b.Doc.Materials)
		za.paletteStart = append(za.paletteStart, start)
		for i, mObj := range matArr.Children {
			body, _ := mObj.ReadBody(r)
			m, err := eqoa.ParseMaterialBody(body, mObj.Header.ObjectVersion, order)
			if err != nil {
				za.b.Doc.Materials = append(za.b.Doc.Materials, Material{
					Name:        fmt.Sprintf("ZoneMat_%s%d_%d_missing", prefix, p, i),
					DoubleSided: true,
					PBRMetallicRoughness: &PBR{
						MetallicFactor:  0,
						RoughnessFactor: 1,
						BaseColorFactor: []float32{0.5, 0.5, 0.5, 1.0},
					},
				})
				continue
			}
			alphaMode := "OPAQUE"
			if len(m.Layers) > 0 {
				if mode, ok := za.surfaceAlphaMode[m.Layers[0].TexID]; ok {
					alphaMode = mode
				}
			}
			gm := Material{
				Name:        fmt.Sprintf("ZoneMat_%s%d_%d_0x%X", prefix, p, i, m.DictID),
				AlphaMode:   alphaMode,
				DoubleSided: true,
				PBRMetallicRoughness: &PBR{
					MetallicFactor:  0,
					RoughnessFactor: 1,
				},
			}
			if alphaMode == "MASK" {
				cutoff := float32(0.5)
				gm.AlphaCutoff = &cutoff
			}
			if len(m.Layers) > 0 {
				if ovIdx, ok := za.overrideTexIndex(m.Layers[0].TexID); ok {
					gm.PBRMetallicRoughness.BaseColorTexture = &TextureInfo{Index: ovIdx}
				} else if texIdx, ok := za.surfaceToIndex[m.Layers[0].TexID]; ok {
					gm.PBRMetallicRoughness.BaseColorTexture = &TextureInfo{Index: texIdx}
				}
			}
			if gm.PBRMetallicRoughness.BaseColorTexture == nil {
				gm.PBRMetallicRoughness.BaseColorFactor = []float32{0.65, 0.65, 0.65, 1.0}
			}
			za.b.Doc.Materials = append(za.b.Doc.Materials, gm)
		}
	}
}

// PaletteStart returns the first GLTF material index for zone palette paletteIdx.
// Terrain tiles use this to route materialIndex offsets into the correct palette.
func (za *ZoneAssembler) PaletteStart(paletteIdx int) int {
	if paletteIdx < 0 || paletteIdx >= len(za.paletteStart) {
		return 0
	}
	return za.paletteStart[paletteIdx]
}

// LoadSpriteMaterials loads the 0x1001 surfaces and 0x1101 materials embedded
// directly in a non-terrain sprite (0x2310) object. Returns the base GLTF
// material index so the caller can route fg.MaterialIndex offsets correctly.
// Returns -1 if the sprite has no embedded material array.
func (za *ZoneAssembler) LoadSpriteMaterials(r io.ReadSeeker, spriteObj *eqoa.ESFObject, order binary.ByteOrder) int {
	var surfArr, matArr *eqoa.ESFObject
	for _, child := range spriteObj.Children {
		switch uint16(child.Header.ObjectType) {
		case 0x1001:
			surfArr = child
		case 0x1101:
			matArr = child
		case 0x1110:
			// MaterialPalette container — used by 0x2000 SimpleSprites from SCENE.ESF.
			for _, gc := range child.Children {
				switch uint16(gc.Header.ObjectType) {
				case 0x1001:
					if surfArr == nil {
						surfArr = gc
					}
				case 0x1101:
					if matArr == nil {
						matArr = gc
					}
				}
			}
		}
	}
	if matArr == nil {
		return -1
	}

	// Collect needed TexIDs from this sprite's materials.
	neededIDs := map[uint32]bool{}
	for _, mObj := range matArr.Children {
		body, _ := mObj.ReadBody(r)
		m, _ := eqoa.ParseMaterialBody(body, mObj.Header.ObjectVersion, order)
		if m != nil {
			for _, layer := range m.Layers {
				if layer.TexID != 0 {
					neededIDs[layer.TexID] = true
				}
			}
		}
	}

	var surfArrs []*eqoa.ESFObject
	if surfArr != nil {
		surfArrs = []*eqoa.ESFObject{surfArr}
	}
	// Always call embedSurfaces so the fallback pool runs even when the
	// sprite carries no embedded surface array (e.g. SCENE.ESF sprites).
	za.embedSurfaces(r, surfArrs, neededIDs, order)

	matBase := len(za.b.Doc.Materials)
	za.buildMaterials(r, []*eqoa.ESFObject{matArr}, "s", order)
	return matBase
}

// ParseSpriteBBox reads the SimpleSubSpriteHeader (0x2311) child of a terrain
// sprite and returns both bbox corners in EQOA world space: [X=east, Y=height, Z=north].
// corner1 is the minimum (southwest-low) corner, corner2 is the maximum.
// Returns false if the header is missing or malformed.
func ParseSpriteBBox(r io.ReadSeeker, spriteObj *eqoa.ESFObject, order binary.ByteOrder) (c1, c2 [3]float32, ok bool) {
	for _, child := range spriteObj.Children {
		if uint16(child.Header.ObjectType) != 0x2311 {
			continue
		}
		body, err := child.ReadBody(r)
		if err != nil || len(body) < 32 {
			return
		}
		// body[8:20]  = corner1 XYZ float32×3 (east, height, north)
		// body[20:32] = corner2 XYZ float32×3
		for k := 0; k < 3; k++ {
			bits := order.Uint32(body[8+k*4 : 12+k*4])
			c1[k] = math.Float32frombits(bits)
			bits = order.Uint32(body[20+k*4 : 24+k*4])
			c2[k] = math.Float32frombits(bits)
		}
		ok = true
		return
	}
	return
}

// ParseSpriteBBoxCorner1 returns only corner1. Prefer ParseSpriteBBox for new callers.
func ParseSpriteBBoxCorner1(r io.ReadSeeker, spriteObj *eqoa.ESFObject, order binary.ByteOrder) ([3]float32, bool) {
	c1, _, ok := ParseSpriteBBox(r, spriteObj, order)
	return c1, ok
}

// ParseSpriteWorldOffset is a backward-compat alias for ParseSpriteBBoxCorner1.
func ParseSpriteWorldOffset(r io.ReadSeeker, spriteObj *eqoa.ESFObject, order binary.ByteOrder) ([3]float32, bool) {
	return ParseSpriteBBoxCorner1(r, spriteObj, order)
}

// AddSpriteMeshes accumulates a sprite's geometry into per-material buckets.
// Call FinalizeZoneMesh after all sprites have been added to emit the combined mesh.
//
// sbEast/sbNorth/sbHeight are the sub-block's anchor from 0x3250 ZonePreTranslations.
// Vertex Pos[] are local to the sub-block; world position:
//
//	GLB.X = Pos[0] + sbEast
//	GLB.Y = Pos[1] + sbHeight
//	GLB.Z = Pos[2] + sbNorth
//
// matStart is the base GLTF material index for this sprite's face groups.
// For terrain tiles: matStart = PaletteStart(subBlockIdx).
// For non-terrain sprites: matStart = LoadSpriteMaterials(…).
// Pass -1 to accumulate without any material (solid grey primitive).
func (za *ZoneAssembler) AddSpriteMeshes(asset *eqoa.Asset, _ string, sbEast, sbNorth, sbHeight float32, matStart int) {
	for _, mesh := range asset.Meshes {
		for _, fg := range mesh.FaceGroups {
			if len(fg.Vertices) == 0 {
				continue
			}

			// Resolve the GLTF material key for this face group.
			matKey := -1
			if matStart >= 0 {
				idx := matStart + int(fg.MaterialIndex)
				if idx < len(za.b.Doc.Materials) {
					matKey = idx
				}
			}

			// Accumulate into the per-material bucket.
			mg := za.matGroups[matKey]
			if mg == nil {
				mg = &mergedGeom{}
				za.matGroups[matKey] = mg
			}
			base := uint32(len(mg.vertices))
			for _, v := range fg.Vertices {
				// Per-vertex world transform: pbtype-4 vertices carry VGroup
				// which indexes directly into the ZonePreTranslations array.
				// This is the correct way to resolve seam gaps — the reference
				// Java exporter (joukop/ESF-file-format, PrimBuffer.java) does
				// the same per-vertex lookup. Non-pbtype-4 meshes fall back to
				// the caller-supplied sub-block anchor.
				east, north, height := sbEast, sbNorth, sbHeight
				if mesh.Type == 4 {
					vg := int(v.VGroup)
					if vg >= 0 && vg < len(za.preTranslations) {
						pt := za.preTranslations[vg]
						east, north, height = pt.East, pt.North, pt.HeightRef
					}
				}
				wp := [3]float32{
					v.Pos[0] + east,
					v.Pos[1] + height,
					v.Pos[2] + north,
				}
				if !za.hasPos {
					za.MinPos, za.MaxPos = wp, wp
					za.hasPos = true
				} else {
					for k := 0; k < 3; k++ {
						if wp[k] < za.MinPos[k] {
							za.MinPos[k] = wp[k]
						}
						if wp[k] > za.MaxPos[k] {
							za.MaxPos[k] = wp[k]
						}
					}
				}
				mg.vertices = append(mg.vertices, accVertex{
					pos:    wp,
					uv:     v.UV,
					normal: [3]float32{v.Normal[0], v.Normal[1], v.Normal[2]},
					// Weld terrain only when the zone actually tiles MULTIPLE
					// sub-blocks — seams exist between adjacent tiles, so a single
					// tile (or none) has nothing to close. Structure zones like
					// ARENA carry one pretranslation and no seams; welding there
					// only snaps detailed prop geometry (clockwork, gears) together
					// into a crumpled mess. A count > 1 is real tiling.
					weld: len(za.preTranslations) > 1,
				})
			}
			for _, idx := range fg.Indices {
				mg.indices = append(mg.indices, base+uint32(idx))
			}
		}
	}
}

// AddUnlitMaterial appends a doubly-sided, fully-emissive (unlit-looking)
// material of the given linear RGB colour and returns its index. Used for the
// built-in spawn marker so it reads as a bright solid shape regardless of scene
// lighting.
func (za *ZoneAssembler) AddUnlitMaterial(name string, rgb [3]float32) int {
	idx := len(za.b.Doc.Materials)
	za.b.Doc.Materials = append(za.b.Doc.Materials, Material{
		Name:           name,
		DoubleSided:    true,
		EmissiveFactor: []float32{rgb[0], rgb[1], rgb[2]},
		PBRMetallicRoughness: &PBR{
			BaseColorFactor: []float32{rgb[0], rgb[1], rgb[2], 1.0},
			MetallicFactor:  0,
			RoughnessFactor: 1,
		},
	})
	return idx
}

// AddParticleEmitter marks a particle/effect emitter (ESF 0xC100) at an EQOA
// world position. glTF has no particle system, so the emitter is exported as a
// small octahedron marker — unlit and tinted with the effect's start colour so
// fire reads orange, water blue, smoke grey — with the full decoded parameters
// attached to the node's `extras` for a downstream tool to recreate the live
// effect. size is the marker half-extent (world units). pos = (East, Height,
// North), emitted directly as Y-up GLB.
func (za *ZoneAssembler) AddParticleEmitter(name string, pos [3]float32, rgb [3]float32, size float32, extras json.RawMessage) {
	matIdx := za.AddUnlitMaterial(name, rgb)

	// Octahedron vertices (±size on each axis), translated to pos.
	local := [6][3]float32{
		{+size, 0, 0}, {-size, 0, 0}, {0, +size, 0}, {0, -size, 0}, {0, 0, +size}, {0, 0, -size},
	}
	posBuf := new(bytes.Buffer)
	minv := [3]float32{pos[0] - size, pos[1] - size, pos[2] - size}
	maxv := [3]float32{pos[0] + size, pos[1] + size, pos[2] + size}
	for _, v := range local {
		binary.Write(posBuf, binary.LittleEndian, [3]float32{v[0] + pos[0], v[1] + pos[1], v[2] + pos[2]})
	}
	posBv := za.b.AddBufferView(posBuf.Bytes(), 34962)
	posAcc := za.b.AddAccessor(posBv, 0, 5126, 6, "VEC3", false)
	za.b.Doc.Accessors[posAcc].Min = minv[:]
	za.b.Doc.Accessors[posAcc].Max = maxv[:]

	idxBuf := new(bytes.Buffer)
	for _, i := range []uint16{0, 2, 4, 2, 1, 4, 1, 3, 4, 3, 0, 4, 2, 0, 5, 1, 2, 5, 3, 1, 5, 0, 3, 5} {
		binary.Write(idxBuf, binary.LittleEndian, i)
	}
	idxBv := za.b.AddBufferView(idxBuf.Bytes(), 34963)
	idxAcc := za.b.AddAccessor(idxBv, 0, 5123, 24, "SCALAR", false)

	meshIdx := za.b.AddMesh(Mesh{
		Name:       name,
		Primitives: []Primitive{{Attributes: map[string]int{"POSITION": posAcc}, Indices: &idxAcc, Material: &matIdx}},
	})
	nodeIdx := za.b.AddNode(Node{Name: name, Mesh: &meshIdx, Extras: extras})
	za.b.AddSceneNode(nodeIdx)

	if !za.hasPos {
		za.MinPos, za.MaxPos, za.hasPos = pos, pos, true
	} else {
		for k := 0; k < 3; k++ {
			if pos[k] < za.MinPos[k] {
				za.MinPos[k] = pos[k]
			}
			if pos[k] > za.MaxPos[k] {
				za.MaxPos[k] = pos[k]
			}
		}
	}
}

// AddSpriteAtWorldPos places a non-terrain sprite at an absolute EQOA world position.
// pos[0]=East, pos[1]=Height, pos[2]=North. rotY rotates around the EQOA Height
// (vertical) axis in radians. scale is a uniform scale factor applied before
// the world translation.
func (za *ZoneAssembler) AddSpriteAtWorldPos(asset *eqoa.Asset, pos [3]float32, rot [3]float32, scale float32, matStart int) {
	// Full Euler rotation about the EQOA local axes (East=X, Height=Y, North=Z):
	// rot[0] yaw about Height (vertical), rot[1] pitch about East, rot[2] roll
	// about North — composed intrinsically R = Ryaw·Rpitch·Rroll. This reduces
	// EXACTLY to the previous yaw-only 2D rotation when rot[1]==rot[2]==0 (so
	// props with only a heading are byte-for-byte unchanged); only the minority
	// of actors that carry a pitch/roll (props following terrain slope) change.
	cy := float32(math.Cos(float64(rot[0])))
	sy := float32(math.Sin(float64(rot[0])))
	cp := float32(math.Cos(float64(rot[1])))
	sp := float32(math.Sin(float64(rot[1])))
	cr := float32(math.Cos(float64(rot[2])))
	sr := float32(math.Sin(float64(rot[2])))
	// R = Ryaw(Y) · Rpitch(X) · Rroll(Z), row-major 3×3.
	m00 := cy*cr - sy*sp*sr
	m01 := -cy*sr - sy*sp*cr
	m02 := -sy * cp
	m10 := cp * sr
	m11 := cp * cr
	m12 := -sp
	m20 := sy*cr + cy*sp*sr
	m21 := -sy*sr + cy*sp*cr
	m22 := cy * cp
	// rotVec rotates a local (East,Height,North) vector by R.
	rotVec := func(x, y, z float32) (float32, float32, float32) {
		return m00*x + m01*y + m02*z,
			m10*x + m11*y + m12*z,
			m20*x + m21*y + m22*z
	}

	for _, mesh := range asset.Meshes {
		for _, fg := range mesh.FaceGroups {
			if len(fg.Vertices) == 0 {
				continue
			}
			matKey := -1
			if matStart >= 0 {
				idx := matStart + int(fg.MaterialIndex)
				if idx < len(za.b.Doc.Materials) {
					matKey = idx
				}
			}
			mg := za.matGroups[matKey]
			if mg == nil {
				mg = &mergedGeom{}
				za.matGroups[matKey] = mg
			}
			base := uint32(len(mg.vertices))
			for _, v := range fg.Vertices {
				le := v.Pos[0] * scale
				lh := v.Pos[1] * scale
				ln := v.Pos[2] * scale
				// Rotate by the full Euler matrix. EQOA (East,Height,North)
				// is emitted directly as glTF (East,Height,North) — Y-up,
				// matching the model export.
				re, rh, rn := rotVec(le, lh, ln)
				wp := [3]float32{
					re + pos[0],
					rh + pos[1],
					rn + pos[2],
				}
				if !za.hasPos {
					za.MinPos, za.MaxPos = wp, wp
					za.hasPos = true
				} else {
					for k := 0; k < 3; k++ {
						if wp[k] < za.MinPos[k] {
							za.MinPos[k] = wp[k]
						}
						if wp[k] > za.MaxPos[k] {
							za.MaxPos[k] = wp[k]
						}
					}
				}
				ne, nh, nn := rotVec(v.Normal[0], v.Normal[1], v.Normal[2])
				mg.vertices = append(mg.vertices, accVertex{
					pos:    wp,
					uv:     v.UV,
					normal: [3]float32{ne, nh, nn},
					// Placed props are self-contained sprites with no sub-block
					// seams — welding them (esp. detailed props like clockwork
					// gears, spaced ~0.3u) collapses their geometry. Never weld.
					weld: false,
				})
			}
			for _, idx := range fg.Indices {
				mg.indices = append(mg.indices, base+uint32(idx))
			}
		}
	}
}

// EulerRotMatrix builds the row-major 3×3 rotation for EQOA actor euler angles
// rot = [yaw about Height(Y), pitch about East(X), roll about North(Z)],
// composed R = Ryaw·Rpitch·Rroll — the same convention AddSpriteAtWorldPos bakes.
// Exported so callers can transform a sprite's collision geometry by the same
// actor placement the visual mesh receives (see cmd collectSpriteCollision).
func EulerRotMatrix(rot [3]float32) [9]float32 {
	cy := float32(math.Cos(float64(rot[0])))
	sy := float32(math.Sin(float64(rot[0])))
	cp := float32(math.Cos(float64(rot[1])))
	sp := float32(math.Sin(float64(rot[1])))
	cr := float32(math.Cos(float64(rot[2])))
	sr := float32(math.Sin(float64(rot[2])))
	return [9]float32{
		cy*cr - sy*sp*sr, -cy*sr - sy*sp*cr, -sy * cp,
		cp * sr, cp * cr, -sp,
		sy*cr + cy*sp*sr, -sy*sr + cy*sp*cr, cy * cp,
	}
}

// mat3ToQuat converts a row-major 3×3 rotation matrix (as produced by
// EulerRotMatrix) to a normalized glTF quaternion [x, y, z, w]. Uses the stable
// trace/largest-diagonal branch method so no axis blows up near 180°.
func mat3ToQuat(m [9]float32) []float32 {
	// m indices: [0 1 2 / 3 4 5 / 6 7 8] = R[row][col].
	m00, m01, m02 := float64(m[0]), float64(m[1]), float64(m[2])
	m10, m11, m12 := float64(m[3]), float64(m[4]), float64(m[5])
	m20, m21, m22 := float64(m[6]), float64(m[7]), float64(m[8])
	tr := m00 + m11 + m22
	var x, y, z, w float64
	switch {
	case tr > 0:
		s := math.Sqrt(tr+1.0) * 2 // s = 4w
		w = 0.25 * s
		x = (m21 - m12) / s
		y = (m02 - m20) / s
		z = (m10 - m01) / s
	case m00 > m11 && m00 > m22:
		s := math.Sqrt(1.0+m00-m11-m22) * 2 // s = 4x
		w = (m21 - m12) / s
		x = 0.25 * s
		y = (m01 + m10) / s
		z = (m02 + m20) / s
	case m11 > m22:
		s := math.Sqrt(1.0+m11-m00-m22) * 2 // s = 4y
		w = (m02 - m20) / s
		x = (m01 + m10) / s
		y = 0.25 * s
		z = (m12 + m21) / s
	default:
		s := math.Sqrt(1.0+m22-m00-m11) * 2 // s = 4z
		w = (m10 - m01) / s
		x = (m02 + m20) / s
		y = (m12 + m21) / s
		z = 0.25 * s
	}
	n := math.Sqrt(x*x + y*y + z*z + w*w)
	if n == 0 {
		return []float32{0, 0, 0, 1}
	}
	return []float32{float32(x / n), float32(y / n), float32(z / n), float32(w / n)}
}

// AddAnimatedSpriteNode places an animated/hierarchical sprite as its own
// skinned+animated glTF subtree (preserving its skeleton and animation clips),
// parented under a node carrying the actor's world transform — instead of baking
// it into the flat zone mesh. This keeps the historical animation (e.g. a
// spinning clockwork) alive and avoids the frozen/wrong rest-pose look that
// baking produces. Static geometry still goes through AddSpriteAtWorldPos.
func (za *ZoneAssembler) AddAnimatedSpriteNode(r io.ReadSeeker, asset *eqoa.Asset, order binary.ByteOrder, registry *eqoa.SurfaceRegistry, pos [3]float32, rot [3]float32, scale float32) error {
	rootIdx, err := ExportAssetToBuilder(za.b, r, asset, order, registry, false)
	if err != nil {
		return err
	}
	// Place the subtree at the actor's world transform as explicit TRS, not a
	// baked matrix. The root anchors a skin/skeleton, and glTF viewers commonly
	// mis-decompose a `matrix` on such a node (uniform scale + a rotation whose
	// columns carry negative entries), shearing the whole skinned hierarchy —
	// which is exactly the clockwork "warp" seen only once the sprite is placed
	// in a zone (the un-transformed standalone export renders correctly). TRS is
	// decomposition-free, so the skin resolves cleanly. The root is not
	// animation-targeted (only its joint children are), so plain TRS is legal.
	n := &za.b.Doc.Nodes[rootIdx]
	n.Matrix = nil
	n.Translation = []float32{pos[0], pos[1], pos[2]}
	n.Rotation = mat3ToQuat(EulerRotMatrix(rot))
	n.Scale = []float32{scale, scale, scale}
	za.b.AddSceneNode(rootIdx)

	// Extend the zone bbox by the actor position (the sprite is small vs a zone).
	if !za.hasPos {
		za.MinPos, za.MaxPos, za.hasPos = pos, pos, true
	} else {
		for k := 0; k < 3; k++ {
			if pos[k] < za.MinPos[k] {
				za.MinPos[k] = pos[k]
			}
			if pos[k] > za.MaxPos[k] {
				za.MaxPos[k] = pos[k]
			}
		}
	}
	return nil
}

// zoneLightIntensity is the KHR_lights_punctual intensity (candela) given to
// recovered zone point lights. The 0x2b00 defs carry only color + radius, not
// intensity, so this is a fixed, tunable value chosen to read well in viewers.
const zoneLightIntensity = 22.5

// AddPointLight places a KHR_lights_punctual point light at EQOA world position
// pos = [East, Height, North], emitted directly as GLB [East, Height, North] (Y-up).
// color is linear RGB in [0,1]; radius (world units) becomes the light range.
func (za *ZoneAssembler) AddPointLight(name string, pos [3]float32, color [3]float32, radius float32) {
	wp := [3]float32{pos[0], pos[1], pos[2]}
	li := za.b.AddPointLight(name, color, zoneLightIntensity, radius)
	za.b.AddLightNode(name, wp, li)
	if !za.hasPos {
		za.MinPos, za.MaxPos = wp, wp
		za.hasPos = true
	} else {
		for k := 0; k < 3; k++ {
			if wp[k] < za.MinPos[k] {
				za.MinPos[k] = wp[k]
			}
			if wp[k] > za.MaxPos[k] {
				za.MaxPos[k] = wp[k]
			}
		}
	}
}

// weldEps is the world-unit snapping distance for vertex welding.
// At EQOA scale (~8 units avg inter-vertex spacing on terrain) this closes
// sub-block boundary micro-gaps without merging any interior vertices.
const weldEps = float32(0.5)

// weldVertices merges vertices whose positions are within weldEps of each
// other. Only position is compared; the canonical vertex (first encountered
// in each grid cell) wins for UV and normal. Degenerate triangles produced
// by merging are removed. Returns the compacted vertex and index slices.
func weldVertices(verts []accVertex, indices []uint32) ([]accVertex, []uint32) {
	type key3 struct{ x, y, z int32 }
	scale := float32(1.0) / weldEps

	remap := make([]uint32, len(verts))
	canonical := make([]int, 0, len(verts))
	keyToCanon := make(map[key3]uint32, len(verts))

	for i, v := range verts {
		// Non-weldable vertices (placed props) always get a unique slot — never
		// merged with each other or with terrain — so their fine detail survives.
		if !v.weld {
			remap[i] = uint32(len(canonical))
			canonical = append(canonical, i)
			continue
		}
		k := key3{
			int32(math.Round(float64(v.pos[0] * scale))),
			int32(math.Round(float64(v.pos[1] * scale))),
			int32(math.Round(float64(v.pos[2] * scale))),
		}
		if c, ok := keyToCanon[k]; ok {
			remap[i] = c
		} else {
			c = uint32(len(canonical))
			keyToCanon[k] = c
			canonical = append(canonical, i)
			remap[i] = c
		}
	}

	newVerts := make([]accVertex, len(canonical))
	for newIdx, origIdx := range canonical {
		newVerts[newIdx] = verts[origIdx]
	}

	// Remap indices; drop triangles that became degenerate after welding.
	newIndices := make([]uint32, 0, len(indices))
	for i := 0; i+2 < len(indices); i += 3 {
		a, b, c := remap[indices[i]], remap[indices[i+1]], remap[indices[i+2]]
		if a == b || b == c || a == c {
			continue
		}
		newIndices = append(newIndices, a, b, c)
	}
	return newVerts, newIndices
}

// FinalizeZoneMesh creates a single GLTF mesh named `name` with one primitive
// per unique material, merging all face groups accumulated across all sprites.
// Must be called once after all AddSpriteMeshes calls and before Builder().WriteGLB.
func (za *ZoneAssembler) FinalizeZoneMesh(name string) {
	if len(za.matGroups) == 0 {
		return
	}

	// Sort material keys for deterministic output.
	keys := make([]int, 0, len(za.matGroups))
	for k := range za.matGroups {
		keys = append(keys, k)
	}
	sort.Ints(keys)

	gMesh := Mesh{Name: name}
	for _, matKey := range keys {
		mg := za.matGroups[matKey]
		if len(mg.vertices) == 0 || len(mg.indices) == 0 {
			continue
		}

		// Weld boundary vertices before packing to close sub-block seam gaps.
		mg.vertices, mg.indices = weldVertices(mg.vertices, mg.indices)
		if len(mg.indices) == 0 {
			continue
		}

		prim := Primitive{Attributes: make(map[string]int)}
		if matKey >= 0 {
			prim.Material = ptrInt(matKey)
		}

		// POSITION
		posData := new(bytes.Buffer)
		var minP, maxP [3]float32
		for j, av := range mg.vertices {
			binary.Write(posData, binary.LittleEndian, av.pos)
			if j == 0 {
				minP, maxP = av.pos, av.pos
			} else {
				for k := 0; k < 3; k++ {
					if av.pos[k] < minP[k] {
						minP[k] = av.pos[k]
					}
					if av.pos[k] > maxP[k] {
						maxP[k] = av.pos[k]
					}
				}
			}
		}
		bvIdx := za.b.AddBufferView(posData.Bytes(), 34962)
		accIdx := za.b.AddAccessor(bvIdx, 0, 5126, len(mg.vertices), "VEC3", false)
		za.b.Doc.Accessors[accIdx].Min = minP[:]
		za.b.Doc.Accessors[accIdx].Max = maxP[:]
		prim.Attributes["POSITION"] = accIdx

		// TEXCOORD_0
		uvData := new(bytes.Buffer)
		for _, av := range mg.vertices {
			binary.Write(uvData, binary.LittleEndian, av.uv)
		}
		bvIdx = za.b.AddBufferView(uvData.Bytes(), 34962)
		prim.Attributes["TEXCOORD_0"] = za.b.AddAccessor(bvIdx, 0, 5126, len(mg.vertices), "VEC2", false)

		// NORMAL — emit unit-length normals; a few source verts carry a zero
		// normal (degenerate source quads) which glTF rejects, so normalize and
		// fall back to +Y (up) for zero-length ones.
		normData := new(bytes.Buffer)
		for _, av := range mg.vertices {
			n := av.normal
			l := float32(math.Sqrt(float64(n[0]*n[0] + n[1]*n[1] + n[2]*n[2])))
			if l < 1e-6 {
				n = [3]float32{0, 1, 0}
			} else {
				n = [3]float32{n[0] / l, n[1] / l, n[2] / l}
			}
			binary.Write(normData, binary.LittleEndian, n)
		}
		bvIdx = za.b.AddBufferView(normData.Bytes(), 34962)
		prim.Attributes["NORMAL"] = za.b.AddAccessor(bvIdx, 0, 5126, len(mg.vertices), "VEC3", false)

		// INDICES
		idxData := new(bytes.Buffer)
		for _, idx := range mg.indices {
			binary.Write(idxData, binary.LittleEndian, idx)
		}
		bvIdx = za.b.AddBufferView(idxData.Bytes(), 34963)
		prim.Indices = ptrInt(za.b.AddAccessor(bvIdx, 0, 5125, len(mg.indices), "SCALAR", false))

		gMesh.Primitives = append(gMesh.Primitives, prim)
	}

	if len(gMesh.Primitives) == 0 {
		return
	}
	mIdx := za.b.AddMesh(gMesh)
	nodeIdx := za.b.AddNode(Node{
		Mesh: &mIdx,
		Name: name,
	})
	za.b.AddSceneNode(nodeIdx)
}

// HasPos reports whether any vertex positions have been accumulated.
func (za *ZoneAssembler) HasPos() bool { return za.hasPos }

// Builder returns the accumulated GLTF builder for writing.
func (za *ZoneAssembler) Builder() *Builder { return za.b }
