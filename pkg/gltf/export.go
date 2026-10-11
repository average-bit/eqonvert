package gltf

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"

	"github.com/average-bit/eqonvert/pkg/eqoa"
)

// quatNorm normalizes a [4]float32 quaternion and returns it as a []float32.
// Returns identity [0,0,0,1] if the input is degenerate (zero magnitude) or contains NaN/Inf.
func quatNorm(q [4]float32) []float32 {
	for _, v := range q {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return []float32{0, 0, 0, 1}
		}
	}
	x, y, z, w := float64(q[0]), float64(q[1]), float64(q[2]), float64(q[3])
	mag := math.Sqrt(x*x + y*y + z*z + w*w)
	if mag < 1e-10 {
		return []float32{0, 0, 0, 1}
	}
	return []float32{float32(x / mag), float32(y / mag), float32(z / mag), float32(w / mag)}
}

// normalNorm normalizes a [3]float32 normal vector.
// Returns (0,1,0) for zero-length vectors or inputs containing NaN/Inf.
func normalNorm(n [3]float32) [3]float32 {
	for _, v := range n {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return [3]float32{0, 1, 0}
		}
	}
	mag := math.Sqrt(float64(n[0]*n[0] + n[1]*n[1] + n[2]*n[2]))
	if mag < 1e-10 {
		return [3]float32{0, 1, 0}
	}
	f := float32(1.0 / mag)
	return [3]float32{n[0] * f, n[1] * f, n[2] * f}
}

// sanitizeVec3 replaces NaN/Inf components with 0.
func sanitizeVec3(v [3]float32) [3]float32 {
	for i, c := range v {
		if math.IsNaN(float64(c)) || math.IsInf(float64(c), 0) {
			v[i] = 0
		}
	}
	return v
}

// sanitizeFloat replaces NaN/Inf with 0.
func sanitizeFloat(f float32) float32 {
	if math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
		return 0
	}
	return f
}

// emitBlendFromMaterial forces alphaMode BLEND wherever the source material's blend mode is
// non-zero (modes 1-4 all set PRIM.ABE and write GS ALPHA, i.e. a genuinely translucent pass).
//
// It is OFF because being faithful to the source made the result worse. On PS2 those passes draw
// in a fixed, authored order with Z-write suppressed; glTF has no way to express draw order, so a
// BLEND material lands in the viewer's transparent pass and is ordered by distance instead.
// Layered geometry then drops out -- on the phantom, the chains hanging over its robe disappear
// when viewed head-on, which is far more objectionable than the chains being too opaque.
//
// So this is a case where the disassembly is right about the hardware and wrong about the export.
// The blend mode is recorded in material extras regardless (see matExtras), which is the better
// home for it: a renderer that controls its own draw order can act on it, while a generic glTF
// viewer is not handed an ordering problem it cannot solve.
//
// Turn on with SetBlendFromMaterial / --blend-from-material to inspect the difference.
var emitBlendFromMaterial = false

// SetBlendFromMaterial toggles deriving alphaMode from the source blend mode. See
// emitBlendFromMaterial for why this defaults to off.
func SetBlendFromMaterial(on bool) { emitBlendFromMaterial = on }

// gradientBlendBar is the TranslucentFraction above which a character/item MASK surface is
// upgraded to BLEND. Tunable because the right value is a judgement call the measurements cannot
// settle, and the current one is known to be marginal:
//
// A CHARCUST hair sheet measures tf = 0.052 and so clears the 0.05 bar by two thousandths, which
// is why hair exports as BLEND. Against the hardware that is the wrong call — the client draws
// 79.8% of the sheet, glTF MASK@0.5 draws 84.3%, BLEND draws ~96%, so MASK is the closer match by
// a wide margin and the "edges only" symptom cannot arise from a sheet that is 80% dense.
// Genuinely sheer character surfaces measure tf 0.21-0.24, so a bar near 0.10 separates the two
// cleanly.
//
// Set to 0.10 after visual confirmation through the A/B harness: MASK hair and, more visibly,
// solid headwear both read better than the BLEND versions. Faithful is not automatically better --
// this session twice found the opposite -- so the number was checked by eye before being adopted.
// Override with --gradient-blend-bar.
var gradientBlendBar = 0.10

// SetGradientBlendBar sets the MASK-to-BLEND threshold for character/item surfaces.
func SetGradientBlendBar(v float64) { gradientBlendBar = v }

// maskCutoff is the glTF alphaCutoff written for MASK materials.
//
// 0.5 is not what the hardware does. The client alpha-tests at AREF=128 GEQUAL against a texture
// alpha byte that is a full 0-255 range, so it draws exactly the texels with raw alpha >= 128.
// eqonvert's palette rescale maps raw >= 128 to PNG 255 and raw < 128 to raw*2 -- verified over
// 7.9M exported texels: no odd alpha value below 255 ever occurs, and there are exactly 128
// distinct sub-255 values. So PNG alpha == 255 corresponds precisely to raw >= 128, and a cutoff
// just above 254/255 reproduces the hardware's set exactly.
//
// At 0.5 the export instead draws every texel with PNG alpha >= 128, i.e. raw >= 64 -- twice as
// permissive as the console. Measured over-draw on Frontiers content:
//
//	CHAR    173 surfaces   mean  1.72 pts   max 17.35
//	ARENA    10 surfaces   mean 13.05 pts   max 54.70
//	LAVASTM  11 surfaces   mean  4.61 pts   max  9.09
//
// Set to 0.999 after visual confirmation on Qeynos (buildings, foliage and coastline together).
// The caution that kept it at 0.5 -- that removing half a foliage surface's texels might read as
// sparse or shredded -- turned out to be backwards. At 0.5 the extra texels are not extra
// coverage, they are the INTERIORS OF HOLES: a lattice window rendered with opaque black diamonds
// instead of showing grass through the trellis, and shutters with black blotches instead of the
// wall behind. 0.999 resolves both, and foliage was confirmed better too.
//
// Override with --alpha-cutoff.
var maskCutoff = float32(0.999)

// charMaskCutoff is the same threshold for CHARACTER and ITEM content, and it is deliberately
// NOT the console value.
//
// 0.999 is faithful, and on characters it is a severe regression. A face is not a texture stretched
// over the head; it is an alpha-masked detail overlay on top of a plain skin material. Discarding
// every texel below raw 128 removes most of that overlay, so the features bunch toward the centre
// of the face and the surrounding area falls back to base skin -- which then disagrees in colour
// with the neck. Hair suffers the same way. Confirmed visually on Elf Male and Dark Elf.
//
// The zone case that justified 0.999 does not apply here: there, the sub-128 texels were the
// INTERIORS of cutout holes and drawing them produced opaque black lattice windows. On a character
// they are the body of the artwork. Same number, opposite meaning, so the threshold follows the
// content split the exporter already makes (blendGradients).
//
// Override with --char-alpha-cutoff.
var charMaskCutoff = float32(0.5)

