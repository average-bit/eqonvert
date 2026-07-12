package gltf

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"

	"github.com/average-bit/eqonvert/pkg/eqoa"
)

// ── helpers ──────────────────────────────────────────────────────────────────

// minimalJoints returns N joints all as roots with identity transforms.
func minimalJoints(n int) []eqoa.Joint {
	joints := make([]eqoa.Joint, n)
	for i := range joints {
		joints[i] = eqoa.Joint{
			ParentIndex: -1,
			Rotation:    [4]float32{0, 0, 0, 1},
			Scale:       1,
		}
	}
	return joints
}

// skinnedAsset builds an Asset with a hierarchy of n joints and one face group
// containing the supplied vertices (Indices are auto-generated as a fan).
func skinnedAsset(joints []eqoa.Joint, verts []eqoa.Vertex) *eqoa.Asset {
	idx := make([]uint32, 0, (len(verts)-2)*3)
	for i := 0; i < len(verts)-2; i++ {
		idx = append(idx, 0, uint32(i+1), uint32(i+2))
	}
	return &eqoa.Asset{
		ID: 0xDEADBEEF,
		Meshes: []*eqoa.Mesh{{
			Type:       5,
			FaceGroups: []eqoa.FaceGroup{{Vertices: verts, Indices: idx}},
		}},
		Hierarchy: &eqoa.HSpriteHierarchy{Joints: joints},
	}
}

// bareSkinnedAsset is like skinnedAsset but has no hierarchy (SkinSubSprite case).
func bareSkinnedAsset(verts []eqoa.Vertex) *eqoa.Asset {
	idx := make([]uint32, 0, (len(verts)-2)*3)
	for i := 0; i < len(verts)-2; i++ {
		idx = append(idx, 0, uint32(i+1), uint32(i+2))
	}
	return &eqoa.Asset{
		ID: 0xDEADBEEF,
		Meshes: []*eqoa.Mesh{{
			Type:       5,
			FaceGroups: []eqoa.FaceGroup{{Vertices: verts, Indices: idx}},
		}},
	}
}

// exportAndAddRoot runs ExportAssetToBuilder and adds the sprite root to the scene,
// matching what cmd/convert.go does in production.
func exportAndAddRoot(t *testing.T, asset *eqoa.Asset) *Builder {
	t.Helper()
	b := NewBuilder()
	rootIdx, err := ExportAssetToBuilder(b, bytes.NewReader(nil), asset, binary.LittleEndian, nil, true)
	if err != nil {
		t.Fatalf("ExportAssetToBuilder: %v", err)
	}
	b.AddSceneNode(rootIdx)
	return b
}

// readU8Vec4 reads all VEC4 UNSIGNED_BYTE accessor elements from the builder binary.
func readU8Vec4(b *Builder, accIdx int) [][4]uint8 {
	acc := b.Doc.Accessors[accIdx]
	bv := b.Doc.BufferViews[acc.BufferView]
	raw := b.binData.Bytes()
	base := bv.ByteOffset + acc.ByteOffset
	out := make([][4]uint8, acc.Count)
	for i := range out {
		copy(out[i][:], raw[base+i*4:])
	}
	return out
}

// readF32Vec4 reads all VEC4 FLOAT accessor elements from the builder binary.
func readF32Vec4(b *Builder, accIdx int) [][4]float32 {
	acc := b.Doc.Accessors[accIdx]
	bv := b.Doc.BufferViews[acc.BufferView]
	raw := b.binData.Bytes()
	base := bv.ByteOffset + acc.ByteOffset
	out := make([][4]float32, acc.Count)
	for i := range out {
		for k := 0; k < 4; k++ {
			bits := binary.LittleEndian.Uint32(raw[base+i*16+k*4:])
			out[i][k] = math.Float32frombits(bits)
		}
	}
	return out
}

