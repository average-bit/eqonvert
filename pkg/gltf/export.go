package gltf

import (
	"bytes"
	"encoding/binary"
	"fmt"
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
	if blendGradients && tf >= 0.05 {
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

func ExportAssetToBuilder(b *Builder, r io.ReadSeeker, asset *eqoa.Asset, order binary.ByteOrder, registry *eqoa.SurfaceRegistry, blendGradients bool) (int, error) {
	rootNodeIdx := b.AddNode(Node{Name: fmt.Sprintf("Sprite_0x%X", asset.ID)})

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
				Name:        fmt.Sprintf("Joint_%d", i),
				Translation: pos[:],
				Rotation:    quatNorm(rot),
				Scale:       []float32{scale, scale, scale},
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
	materialToIndex := make(map[int]int)
	materialHasTexture := make(map[int]bool)

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
						cutoff := float32(0.5)
						gm.AlphaCutoff = &cutoff
					}
					if len(m.Layers) > 0 {
						if texIdx, ok := surfaceToIndex[m.Layers[0].TexID]; ok {
							gm.PBRMetallicRoughness.BaseColorTexture = &TextureInfo{Index: texIdx}
							hasTexture = true
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

// animStateNames maps the logical animation pair index (== the AnimationState
// byte ID the EQOA server sends) to a human-readable name.  Populated at
// startup by the cmd package from the version-controlled
// cmd/animation_names.json — edit the JSON, not this file.
var animStateNames = map[int]string{}

// SetAnimationNames installs the pair-index → name table used when naming
// exported glTF animations.
func SetAnimationNames(names map[int]string) {
	animStateNames = names
}

// animationName builds the glTF animation name: the AnimationState ID and
// name when known, the body half (the half whose channels include the root
// joint drives the legs/lower body), and the DictID for traceability.
func animationName(ai int, dictID uint32, includesRoot bool) string {
	pairIdx := ai / 2
	part := "upper"
	if includesRoot {
		part = "lower"
	}
	if name, ok := animStateNames[pairIdx]; ok {
		return fmt.Sprintf("0x%02X_%s_%s_0x%X", pairIdx, name, part, dictID)
	}
	return fmt.Sprintf("0x%02X_Unknown_%s_0x%X", pairIdx, part, dictID)
}

// mergedAnimationName builds the name for the combined full-body clip (upper +
// lower merged) — the action name without a body-half suffix.
func mergedAnimationName(pairIdx int, dictID uint32) string {
	if name, ok := animStateNames[pairIdx]; ok {
		return fmt.Sprintf("0x%02X_%s_0x%X", pairIdx, name, dictID)
	}
	return fmt.Sprintf("0x%02X_Unknown_0x%X", pairIdx, dictID)
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
// follow for anyone compositing by hand.  Actions arrive as consecutive pairs
// (pairIdx = ai/2).
func exportAnimations(b *Builder, asset *eqoa.Asset, jointNodeIndices []int) {
	// Per-pair accumulator for the merged full-body clips. Layer clips are held
	// aside and appended after the merged clips so the merged ones come first.
	pairSpecs := map[int][]animChanSpec{}
	pairName := map[int]string{}
	var pairOrder []int
	// Layer clips are held with their pair index and emitted after the merged
	// clips — but only for pairs that actually have TWO contributing layers. When
	// one half of a pair is empty (e.g. a prop whose action has only a "lower"
	// layer — clockwork gears, banners), the merged clip is byte-identical to the
	// lone layer, and emitting both leaves two identical animations targeting the
	// same joints. A viewer that plays every clip in a zone GLB then applies both,
	// compounding each member's rotate-about-center translation (T = C − R·C) and
	// flinging it off its axle — the "warped gear/clock-hand" artifact. Skipping
	// the redundant duplicate is safe: the merged clip already carries it.
	type layerClip struct {
		pair int
		anim Animation
	}
	var layerClips []layerClip
	pairLayers := map[int]int{}
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
		// Accumulate the pair's channels for the merged full-body clip.
		pairIdx := ai / 2
		if _, seen := pairSpecs[pairIdx]; !seen {
			pairOrder = append(pairOrder, pairIdx)
			pairName[pairIdx] = mergedAnimationName(pairIdx, aSet.DictID)
		}
		pairSpecs[pairIdx] = append(pairSpecs[pairIdx], specs...)
		pairLayers[pairIdx]++

		// Hold this half as a standalone "layer" clip (appended after merged, and
		// only if its pair ends up with two real layers — see pairLayers below).
		layerClips = append(layerClips, layerClip{
			pair: pairIdx,
			anim: buildAnimation(animationName(ai, aSet.DictID, includesRoot), specs),
		})
	}

	// Merged full-body clips first (so animation[0] is a correct composited
	// pose), then the individual upper/lower layers — but only for pairs that
	// genuinely have two layers, so a single-layer action isn't duplicated.
	for _, pi := range pairOrder {
		b.Doc.Animations = append(b.Doc.Animations,
			buildAnimation(pairName[pi], pairSpecs[pi]))
	}
	for _, lc := range layerClips {
		if pairLayers[lc.pair] >= 2 {
			b.Doc.Animations = append(b.Doc.Animations, lc.anim)
		}
	}
}

func ptrInt(i int) *int { return &i }