// SetMaskCutoff sets the glTF alphaCutoff for zone/environment MASK materials. See maskCutoff.
func SetMaskCutoff(v float64) { maskCutoff = float32(v) }

// SetCharMaskCutoff sets the glTF alphaCutoff for character/item MASK materials.
// See charMaskCutoff for why it differs from the zone value.
func SetCharMaskCutoff(v float64) { charMaskCutoff = float32(v) }

// cutoffFor picks the MASK threshold by content type. blendGradients is true for character and
// item content, false for zone/environment content -- the same signal alphaModeFor uses.
func cutoffFor(blendGradients bool) float32 {
	if blendGradients {
		return charMaskCutoff
	}
	return maskCutoff
}

// alphaModeFor returns the glTF alpha mode for a surface. When blendGradients is
// set (character/item content), a MASK gradient (sheer cloth / translucent trim)
// is upgraded to BLEND so its semi-transparent body isn't hard-discarded by the
// alpha-test cutoff (which left only the opaque edges). Zone/environment content
// passes false, keeping foliage cutouts on MASK to avoid colored halos.
func alphaModeFor(s *eqoa.Surface, blendGradients bool) string {
	am := s.AlphaMode()
	if am != "MASK" {
		return am
	}
	tf := s.TranslucentFraction()
	// Character/item sheer cloth: even a faint translucency gradient should BLEND
	// (see the garment-alpha fix).
	if blendGradients && tf >= gradientBlendBar {
		return "BLEND"
	}
	// Predominantly-translucent surfaces (glass, water) must BLEND EVERYWHERE,
	// zones included: MASK's hard 0.5 cutout shatters a smooth alpha gradient into
	// jagged edges (the "glass looks horrible" bug — measured glass ≈ 0.58 mid-
	// alpha band). Foliage/cutout masks are near-binary (measured ≤ 0.21, the vast
	// majority < 0.1), so this high bar leaves them on MASK and avoids the
	// colored-halo regression.
	if tf >= 0.4 {
		return "BLEND"
	}
	return am
}

// JointNodeName is the glTF node name given to hierarchy joint i. The index in
// the name is the joint's index in the source 0x2400 hierarchy — the same value
// an 0x2500 HSpriteAttachments record's NodeIndex holds — so consumers can
// resolve an attachment by name alone. Callers that build or read that mapping
// must go through this function rather than re-spelling the format.
func JointNodeName(i int) string { return fmt.Sprintf("Joint_%d", i) }

func ExportAssetToBuilder(b *Builder, r io.ReadSeeker, asset *eqoa.Asset, order binary.ByteOrder, registry *eqoa.SurfaceRegistry, blendGradients bool) (int, error) {
	return ExportAssetToBuilderWithAppearance(b, r, asset, order, registry, blendGradients, nil)
}