// primitiveAccessorIdx returns the accessor index for the named attribute in
// mesh 0, primitive 0, or -1 if not present.
func primitiveAccessorIdx(b *Builder, attr string) int {
	if len(b.Doc.Meshes) == 0 || len(b.Doc.Meshes[0].Primitives) == 0 {
		return -1
	}
	idx, ok := b.Doc.Meshes[0].Primitives[0].Attributes[attr]
	if !ok {
		return -1
	}
	return idx
}

// ── math helper tests ─────────────────────────────────────────────────────────

func TestQuatNorm_Unit(t *testing.T) {
	got := quatNorm([4]float32{0, 0, 0, 1})
	if got[3] != 1 || got[0] != 0 {
		t.Errorf("unit quat unchanged: got %v", got)
	}
}

func TestQuatNorm_Normalizes(t *testing.T) {
	got := quatNorm([4]float32{0, 0, 0, 2})
	if math.Abs(float64(got[3])-1.0) > 1e-6 {
		t.Errorf("expected w=1 after normalizing (0,0,0,2), got %v", got)
	}
}

func TestQuatNorm_ZeroReturnsIdentity(t *testing.T) {
	got := quatNorm([4]float32{0, 0, 0, 0})
	want := []float32{0, 0, 0, 1}
	for i, v := range got {
		if v != want[i] {
			t.Errorf("zero quat: got %v, want %v", got, want)
			break
		}
	}
}

func TestQuatNorm_NaNReturnsIdentity(t *testing.T) {
	nan := float32(math.NaN())
	got := quatNorm([4]float32{nan, 0, 0, 1})
	if got[3] != 1 || got[0] != 0 {
		t.Errorf("NaN quat: expected identity, got %v", got)
	}
}

func TestNormalNorm_Normalizes(t *testing.T) {
	got := normalNorm([3]float32{0, 3, 0})
	if math.Abs(float64(got[1])-1.0) > 1e-6 {
		t.Errorf("expected (0,1,0), got %v", got)
	}
}

func TestNormalNorm_ZeroReturnsUp(t *testing.T) {
	got := normalNorm([3]float32{0, 0, 0})
	if got[1] != 1 || got[0] != 0 || got[2] != 0 {
		t.Errorf("zero normal: expected (0,1,0), got %v", got)
	}
}

func TestSanitizeVec3_PassesThrough(t *testing.T) {
	v := [3]float32{1, 2, 3}
	got := sanitizeVec3(v)
	if got != v {
		t.Errorf("expected %v unchanged, got %v", v, got)
	}
}

func TestSanitizeVec3_ZerosNaN(t *testing.T) {
	nan := float32(math.NaN())
	got := sanitizeVec3([3]float32{nan, 1, 2})
	if got[0] != 0 || got[1] != 1 || got[2] != 2 {
		t.Errorf("NaN not zeroed: got %v", got)
	}
}

func TestSanitizeFloat_ZerosInf(t *testing.T) {
	if sanitizeFloat(float32(math.Inf(1))) != 0 {
		t.Error("Inf not zeroed")
	}
}

// ── ACCESSOR_JOINTS_USED_ZERO_WEIGHT ─────────────────────────────────────────

// TestJointsZeroedForZeroWeight verifies that a dead-slot joint (non-zero index,
// zero weight) is clamped to joint 0 after export.
func TestJointsZeroedForZeroWeight(t *testing.T) {
	// Vertex: influenced 100% by joint 3; joint 7 sits in the unused slot with weight=0.
	verts := []eqoa.Vertex{
		{Pos: [3]float32{0, 0, 0}, Joints: [4]uint8{3, 7, 0, 0}, Weights: [4]uint8{255, 0, 0, 0}},
		{Pos: [3]float32{1, 0, 0}, Joints: [4]uint8{3, 7, 0, 0}, Weights: [4]uint8{255, 0, 0, 0}},
		{Pos: [3]float32{0, 1, 0}, Joints: [4]uint8{3, 7, 0, 0}, Weights: [4]uint8{255, 0, 0, 0}},
	}
	b := exportAndAddRoot(t, skinnedAsset(minimalJoints(8), verts))

	jAccIdx := primitiveAccessorIdx(b, "JOINTS_0")
	if jAccIdx < 0 {
		t.Fatal("JOINTS_0 accessor missing")
	}
	joints := readU8Vec4(b, jAccIdx)
	for i, j := range joints {
		if j[0] != 3 {
			t.Errorf("vertex %d: expected joint[0]=3, got %d", i, j[0])
		}
		if j[1] != 0 {
			t.Errorf("vertex %d: joint[1] should be 0 (zero-weight slot), got %d", i, j[1])
		}
	}
}

// TestJointsPreservedForNonZeroWeight verifies that joints with actual weight are kept.
func TestJointsPreservedForNonZeroWeight(t *testing.T) {
	// Vertex split ~50/50 between joints 2 and 5.
	verts := []eqoa.Vertex{
		{Pos: [3]float32{0, 0, 0}, Joints: [4]uint8{2, 5, 0, 0}, Weights: [4]uint8{128, 127, 0, 0}},
		{Pos: [3]float32{1, 0, 0}, Joints: [4]uint8{2, 5, 0, 0}, Weights: [4]uint8{128, 127, 0, 0}},
		{Pos: [3]float32{0, 1, 0}, Joints: [4]uint8{2, 5, 0, 0}, Weights: [4]uint8{128, 127, 0, 0}},
	}
	b := exportAndAddRoot(t, skinnedAsset(minimalJoints(8), verts))

	jAccIdx := primitiveAccessorIdx(b, "JOINTS_0")
	if jAccIdx < 0 {
		t.Fatal("JOINTS_0 accessor missing")
	}
	wAccIdx := primitiveAccessorIdx(b, "WEIGHTS_0")

	joints := readU8Vec4(b, jAccIdx)
	weights := readF32Vec4(b, wAccIdx)
	for i, j := range joints {
		if j[0] != 2 {
			t.Errorf("vertex %d: expected joint[0]=2, got %d", i, j[0])
		}
		if j[1] != 5 {
			t.Errorf("vertex %d: expected joint[1]=5, got %d", i, j[1])
		}
		if weights[i][0] == 0 || weights[i][1] == 0 {
			t.Errorf("vertex %d: both active weights should be non-zero, got %v", i, weights[i])
		}
	}
}

// TestOOBJointClamped verifies that a joint index ≥ numJoints is clamped to 0.
func TestOOBJointClamped(t *testing.T) {
	// Hierarchy has 4 joints; vertex references joint index 99 (OOB).
	verts := []eqoa.Vertex{
		{Pos: [3]float32{0, 0, 0}, Joints: [4]uint8{99, 0, 0, 0}, Weights: [4]uint8{255, 0, 0, 0}},
		{Pos: [3]float32{1, 0, 0}, Joints: [4]uint8{99, 0, 0, 0}, Weights: [4]uint8{255, 0, 0, 0}},
		{Pos: [3]float32{0, 1, 0}, Joints: [4]uint8{99, 0, 0, 0}, Weights: [4]uint8{255, 0, 0, 0}},
	}
	b := exportAndAddRoot(t, skinnedAsset(minimalJoints(4), verts))

	jAccIdx := primitiveAccessorIdx(b, "JOINTS_0")
	if jAccIdx < 0 {
		t.Fatal("JOINTS_0 accessor missing")
	}
	joints := readU8Vec4(b, jAccIdx)
	for i, j := range joints {
		if j[0] != 0 {
			t.Errorf("vertex %d: OOB joint 99 should be clamped to 0, got %d", i, j[0])
		}
	}
}