// ExportAssetToBuilderWithAppearance is ExportAssetToBuilder with an optional
// CHARCUST appearance to bake onto the character's materials. When appearance is
// nil the output is identical to ExportAssetToBuilder (byte-for-byte); when set,
// each character body material gets a provisional body-slot guess in its extras
// and — per the chosen armor set / hair / tint — its BaseColor texture/factor is
// overridden. See AppearanceSpec for the (heuristic) slot model.
func ExportAssetToBuilderWithAppearance(b *Builder, r io.ReadSeeker, asset *eqoa.Asset, order binary.ByteOrder, registry *eqoa.SurfaceRegistry, blendGradients bool, appearance *AppearanceSpec) (int, error) {
	rootNodeIdx := b.AddNode(Node{Name: fmt.Sprintf("Sprite_0x%X", asset.ID)})
	// The CSprite's 0x2710 size factor scales the whole sprite. The root node is
	// the parent of the skeleton's root joints and of the rigid meshes, so
	// joints, root motion and skinned vertices all scale with it.
	if sf := asset.SizeFactor; sf > 0 && sf != 1 {
		b.Doc.Nodes[rootNodeIdx].Scale = []float32{sf, sf, sf}
	}

	// Add Skeleton
	var jointNodeIndices []int
	var skinIdx *int
	if asset.Hierarchy != nil {
		firstJointNodeIdx := len(b.Doc.Nodes)
		for i := range asset.Hierarchy.Joints {
			// The 0x2400 hierarchy stores WORLD/model-space bind TRS; glTF nodes
			// need parent-relative transforms.  LocalTRS performs the same
			// world→local conversion the engine does at load (FUN_0041ae00).
			rot, pos, scale := asset.Hierarchy.LocalTRS(i)
			nodeIdx := b.AddNode(Node{
				Name:        JointNodeName(i),
				Translation: pos[:],
				Rotation:    quatNorm(rot),
				Scale:       []float32{scale, scale, scale},
				// Source node identity, carried explicitly so it survives a
				// rename or a re-index by any tool in between: i is the joint's
				// index in the 0x2400 hierarchy, which is exactly the NodeIndex
				// an 0x2500 HSpriteAttachments record refers to (the engine
				// hands out node indices in 0x2400 file order — see
				// ParseHSpriteHierarchy @ 0x0040d168). The Skin's `joints`
				// array is index-aligned with it too, so
				// skin.joints[node_index] resolves to this glTF node.
				Extras: json.RawMessage(fmt.Sprintf(`{"node_index":%d}`, i)),
			})
			jointNodeIndices = append(jointNodeIndices, nodeIdx)
		}
		// Build hierarchy
		for i, j := range asset.Hierarchy.Joints {
			if j.ParentIndex != -1 {
				parentIdx := int(j.ParentIndex) + firstJointNodeIdx
				b.Doc.Nodes[parentIdx].Children = append(b.Doc.Nodes[parentIdx].Children, i+firstJointNodeIdx)
			} else {
				// Attach root joints to the sprite root
				b.Doc.Nodes[rootNodeIdx].Children = append(b.Doc.Nodes[rootNodeIdx].Children, i+firstJointNodeIdx)
			}
		}

		// Calculate IBMs
		ibmData := new(bytes.Buffer)
		globals := asset.Hierarchy.ComputeGlobalTransforms()
		for _, g := range globals {
			ibm := g.Inverse()
			binary.Write(ibmData, binary.LittleEndian, ibm)
		}
		ibmBvIdx := b.AddBufferView(ibmData.Bytes(), 0)
		ibmAccIdx := b.AddAccessor(ibmBvIdx, 0, 5126, len(globals), "MAT4", false)

		// Create Skin — skeleton points to the root joint so three.js can anchor the hierarchy.
		rootJointNodeIdx := firstJointNodeIdx
		sIdx := b.AddSkin(Skin{
			InverseBindMatrices: &ibmAccIdx,
			Joints:              jointNodeIndices,
			Skeleton:            &rootJointNodeIdx,
			Name:                fmt.Sprintf("Skin_0x%X", asset.ID),
		})
		skinIdx = &sIdx
	}

	// Rigid-member props (windmill blades, banner cloth, …): an HSprite whose
	// members are static SimpleSubSprites (no per-vertex skinning) authored in
	// joint-local space, meant to be attached to animated joints — the idle
	// animation places them (e.g. lifts the windmill blades / banner cloth into
	// position; verified: SCENE windmill Joint_3 animates to Y≈25.82, the hub).
	// The engine attaches member[i] to joint[i+1] (joint 0 is the base/root),
	// which we detect by the invariant #joints == #members + 1 with no skinned
	// mesh. Without this, members render frozen at their local origin (blades at
	// ground / cloth hanging below the spar) and the skeleton is orphaned.
	rigidMembers := false
	if asset.Hierarchy != nil && skinIdx != nil && len(jointNodeIndices) == len(asset.Meshes)+1 {
		anySkinned := false
		for _, m := range asset.Meshes {
			if m.Type == 5 {
				anySkinned = true
				break
			}
		}
		rigidMembers = !anySkinned
	}

	surfaceToIndex := make(map[uint32]int)
	surfaceAlphaMode := make(map[uint32]string)
	// Decoded surfaces are kept so a per-race tint can be baked into a copy. The tint is only
	// known at material time, by which point the surface has already been embedded.
	surfaceImages := make(map[uint32]image.Image)
	materialToIndex := make(map[int]int)
	materialHasTexture := make(map[int]bool)

	// Texture wrapping. wrapMode is a two-bit mask on the material layer, not an enum:
	// bit 0 clamps U, bit 1 clamps V. Confirmed against the client -- SetLayerWrapMode packs it
	// into the layer flags and ConstructTextPass unpacks bit 1 into GS CLAMP.WMS and bit 2 into
	// CLAMP.WMT (0 = REPEAT, 1 = CLAMP). Values above 3 are rejected by the engine.
	//
	// This was parsed and then read by nothing, so every texture exported as glTF's default
	// REPEAT and any surface the game clamps tiled instead -- visible as a band of repeated edge
	// pixels where a decal or a sky panel should simply stop.
	//
	// Wrapping is a property of the material layer while textures are keyed by surface DictID, so
	// one image can be referenced at two different wrap settings. glTF expresses that as several
	// Texture objects sharing one Source, which is what the variant map below builds -- and only
	// when needed, so wrapMode 0 keeps its original texture index and the output is unchanged.
	samplerIndex := make(map[int32]int)
	wrapVariant := make(map[[2]int64]int)
	textureFor := func(dictID uint32, wrapMode int32) (int, bool) {
		base, ok := surfaceToIndex[dictID]
		if !ok || wrapMode <= 0 || wrapMode > 3 {
			return base, ok
		}
		key := [2]int64{int64(dictID), int64(wrapMode)}
		if idx, ok := wrapVariant[key]; ok {
			return idx, true
		}
		sIdx, ok := samplerIndex[wrapMode]
		if !ok {
			wrap := func(clamped bool) int {
				if clamped {
					return WrapClampToEdge
				}
				return WrapRepeat
			}
			b.Doc.Samplers = append(b.Doc.Samplers, Sampler{
				WrapS: wrap(wrapMode&1 != 0),
				WrapT: wrap(wrapMode&2 != 0),
			})
			sIdx = len(b.Doc.Samplers) - 1
			samplerIndex[wrapMode] = sIdx
		}
		s := sIdx
		b.Doc.Textures = append(b.Doc.Textures, Texture{
			Source:  b.Doc.Textures[base].Source,
			Sampler: &s,
		})
		idx := len(b.Doc.Textures) - 1
		wrapVariant[key] = idx
		return idx, true
	}

	if asset.MatPalObj != nil {
		var surfaceArray *eqoa.ESFObject
		var materialArray *eqoa.ESFObject
		for _, child := range asset.MatPalObj.Children {
			if child.Header.ObjectType == 0x1001 {
				surfaceArray = child
			} else if child.Header.ObjectType == 0x1101 {
				materialArray = child
			}
		}

		// Pre-pass: collect TexIDs that are actually referenced by materials so we
		// only embed surfaces that end up used. Surfaces in the SurfaceArray that no
		// material references (e.g. AO/shadow maps, unused detail textures) would
		// otherwise appear as near-black phantom textures in the GLB image list.
		neededTexIDs := map[uint32]bool{}
		if materialArray != nil {
			for _, mObj := range materialArray.Children {
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

		if surfaceArray != nil {
			for _, sObj := range surfaceArray.Children {
				body, _ := sObj.ReadBody(r)
				s, err := eqoa.ParseSurface(body, order)
				if err == nil && neededTexIDs[s.DictID] {
					img, err := s.ToImage(0)
					if err == nil {
						tmpBuf := new(bytes.Buffer)
						png.Encode(tmpBuf, img)
						bvIdx := b.AddBufferView(tmpBuf.Bytes(), 0)
						imgIdx := len(b.Doc.Images)
						b.Doc.Images = append(b.Doc.Images, Image{
							BufferView: bvIdx,
							MimeType:   "image/png",
						})
						texIdx := len(b.Doc.Textures)
						b.Doc.Textures = append(b.Doc.Textures, Texture{Source: imgIdx})
						surfaceToIndex[s.DictID] = texIdx
						surfaceAlphaMode[s.DictID] = alphaModeFor(s, blendGradients)
						surfaceImages[s.DictID] = img
					}
				}
			}
		}

		// embedFromRegistry embeds a surface from the cross-file registry into
		// this builder if it isn't already in surfaceToIndex.
		embedFromRegistry := func(dictID uint32) {
			if _, ok := surfaceToIndex[dictID]; ok {
				return
			}
			if registry == nil {
				return
			}
			surf, ok := registry.Get(dictID)
			if !ok {
				return
			}
			img, err := surf.ToImage(0)
			if err != nil {
				return
			}
			tmpBuf := new(bytes.Buffer)
			png.Encode(tmpBuf, img)
			bvIdx := b.AddBufferView(tmpBuf.Bytes(), 0)
			imgIdx := len(b.Doc.Images)
			b.Doc.Images = append(b.Doc.Images, Image{BufferView: bvIdx, MimeType: "image/png"})
			texIdx := len(b.Doc.Textures)
			b.Doc.Textures = append(b.Doc.Textures, Texture{Source: imgIdx})
			surfaceToIndex[dictID] = texIdx
			surfaceAlphaMode[dictID] = alphaModeFor(surf, blendGradients)
			surfaceImages[dictID] = img
		}

		// Bake a material's layer-0 modulate into a copy of its texture.
		//
		// The flesh art is shared: three body textures are used by Human, Elf, Erudite and
		// Barbarian alike, with the per-race skin tone carried in the palette as a modulate
		// (Human and Elf neutral, Erudite 109,83,77, Barbarian 237,219,188). Without applying it
		// the Erudite gets a dark head from its own CHARFACE texture and a pale body from the
		// shared flesh, which is the visible bug.
		//
		// Baking rather than emitting baseColorFactor is also the more faithful operation. The
		// console multiplies texel by modulate in GAMMA space; glTF's baseColorFactor multiplies
		// in LINEAR space, which is why that route needed a c^2.2 correction and still read wrong.
		// A per-texel multiply here is exactly what the hardware does.
		//
		// Keyed by surface AND modulate, so two races sharing a texture at the same tone share
		// one baked copy, and a race using it neutral keeps the original.
		tintCache := make(map[[2]uint64]int)
		tintedTexture := func(dictID uint32, mod [4]float32) (int, bool) {
			base, ok := surfaceToIndex[dictID]
			if !ok {
				return 0, false
			}
			r8 := uint32(mod[0]*255 + 0.5)
			g8 := uint32(mod[1]*255 + 0.5)
			b8 := uint32(mod[2]*255 + 0.5)
			if r8 >= 255 && g8 >= 255 && b8 >= 255 {
				return base, true // neutral: the shared texture is already correct
			}
			key := [2]uint64{uint64(dictID), uint64(r8)<<16 | uint64(g8)<<8 | uint64(b8)}
			if idx, hit := tintCache[key]; hit {
				return idx, true
			}
			src, ok := surfaceImages[dictID]
			if !ok {
				return base, true
			}
			bnds := src.Bounds()
			dst := image.NewNRGBA(bnds)
			for y := bnds.Min.Y; y < bnds.Max.Y; y++ {
				for x := bnds.Min.X; x < bnds.Max.X; x++ {
					cr, cg, cb, ca := src.At(x, y).RGBA() // 16-bit, alpha-premultiplied
					a8 := uint8(ca >> 8)
					un := func(v uint32) uint32 { // undo premultiplication
						if ca == 0 {
							return 0
						}
						return v * 0xffff / ca
					}
					mul := func(v, m uint32) uint8 {
						return uint8(min(255, (un(v)>>8)*m/255))
					}
					dst.Set(x, y, color.NRGBA{mul(cr, r8), mul(cg, g8), mul(cb, b8), a8})
				}
			}
			buf := new(bytes.Buffer)
			if png.Encode(buf, dst) != nil {
				return base, true
			}
			bvIdx := b.AddBufferView(buf.Bytes(), 0)
			imgIdx := len(b.Doc.Images)
			b.Doc.Images = append(b.Doc.Images, Image{BufferView: bvIdx, MimeType: "image/png"})
			idx := len(b.Doc.Textures)
			b.Doc.Textures = append(b.Doc.Textures, Texture{Source: imgIdx})
			tintCache[key] = idx
			return idx, true
		}

		// Opt-in CHARCUST appearance: pre-embed the chosen armor-slot and hair
		// textures once so material overrides below can reference stable indices.
		// Only runs when a spec is supplied (default export path is untouched).
		var armorTexIdx [5]int
		hairTexIdx := -1
		for i := range armorTexIdx {
			armorTexIdx[i] = -1
		}
		if appearance != nil {
			for slot := 0; slot < 5; slot++ {
				if img := appearance.ArmorTextures[slot]; img != nil {
					armorTexIdx[slot] = b.AddImageTexture(img)
				}
			}
			if appearance.HairTexture != nil {
				hairTexIdx = b.AddImageTexture(appearance.HairTexture)
			}
		}

		if materialArray != nil {
			for i, mObj := range materialArray.Children {
				body, _ := mObj.ReadBody(r)
				m, err := eqoa.ParseMaterialBody(body, mObj.Header.ObjectVersion, order)
				if err == nil {
					// Pull any cross-file textures referenced by this material.
					if len(m.Layers) > 0 {
						embedFromRegistry(m.Layers[0].TexID)
					}
					alphaMode := "OPAQUE"
					if len(m.Layers) > 0 {
						if mode, ok := surfaceAlphaMode[m.Layers[0].TexID]; ok {
							alphaMode = mode
						}
					}
					// Record what the source material states, including the parts glTF has no
					// way to represent. Layers beyond the first are the notable case: their
					// artwork is embedded in the GLB but nothing references it, so without this
					// it is unreachable bytes.
					ex := matExtras{LayerCount: len(m.Layers)}
					if len(m.Layers) > 0 {
						ex.BlendMode = int(m.Layers[0].BlendMode)
						ex.WrapMode = int(m.Layers[0].WrapMode)
						// Layer 0's modulate, 0-255: the per-race skin tone. Recorded, not applied.
						c0 := m.Layers[0].Color
						ex.LayerColor = [4]float32{c0[0] * 255, c0[1] * 255, c0[2] * 255, c0[3] * 255}
						for _, l := range m.Layers[1:] {
							ex.ExtraLayers = append(ex.ExtraLayers, extraLayer{
								TexID:     fmt.Sprintf("0x%X", l.TexID),
								BlendMode: int(l.BlendMode),
								WrapMode:  int(l.WrapMode),
								Color:     l.Color,
							})
						}
					}

					// OFF BY DEFAULT -- see SetBlendFromMaterial.
					if emitBlendFromMaterial && len(m.Layers) > 0 && m.Layers[0].BlendMode != 0 {
						alphaMode = "BLEND"
					}

					hasTexture := false
					gm := Material{
						Name:        fmt.Sprintf("Material_0x%X", m.DictID),
						AlphaMode:   alphaMode,
						DoubleSided: true,
						PBRMetallicRoughness: &PBR{
							MetallicFactor:  0,
							RoughnessFactor: 1,
						},
					}
					if alphaMode == "MASK" {
						cutoff := cutoffFor(blendGradients)
						gm.AlphaCutoff = &cutoff
					}
					if len(m.Layers) > 0 {
						if texIdx, ok := textureFor(m.Layers[0].TexID, m.Layers[0].WrapMode); ok {
							// Bake the per-race skin tone into a copy of the shared flesh texture.
							if bakeSkinTint {
								if ti, ok2 := tintedTexture(m.Layers[0].TexID, m.Layers[0].Color); ok2 {
									texIdx = ti
								}
							}
							gm.PBRMetallicRoughness.BaseColorTexture = &TextureInfo{Index: texIdx}
							hasTexture = true
						}
						// The layer's modulate colour: how the client gives each race its skin
						// tone. Flesh textures are shared between races, and the material
						// palette in char.esf carries the tint -- an Erudite is (109,83,77) on
						// the same four surfaces a Human wears at (255,255,255), Barbarians are
						// (237,219,188). Dark Elves, Gnomes, Dwarves and Trolls have their own
						// textures and are neutral. Parsed all along and never emitted, which is
						// why exported Erudites had a correct dark face on a pale body.
						//
						// OFF BY DEFAULT (--skin-modulate). Emitting this changed how models
						// looked in ways that went beyond the intended body tint: reviewed
						// against the real screens, face textures on several races came out
						// wrong-coloured and poorly fitted to the head, and the Erudite body
						// picked up a specular sheen the console never had -- an artifact of
						// giving a PBR material a very dark albedo, which leaves the constant
						// dielectric highlight dominating what should be flat, unlit shading.
						//
						// The finding itself is solid: the modulate exists, it is the per-race
						// skin tone, and it was being discarded. What is not settled is how to
						// express it in glTF. So it is available to experiment with and does not
						// affect anyone who does not ask for it.
						//
						// Only CHROMATIC values are applied even then. Head materials -- face,
						// eyes, hair -- carry flat greys such as (191,191,191), and emitting
						// those darkened faces that were already correct. Skin tones observed so
						// far are all chromatic; note Dark Elf skin is grey but comes from its
						// own textures with no modulate at all, so it is not a counter-example.
						c := m.Layers[0].Color
						chromatic := c[0] != c[1] || c[1] != c[2]
						if emitSkinModulate && chromatic && c != [4]float32{0, 0, 0, 0} {
							// Converted to linear before emitting. The console multiplies the
							// texel in gamma space, whereas glTF's baseColorFactor multiplies in
							// linear space -- so passing 109/255 = 0.427 straight through lands
							// far too light. Reproducing a gamma-space multiply k needs a linear
							// factor of k^2.2, which is exactly the sRGB transfer function.
							// Alpha is a coverage value, not a colour, and is not converted.
							gm.PBRMetallicRoughness.BaseColorFactor = []float32{
								srgbToLinear(c[0]), srgbToLinear(c[1]), srgbToLinear(c[2]), c[3],
							}
						}
					}
					if !hasTexture {
						if len(m.Layers) == 0 {
							// Empty material (no layers, no texture): a runtime-tinted
							// shell such as the spirit "aura" envelope — the game applies
							// its colour + blend per situation (e.g. red aura over red
							// bones), so nothing is baked. Export as a translucent shell
							// (neutral low-alpha) rather than opaque grey, so it doesn't
							// occlude the inner mesh (the bones show through) and can be
							// re-tinted downstream.
							gm.AlphaMode = "BLEND"
							gm.PBRMetallicRoughness.BaseColorFactor = []float32{1.0, 1.0, 1.0, 0.25}
						} else {
							// Material references a texture we couldn't resolve: mid-grey
							// placeholder so it's visible instead of pure black.
							gm.PBRMetallicRoughness.BaseColorFactor = []float32{0.65, 0.65, 0.65, 1.0}
						}
					}
					// CHARCUST appearance override (opt-in). Provisional slot model
					// (see AppearanceSpec): material palette index i → body slot
					// i mod 5; the head/hair slot is heuristically material 0.
					// Apply the chosen armor-set texture per slot, hair to the head
					// material, and the skin tint as BaseColorFactor on bare-skin
					// materials (armorSet 0, or any material we couldn't skin). This
					// is best-effort and meant to be visually spot-checked.
					if appearance != nil {
						slot := i % 5
						hairSlot := i == 0 && hairTexIdx >= 0
						bare := false
						if hairSlot {
							gm.PBRMetallicRoughness.BaseColorTexture = &TextureInfo{Index: hairTexIdx}
							gm.AlphaMode = "MASK"
							cutoff := cutoffFor(blendGradients)
							gm.AlphaCutoff = &cutoff
						} else if appearance.ArmorSet != 0 && armorTexIdx[slot] >= 0 {
							gm.PBRMetallicRoughness.BaseColorTexture = &TextureInfo{Index: armorTexIdx[slot]}
						} else {
							// Bare-skin slot: tint the base color (skin/robe color).
							bare = true
							gm.PBRMetallicRoughness.BaseColorFactor = []float32{
								float32(appearance.Tint[0]) / 255.0,
								float32(appearance.Tint[1]) / 255.0,
								float32(appearance.Tint[2]) / 255.0,
								float32(appearance.Tint[3]) / 255.0,
							}
						}
						mi, sg := i, slot
						ex.MatIndex = &mi
						ex.SlotGuess = &sg
						ex.HairSlot = hairSlot
						ex.Bare = bare
					}
					gm.Extras = mustJSON(ex)

					matIdx := len(b.Doc.Materials)
					b.Doc.Materials = append(b.Doc.Materials, gm)
					materialToIndex[i] = matIdx
					materialHasTexture[matIdx] = hasTexture
				}
			}
		}
	}

	for i, mesh := range asset.Meshes {
		gMesh := Mesh{Name: fmt.Sprintf("Mesh_%d", i)}
		for _, fg := range mesh.FaceGroups {
			prim := Primitive{
				Attributes: make(map[string]int),
			}
			if realMatIdx, ok := materialToIndex[int(fg.MaterialIndex)]; ok {
				prim.Material = ptrInt(realMatIdx)
			}

			// POSITION
			posData := new(bytes.Buffer)
			var minPos, maxPos [3]float32
			for j, v := range fg.Vertices {
				binary.Write(posData, binary.LittleEndian, v.Pos)
				if j == 0 {
					minPos, maxPos = v.Pos, v.Pos
				} else {
					for k := 0; k < 3; k++ {
						if v.Pos[k] < minPos[k] {
							minPos[k] = v.Pos[k]
						}
						if v.Pos[k] > maxPos[k] {
							maxPos[k] = v.Pos[k]
						}
					}
				}
			}
			bvIdx := b.AddBufferView(posData.Bytes(), 34962)
			accIdx := b.AddAccessor(bvIdx, 0, 5126, len(fg.Vertices), "VEC3", false)
			b.Doc.Accessors[accIdx].Min = minPos[:]
			b.Doc.Accessors[accIdx].Max = maxPos[:]
			prim.Attributes["POSITION"] = accIdx

			// TEXCOORD_0
			uvData := new(bytes.Buffer)
			for _, v := range fg.Vertices {
				binary.Write(uvData, binary.LittleEndian, v.UV)
			}
			bvIdx = b.AddBufferView(uvData.Bytes(), 34962)
			prim.Attributes["TEXCOORD_0"] = b.AddAccessor(bvIdx, 0, 5126, len(fg.Vertices), "VEC2", false)

			// COLOR_0 — skip when all RGB channels are zero.
			// PS2 static-mesh vertex colors are (0,0,0,alpha): designed as a
			// texture multiplier in the GS pipeline. Emitting them in PBR causes
			// everything to render black in viewers that lack the embedded texture.
			hasNonZeroRGB := false
			for _, v := range fg.Vertices {
				if v.Color[0] > 0 || v.Color[1] > 0 || v.Color[2] > 0 {
					hasNonZeroRGB = true
					break
				}
			}
			if hasNonZeroRGB {
				colorData := new(bytes.Buffer)
				for _, v := range fg.Vertices {
					binary.Write(colorData, binary.LittleEndian, v.Color)
				}
				bvIdx = b.AddBufferView(colorData.Bytes(), 34962)
				prim.Attributes["COLOR_0"] = b.AddAccessor(bvIdx, 0, 5126, len(fg.Vertices), "VEC4", false)
			}

			// NORMAL — normalize each vector; substitute (0,1,0) for degenerate/NaN.
			normData := new(bytes.Buffer)
			for _, v := range fg.Vertices {
				binary.Write(normData, binary.LittleEndian, normalNorm(v.Normal))
			}
			bvIdx = b.AddBufferView(normData.Bytes(), 34962)
			prim.Attributes["NORMAL"] = b.AddAccessor(bvIdx, 0, 5126, len(fg.Vertices), "VEC3", false)

			// Only emit JOINTS_0/WEIGHTS_0 when a Skin exists in this file.
			// SkinSubSprite assets have weighted vertices but no hierarchy — without
			// a Skin to reference, the joint indices are meaningless and would
			// trigger NODE_SKINNED_MESH_WITHOUT_SKIN.
			if mesh.Type == 5 && skinIdx != nil {
				numJoints := len(asset.Hierarchy.Joints) // safe: skinIdx != nil implies Hierarchy != nil

				jointData := new(bytes.Buffer)
				weightData := new(bytes.Buffer)
				for _, v := range fg.Vertices {
					// Normalize weights first so we know which slots are truly zero.
					var fw [4]float32
					for k := 0; k < 4; k++ {
						fw[k] = float32(v.Weights[k]) / 255.0
					}
					sum := fw[0] + fw[1] + fw[2] + fw[3]
					if sum < 1e-6 {
						fw = [4]float32{1, 0, 0, 0}
					} else {
						inv := float32(1.0 / float64(sum))
						for k := 0; k < 4; k++ {
							fw[k] *= inv
						}
					}

					// Clamp OOB indices and zero any slot whose weight is zero.
					// glTF spec requires JOINTS_0[i] == 0 when WEIGHTS_0[i] == 0.
					clamped := v.Joints
					for k := 0; k < 4; k++ {
						if fw[k] == 0 || int(clamped[k]) >= numJoints {
							clamped[k] = 0
						}
					}

					binary.Write(jointData, binary.LittleEndian, clamped)
					binary.Write(weightData, binary.LittleEndian, fw)
				}
				bvIdx = b.AddBufferView(jointData.Bytes(), 34962)
				prim.Attributes["JOINTS_0"] = b.AddAccessor(bvIdx, 0, 5121, len(fg.Vertices), "VEC4", false)
				bvIdx = b.AddBufferView(weightData.Bytes(), 34962)
				prim.Attributes["WEIGHTS_0"] = b.AddAccessor(bvIdx, 0, 5126, len(fg.Vertices), "VEC4", false)
			}

			// INDICES
			idxData := new(bytes.Buffer)
			for _, idx := range fg.Indices {
				binary.Write(idxData, binary.LittleEndian, uint32(idx))
			}
			bvIdx = b.AddBufferView(idxData.Bytes(), 34963)
			prim.Indices = ptrInt(b.AddAccessor(bvIdx, 0, 5125, len(fg.Indices), "SCALAR", false))

			gMesh.Primitives = append(gMesh.Primitives, prim)
		}

		if len(gMesh.Primitives) == 0 {
			continue
		}
		mIdx := b.AddMesh(gMesh)
		// Attach a skin only when this mesh carries joint/weight data AND a Skin
		// exists in this file (mesh.Type==5 && skinIdx!=nil).
		var nodeSkin *int
		if mesh.Type == 5 && skinIdx != nil {
			nodeSkin = skinIdx
		}
		meshNodeIdx := b.AddNode(Node{
			Mesh: &mIdx,
			Skin: nodeSkin,
			Name: fmt.Sprintf("MeshNode_%d", i),
		})
		// Skinned mesh nodes must be scene-level roots — a parent transform on
		// the sprite grouping node would corrupt the skinned result
		// (NODE_SKINNED_MESH_NON_ROOT).
		switch {
		case nodeSkin != nil:
			b.AddSceneNode(meshNodeIdx)
		case rigidMembers && i+1 < len(jointNodeIndices):
			// Rigid prop member: parent under joint[i+1] so the joint's bind
			// transform + idle animation position and animate it (windmill
			// blades / banner cloth). See rigidMembers above.
			jn := jointNodeIndices[i+1]
			b.Doc.Nodes[jn].Children = append(b.Doc.Nodes[jn].Children, meshNodeIdx)
		default:
			// Non-skinned nodes stay under the sprite root for grouping.
			b.Doc.Nodes[rootNodeIdx].Children = append(b.Doc.Nodes[rootNodeIdx].Children, meshNodeIdx)
		}
	}

	// Export animations
	if len(asset.Actions) > 0 && len(jointNodeIndices) > 0 {
		exportAnimations(b, asset, jointNodeIndices)
	}

	return rootNodeIdx, nil
}

// animStateNames maps an animation id (the 0x2910 AnimID, which is the
// AnimationState byte ID the EQOA server sends) to a human-readable name.
// Populated at startup by the cmd package from the version-controlled
// cmd/animation_names.json — edit the JSON, not this file.
var animStateNames = map[int]string{}

// SetAnimationNames installs the animation id → name table used when naming
// exported glTF animations.
func SetAnimationNames(names map[int]string) {
	animStateNames = names
}

// clipItem is one exported ActionSet: its index in asset.Actions, its DictID
// and whether its channels include the root joint (the lower half).
type clipItem struct {
	ai           int
	dictID       uint32
	includesRoot bool
}

func (it clipItem) half() string {
	if it.includesRoot {
		return "lower"
	}
	return "upper"
}

// clipGroup is one merged full-body clip.
type clipGroup struct {
	animID  int  // the 0x2910 AnimID; meaningless when !mapped
	mapped  bool // false: no 0x2910 record names these ActionSets
	members []clipItem
	owned   []clipItem // members whose layer clip belongs to this group
	name    string
}

// clipPlan is how a sprite's ActionSets group into clips, and every name.
type clipPlan struct {
	groups    []clipGroup
	groupOf   map[int]int    // ai -> index in groups
	layerName map[int]string // ai -> layer clip name
}

// planClips groups the exported ActionSets into merged clips and names every
// clip from the sprite's 0x2910 animation table.
//
// With a table, each 0x2910 record is one merged clip: the ActionSets whose
// DictID is its ActionID or Action2ID (the two halves the record plays
// together), named by its AnimID. Records are taken in table order; an
// ActionSet named by more than one record (ids that reuse a partner's clips)
// is owned by the first, and a record whose sets are all owned already adds
// no clip, since it would repeat the owner's data. ActionSets no record names,
// and every ActionSet of a sprite without a table, are unmapped: they are
// labeled "Unmapped" with their raw index and never get a name. Without a
// table the old consecutive pairing (ai/2) still groups them, so props keep
// their merged clips; with a table an unnamed set stands alone.
func planClips(items []clipItem, table []eqoa.CSpriteAnimation) clipPlan {
	p := clipPlan{groupOf: map[int]int{}, layerName: map[int]string{}}
	if len(table) > 0 {
		byDict := map[uint32][]clipItem{}
		for _, it := range items {
			byDict[it.dictID] = append(byDict[it.dictID], it)
		}
		for _, rec := range table {
			var members []clipItem
			inMembers := map[int]bool{}
			for _, id := range []uint32{rec.ActionID, rec.Action2ID} {
				if id == 0 {
					continue
				}
				for _, it := range byDict[id] {
					if !inMembers[it.ai] {
						inMembers[it.ai] = true
						members = append(members, it)
					}
				}
			}
			var owned []clipItem
			for _, it := range members {
				if _, taken := p.groupOf[it.ai]; !taken {
					owned = append(owned, it)
				}
			}
			if len(owned) == 0 {
				continue
			}
			animID := int(rec.AnimID)
			name, ok := animStateNames[animID]
			if !ok {
				name = "Unknown"
			}
			label := fmt.Sprintf("0x%02X_%s", animID, name)
			gi := len(p.groups)
			p.groups = append(p.groups, clipGroup{
				animID: animID, mapped: true, members: members, owned: owned,
				name: fmt.Sprintf("%s_0x%X", label, members[0].dictID),
			})
			for _, it := range owned {
				p.groupOf[it.ai] = gi
				p.layerName[it.ai] = fmt.Sprintf("%s_%s_0x%X", label, it.half(), it.dictID)
			}
		}
	}
	noTable := len(table) == 0
	unmappedPair := map[int]int{}
	for _, it := range items {
		if _, taken := p.groupOf[it.ai]; taken {
			continue
		}
		gi, ok := -1, false
		if noTable {
			gi, ok = unmappedPair[it.ai/2]
		}
		if !ok {
			label := fmt.Sprintf("Unmapped_action%d", it.ai)
			if noTable {
				label = fmt.Sprintf("Unmapped_pair%d", it.ai/2)
			}
			gi = len(p.groups)
			p.groups = append(p.groups, clipGroup{name: fmt.Sprintf("%s_0x%X", label, it.dictID)})
			if noTable {
				unmappedPair[it.ai/2] = gi
			}
		}
		p.groups[gi].members = append(p.groups[gi].members, it)
		p.groups[gi].owned = append(p.groups[gi].owned, it)
		p.groupOf[it.ai] = gi
		p.layerName[it.ai] = fmt.Sprintf("Unmapped_action%d_%s_0x%X", it.ai, it.half(), it.dictID)
	}
	return p
}

// animChanSpec references the document-global accessors of one animated joint
// (shared time input, rotation + translation outputs) so the same keyframe data
// can be emitted into both the per-half "layer" clip and the merged full-body
// clip without duplicating any buffer bytes.
type animChanSpec struct {
	timeAcc, rotAcc, posAcc int
	node                    int
}

// buildAnimation assembles a glTF Animation from channel specs: two channels
// (rotation, translation) per joint, each with its own sampler referencing the
// shared accessors. A joint is emitted only once (first spec wins) so that
// merging upper+lower layers — or a non-injective BoneMap — can never produce
// two channels targeting the same node+path, which glTF forbids.
func buildAnimation(name string, specs []animChanSpec) Animation {
	anim := Animation{Name: name}
	seen := make(map[int]bool, len(specs))
	for _, s := range specs {
		if seen[s.node] {
			continue
		}
		seen[s.node] = true
		rotSampler := len(anim.Samplers)
		anim.Samplers = append(anim.Samplers, AnimationSampler{
			Input: s.timeAcc, Output: s.rotAcc, Interpolation: "LINEAR",
		})
		anim.Channels = append(anim.Channels, AnimationChannel{
			Sampler: rotSampler,
			Target:  AnimationChannelTarget{Node: s.node, Path: "rotation"},
		})
		posSampler := len(anim.Samplers)
		anim.Samplers = append(anim.Samplers, AnimationSampler{
			Input: s.timeAcc, Output: s.posAcc, Interpolation: "LINEAR",
		})
		anim.Channels = append(anim.Channels, AnimationChannel{
			Sampler: posSampler,
			Target:  AnimationChannelTarget{Node: s.node, Path: "translation"},
		})
	}
	return anim
}

// exportAnimations writes each ActionSet as a glTF animation with per-bone
// rotation and translation tracks.
//
// Channel→joint mapping: each ActionChannel carries a BoneID that resolves to
// a joint index through the sprite's BoneMap (ESF object 0x5000).  This mirrors
// the engine exactly (Ghidra FUN_0041b6d8): channels whose BoneID is absent
// from the map are skipped — ActionSets are shared across skeletons and only
// the channels a skeleton knows about get bound.  If the asset has no BoneMap,
// channels fall back to sequential joint order (best effort for standalone
// dumps that lost their 0x5000 sibling).
//
// Keyframe semantics (Ghidra FUN_0041dd98, the pose evaluator): each frame's
// rotation, scale and position REPLACE the joint's local TRS — exactly glTF
// animation-channel semantics, so both rotation and translation are exported
// directly.  Joints without a channel keep their bind local TRS (the engine
// copies the stored default from jointState+0xd0), which glTF matches by
// leaving the node untouched.
//
// Scale is always 1.0 after int16 dequantization and is not exported.
//
// EQOA stores each action as two partial-body layers — an "upper" ActionSet
// (torso/arms/head) and a "lower" one (legs/root) — that the engine composites
// at runtime.  A glTF viewer plays one clip at a time, so a lone layer leaves
// half the skeleton at bind pose (the "one arm/leg stuck in T-pose" seen on
// giants).  Worse, some layers carry a large counter-roll meant to be canceled
// by their complement — female idles lean ~10° when the _upper layer plays
// alone but sit upright once composited — so a viewer auto-playing a lone layer
// shows a badly tilted pose.  We therefore export both the individual layers AND
// a merged full-body clip per action (the layers target disjoint joints, so the
// merge is a lossless union of channels that reuses the same accessors — no
// extra buffer bytes).  The merged clips are emitted FIRST so a viewer that
// auto-plays animation[0] defaults to the correct composited pose; the layers
// follow for anyone compositing by hand.  Which two ActionSets form a pair, and
// each clip's name, come from the sprite's 0x2910 animation table (planClips).
func exportAnimations(b *Builder, asset *eqoa.Asset, jointNodeIndices []int) {
	// Each exported ActionSet's channel specs, by its index in asset.Actions.
	// planClips (below) decides which ActionSets make up each merged clip.
	// Layer clips are emitted only for clips with TWO or more contributing
	// layers. When one half is empty (e.g. a prop whose action has only a
	// "lower" layer — clockwork gears, banners), the merged clip is
	// byte-identical to the lone layer, and emitting both leaves two identical
	// animations targeting the same joints. A viewer that plays every clip in a
	// zone GLB then applies both, compounding each member's rotate-about-center
	// translation (T = C − R·C) and flinging it off its axle — the "warped
	// gear/clock-hand" artifact. Skipping the redundant duplicate is safe: the
	// merged clip already carries it.
	var items []clipItem
	specsOf := map[int][]animChanSpec{}
	// Joints whose default (not-playing) pose has already been set from frame 0.
	posed := map[int]bool{}

	for ai, aSet := range asset.Actions {
		nFrames := int(aSet.NumFrames)
		if len(aSet.Channels) == 0 || nFrames <= 0 {
			continue
		}

		// Build time accessor: [0, dt, 2*dt, ...] — shared across all channels.
		// Effective rate = FPS × TimeScale: for v0 ActionSets FPS=1.0 and TimeScale
		// encodes the actual frame rate; for v1+ FPS is frames/tick and TimeScale
		// is ticks/second.  Together they always yield the correct wall-clock dt.
		effectiveFPS := aSet.FPS * aSet.TimeScale
		if effectiveFPS <= 0 {
			effectiveFPS = 1.0
		}
		dt := float32(1.0 / effectiveFPS)
		timeBuf := new(bytes.Buffer)
		var maxTime float32
		for fi := 0; fi < nFrames; fi++ {
			t := float32(fi) * dt
			binary.Write(timeBuf, binary.LittleEndian, t)
			if t > maxTime {
				maxTime = t
			}
		}
		timeBvIdx := b.AddBufferView(timeBuf.Bytes(), 0)
		timeAccIdx := b.AddAccessor(timeBvIdx, 0, 5126, nFrames, "SCALAR", false)
		b.Doc.Accessors[timeAccIdx].Min = []float32{0}
		b.Doc.Accessors[timeAccIdx].Max = []float32{maxTime}

		var specs []animChanSpec
		includesRoot := false

		for chIdx := range aSet.Channels {
			ch := &aSet.Channels[chIdx]

			// Resolve BoneID → joint index via the sprite's 0x5000 BoneMap.
			var jointIdx int
			if asset.BoneMap != nil {
				ji, ok := asset.BoneMap[ch.BoneID]
				if !ok {
					continue // channel targets a bone this skeleton doesn't have
				}
				jointIdx = int(ji)
			} else {
				jointIdx = chIdx
			}
			if jointIdx < 0 || jointIdx >= len(jointNodeIndices) {
				continue
			}
			if asset.Hierarchy != nil && asset.Hierarchy.Joints[jointIdx].ParentIndex == -1 {
				includesRoot = true
			}
			nodeIdx := jointNodeIndices[jointIdx]

			// Bake frame 0 into the joint's DEFAULT (not-playing) pose. EQOA idle
			// clips carry member placement in their keyframes — the bind pose has
			// members unplaced (a banner's mount joint is at origin; frame 0 lifts
			// it onto the wall). A glTF viewer plays one clip at a time, so in a
			// multi-animation zone every other animated prop would otherwise show
			// its unplaced bind pose (banners/windmill parts hanging below the
			// structure). The animation still overrides this when it plays.
			if !posed[nodeIdx] && len(ch.Frames) > 0 {
				posed[nodeIdx] = true
				q := quatNorm(ch.Frames[0].Rotation)
				b.Doc.Nodes[nodeIdx].Rotation = q
				p := ch.Frames[0].Position
				b.Doc.Nodes[nodeIdx].Translation = []float32{p[0], p[1], p[2]}
			}

			// Rotation (VEC4 quaternion XYZW, normalized) + translation outputs.
			rotBuf := new(bytes.Buffer)
			posBuf := new(bytes.Buffer)
			for fi := 0; fi < nFrames && fi < len(ch.Frames); fi++ {
				q := quatNorm(ch.Frames[fi].Rotation)
				binary.Write(rotBuf, binary.LittleEndian, [4]float32{q[0], q[1], q[2], q[3]})
				binary.Write(posBuf, binary.LittleEndian, ch.Frames[fi].Position)
			}
			rotBvIdx := b.AddBufferView(rotBuf.Bytes(), 0)
			rotAccIdx := b.AddAccessor(rotBvIdx, 0, 5126, nFrames, "VEC4", false)
			// Translation — anim frames carry the joint's full local position
			// (replaces bind translation, per FUN_0041dd98).
			posBvIdx := b.AddBufferView(posBuf.Bytes(), 0)
			posAccIdx := b.AddAccessor(posBvIdx, 0, 5126, nFrames, "VEC3", false)

			specs = append(specs, animChanSpec{
				timeAcc: timeAccIdx, rotAcc: rotAccIdx, posAcc: posAccIdx, node: nodeIdx,
			})
		}

		if len(specs) == 0 {
			continue
		}
		items = append(items, clipItem{ai: ai, dictID: aSet.DictID, includesRoot: includesRoot})
		specsOf[ai] = specs
	}

	// Merged full-body clips first (so animation[0] is a correct composited
	// pose), then the individual upper/lower layers in action order — but only
	// for clips that genuinely have two layers, so a single-layer action isn't
	// duplicated.
	plan := planClips(items, asset.AnimTable)
	for _, g := range plan.groups {
		var specs []animChanSpec
		for _, it := range g.members {
			specs = append(specs, specsOf[it.ai]...)
		}
		b.Doc.Animations = append(b.Doc.Animations, buildAnimation(g.name, specs))
	}
	for _, it := range items {
		if len(plan.groups[plan.groupOf[it.ai]].members) >= 2 {
			b.Doc.Animations = append(b.Doc.Animations, buildAnimation(plan.layerName[it.ai], specsOf[it.ai]))
		}
	}
}

func ptrInt(i int) *int { return &i }

// srgbToLinear converts an sRGB-encoded component to linear light.
//
// Needed because the two pipelines multiply in different spaces: the console applies a material
// modulate to the texel in gamma space, while glTF's baseColorFactor multiplies after the
// texture has been decoded to linear. A gamma-space multiply by k is equivalent to a linear
// multiply by k^2.2, which is what this curve gives.
func srgbToLinear(c float32) float32 {
	if c <= 0.04045 {
		return c / 12.92
	}
	return float32(math.Pow(float64((c+0.055)/1.055), 2.4))
}

// emitSkinModulate controls whether a material layer's RGBA modulate is written as
// baseColorFactor. Default off: see the comment at the call site. Set by --skin-modulate.
var emitSkinModulate = false

// bakeSkinTint multiplies a material's layer-0 modulate into a COPY of its texture, rather than
// emitting it as baseColorFactor.
//
// This is the per-race skin tone. Three body textures are shared by Human, Elf, Erudite and
// Barbarian, with the tone carried in the palette -- so without it the Erudite has a dark head
// (its own CHARFACE texture) and a pale body (shared neutral flesh).
//
// Preferred over emitSkinModulate because it matches the hardware: the console multiplies texel
// by modulate in gamma space, while baseColorFactor multiplies in linear space -- which is why
// that route needed a c^2.2 correction and still looked wrong. It also leaves material.color at
// white, so nothing interacts with scene lighting or shows as a sheen.
//
// Costs one extra embedded image per (surface, tone) pair actually used.
var bakeSkinTint = true

// SetBakeSkinTint toggles baking the layer-0 modulate into a texture copy. See bakeSkinTint.
func SetBakeSkinTint(on bool) { bakeSkinTint = on }

// SetSkinModulate enables emitting per-race skin modulate colours.
func SetSkinModulate(on bool) { emitSkinModulate = on }