// TestWeightsRenormalize verifies byte weights are renormalized to sum=1.0.
func TestWeightsRenormalize(t *testing.T) {
	// Two joints, weights (100, 100) — both non-zero, should sum to 1.0 after normalization.
	verts := []eqoa.Vertex{
		{Pos: [3]float32{0, 0, 0}, Joints: [4]uint8{0, 1, 0, 0}, Weights: [4]uint8{100, 100, 0, 0}},
		{Pos: [3]float32{1, 0, 0}, Joints: [4]uint8{0, 1, 0, 0}, Weights: [4]uint8{100, 100, 0, 0}},
		{Pos: [3]float32{0, 1, 0}, Joints: [4]uint8{0, 1, 0, 0}, Weights: [4]uint8{100, 100, 0, 0}},
	}
	b := exportAndAddRoot(t, skinnedAsset(minimalJoints(2), verts))

	wAccIdx := primitiveAccessorIdx(b, "WEIGHTS_0")
	if wAccIdx < 0 {
		t.Fatal("WEIGHTS_0 accessor missing")
	}
	weights := readF32Vec4(b, wAccIdx)
	for i, w := range weights {
		sum := w[0] + w[1] + w[2] + w[3]
		if math.Abs(float64(sum)-1.0) > 1e-5 {
			t.Errorf("vertex %d: weight sum = %f, want 1.0", i, sum)
		}
	}
}

// ── NODE_SKINNED_MESH_WITHOUT_SKIN ────────────────────────────────────────────

// TestNoJointsWithoutSkin verifies that JOINTS_0 and WEIGHTS_0 are not written
// when the asset has no hierarchy (SkinSubSprite case).
func TestNoJointsWithoutSkin(t *testing.T) {
	verts := []eqoa.Vertex{
		{Pos: [3]float32{0, 0, 0}, Joints: [4]uint8{3, 7, 0, 0}, Weights: [4]uint8{200, 55, 0, 0}},
		{Pos: [3]float32{1, 0, 0}, Joints: [4]uint8{3, 7, 0, 0}, Weights: [4]uint8{200, 55, 0, 0}},
		{Pos: [3]float32{0, 1, 0}, Joints: [4]uint8{3, 7, 0, 0}, Weights: [4]uint8{200, 55, 0, 0}},
	}
	b := exportAndAddRoot(t, bareSkinnedAsset(verts))

	if primitiveAccessorIdx(b, "JOINTS_0") >= 0 {
		t.Error("JOINTS_0 should not be present when no skin/hierarchy exists")
	}
	if primitiveAccessorIdx(b, "WEIGHTS_0") >= 0 {
		t.Error("WEIGHTS_0 should not be present when no skin/hierarchy exists")
	}
}

// TestNoSkinOnMeshNode verifies the MeshNode has no skin reference when no hierarchy.
func TestNoSkinOnMeshNode(t *testing.T) {
	verts := []eqoa.Vertex{
		{Pos: [3]float32{0, 0, 0}},
		{Pos: [3]float32{1, 0, 0}},
		{Pos: [3]float32{0, 1, 0}},
	}
	b := exportAndAddRoot(t, bareSkinnedAsset(verts))

	for i, n := range b.Doc.Nodes {
		if n.Mesh != nil && n.Skin != nil {
			t.Errorf("node %d has both mesh and skin but no hierarchy was provided", i)
		}
	}
}

// ── NODE_SKINNED_MESH_NON_ROOT ────────────────────────────────────────────────

// TestSkinnedMeshNodeIsSceneRoot verifies that a MeshNode with a Skin is a
// top-level scene node, not buried under the sprite grouping node.
func TestSkinnedMeshNodeIsSceneRoot(t *testing.T) {
	verts := []eqoa.Vertex{
		{Pos: [3]float32{0, 0, 0}, Joints: [4]uint8{0, 0, 0, 0}, Weights: [4]uint8{255, 0, 0, 0}},
		{Pos: [3]float32{1, 0, 0}, Joints: [4]uint8{0, 0, 0, 0}, Weights: [4]uint8{255, 0, 0, 0}},
		{Pos: [3]float32{0, 1, 0}, Joints: [4]uint8{0, 0, 0, 0}, Weights: [4]uint8{255, 0, 0, 0}},
	}
	b := exportAndAddRoot(t, skinnedAsset(minimalJoints(2), verts))

	sceneRoots := map[int]bool{}
	for _, ni := range b.Doc.Scenes[0].Nodes {
		sceneRoots[ni] = true
	}

	meshNodeIdx := -1
	for i, n := range b.Doc.Nodes {
		if n.Mesh != nil && n.Skin != nil {
			meshNodeIdx = i
			break
		}
	}
	if meshNodeIdx < 0 {
		t.Fatal("no skinned MeshNode found")
	}
	if !sceneRoots[meshNodeIdx] {
		t.Errorf("skinned MeshNode (idx %d) is not a scene root; scene roots: %v", meshNodeIdx, b.Doc.Scenes[0].Nodes)
	}
}

// TestSkinnedMeshNodeNotChildOfSpriteRoot verifies the sprite grouping node does
// not list the skinned MeshNode as one of its children.
func TestSkinnedMeshNodeNotChildOfSpriteRoot(t *testing.T) {
	verts := []eqoa.Vertex{
		{Pos: [3]float32{0, 0, 0}, Joints: [4]uint8{0, 0, 0, 0}, Weights: [4]uint8{255, 0, 0, 0}},
		{Pos: [3]float32{1, 0, 0}, Joints: [4]uint8{0, 0, 0, 0}, Weights: [4]uint8{255, 0, 0, 0}},
		{Pos: [3]float32{0, 1, 0}, Joints: [4]uint8{0, 0, 0, 0}, Weights: [4]uint8{255, 0, 0, 0}},
	}
	b := exportAndAddRoot(t, skinnedAsset(minimalJoints(2), verts))

	meshNodeIdx := -1
	for i, n := range b.Doc.Nodes {
		if n.Mesh != nil && n.Skin != nil {
			meshNodeIdx = i
			break
		}
	}
	if meshNodeIdx < 0 {
		t.Fatal("no skinned MeshNode found")
	}

	// Find the sprite root (node named "Sprite_0x...")
	for i, n := range b.Doc.Nodes {
		for _, child := range n.Children {
			if child == meshNodeIdx {
				t.Errorf("sprite root node %d lists skinned MeshNode %d as child — should not", i, meshNodeIdx)
			}
		}
	}
}

// ── ANIMATION TIMING ─────────────────────────────────────────────────────────

// assetWithAction builds a minimal skinned asset with one ActionSet whose
// timing parameters (fps, timeScale, numFrames) can be controlled.
func assetWithAction(joints []eqoa.Joint, fps, timeScale float32, numFrames int) *eqoa.Asset {
	channels := make([]eqoa.ActionChannel, len(joints))
	for ci := range channels {
		channels[ci].BoneID = int32(ci)
		channels[ci].Frames = make([]eqoa.BoneTransform, numFrames)
		for fi := range channels[ci].Frames {
			channels[ci].Frames[fi].Rotation = [4]float32{0, 0, 0, 1}
			channels[ci].Frames[fi].Scale = 1
		}
	}
	return &eqoa.Asset{
		ID: 0xDEAD0001,
		Meshes: []*eqoa.Mesh{{
			Type: 5,
			FaceGroups: []eqoa.FaceGroup{{
				Vertices: []eqoa.Vertex{
					{Pos: [3]float32{0, 0, 0}, Joints: [4]uint8{0, 0, 0, 0}, Weights: [4]uint8{255, 0, 0, 0}},
					{Pos: [3]float32{1, 0, 0}, Joints: [4]uint8{0, 0, 0, 0}, Weights: [4]uint8{255, 0, 0, 0}},
					{Pos: [3]float32{0, 1, 0}, Joints: [4]uint8{0, 0, 0, 0}, Weights: [4]uint8{255, 0, 0, 0}},
				},
				Indices: []uint32{0, 1, 2},
			}},
		}},
		Hierarchy: &eqoa.HSpriteHierarchy{Joints: joints},
		Actions: []*eqoa.ActionSet{{
			DictID:      0xABCD,
			NumFrames:   int32(numFrames),
			NumChannels: int32(len(joints)),
			TimeScale:   timeScale,
			FPS:         fps,
			Channels:    channels,
		}},
	}
}

// findScalarAccessorWithCount returns the first SCALAR FLOAT accessor with the given Count.
func findScalarAccessorWithCount(b *Builder, count int) *Accessor {
	for i := range b.Doc.Accessors {
		acc := &b.Doc.Accessors[i]
		if acc.Type == "SCALAR" && acc.ComponentType == 5126 && acc.Count == count {
			return acc
		}
	}
	return nil
}

// TestAnimationTimingV0 covers the CHAR.ESF case: FPS=1.0 (version 0 default),
// TimeScale=10.0 → effectiveFPS=10 → dt=0.1s per frame.
func TestAnimationTimingV0(t *testing.T) {
	b := exportAndAddRoot(t, assetWithAction(minimalJoints(2), 1.0, 10.0, 40))

	acc := findScalarAccessorWithCount(b, 40)
	if acc == nil {
		t.Fatal("no SCALAR count=40 accessor found")
	}
	if len(acc.Max) == 0 {
		t.Fatal("time accessor has no Max")
	}
	// 39 frames × (1 / (1.0 × 10.0)) = 3.9s
	wantMax := float32(39) / (1.0 * 10.0)
	if math.Abs(float64(acc.Max[0]-wantMax)) > 1e-4 {
		t.Errorf("time max = %.5f, want %.5f", acc.Max[0], wantMax)
	}
}

// TestAnimationTimingV1 covers the TUNARIA.ESF / zone-object case: FPS=0.15,
// TimeScale=24.0 → effectiveFPS=3.6 → dt≈0.278s per frame.
func TestAnimationTimingV1(t *testing.T) {
	b := exportAndAddRoot(t, assetWithAction(minimalJoints(2), 0.15, 24.0, 5))

	acc := findScalarAccessorWithCount(b, 5)
	if acc == nil {
		t.Fatal("no SCALAR count=5 accessor found")
	}
	if len(acc.Max) == 0 {
		t.Fatal("time accessor has no Max")
	}
	// 4 frames × (1 / (0.15 × 24.0)) ≈ 1.111s
	wantMax := float32(4) / (0.15 * 24.0)
	if math.Abs(float64(acc.Max[0]-wantMax)) > 1e-3 {
		t.Errorf("time max = %.5f, want %.5f", acc.Max[0], wantMax)
	}
}

// TestNonSkinnedMeshNodeUnderSpriteRoot verifies that a non-skinned MeshNode
// stays under the sprite grouping node (not promoted to scene root).
func TestNonSkinnedMeshNodeUnderSpriteRoot(t *testing.T) {
	// Static mesh (Type != 5)
	verts := []eqoa.Vertex{
		{Pos: [3]float32{0, 0, 0}, Color: [4]float32{1, 1, 1, 1}},
		{Pos: [3]float32{1, 0, 0}, Color: [4]float32{1, 1, 1, 1}},
		{Pos: [3]float32{0, 1, 0}, Color: [4]float32{1, 1, 1, 1}},
	}
	idx := []uint32{0, 1, 2}
	asset := &eqoa.Asset{
		ID: 0xDEADBEEF,
		Meshes: []*eqoa.Mesh{{
			Type:       3,
			FaceGroups: []eqoa.FaceGroup{{Vertices: verts, Indices: idx}},
		}},
	}
	b := exportAndAddRoot(t, asset)

	sceneRoots := map[int]bool{}
	for _, ni := range b.Doc.Scenes[0].Nodes {
		sceneRoots[ni] = true
	}

	for i, n := range b.Doc.Nodes {
		if n.Mesh != nil && sceneRoots[i] {
			// Only the sprite root (which has no mesh itself) should be a scene root
			t.Errorf("non-skinned MeshNode %d was promoted to scene root", i)
		}
	}

	// Verify it is under some non-scene-root node (the sprite root)
	found := false
	for _, n := range b.Doc.Nodes {
		for _, child := range n.Children {
			if b.Doc.Nodes[child].Mesh != nil {
				found = true
			}
		}
	}
	if !found {
		t.Error("non-skinned MeshNode not found as child of any node")
	}
}
