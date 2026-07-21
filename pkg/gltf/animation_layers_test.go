package gltf

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/average-bit/eqonvert/pkg/eqoa"
)

// actionTargeting builds a single ActionSet whose channels drive the given bone
// IDs (one keyframe of identity rotation).
func actionTargeting(dictID uint32, boneIDs ...int32) *eqoa.ActionSet {
	chans := make([]eqoa.ActionChannel, len(boneIDs))
	for i, bid := range boneIDs {
		chans[i].BoneID = bid
		chans[i].Frames = []eqoa.BoneTransform{{Rotation: [4]float32{0, 0, 0, 1}, Scale: 1}}
	}
	return &eqoa.ActionSet{
		DictID: dictID, NumFrames: 1, NumChannels: int32(len(boneIDs)),
		TimeScale: 1, FPS: 1, Channels: chans,
	}
}

func exportAnims(t *testing.T, asset *eqoa.Asset) []Animation {
	t.Helper()
	b := NewBuilder()
	if _, err := ExportAssetToBuilder(b, bytes.NewReader(nil), asset, binary.LittleEndian, nil, true); err != nil {
		t.Fatalf("ExportAssetToBuilder: %v", err)
	}
	return b.Doc.Animations
}

// TestFrame0BakedIntoDefaultPose guards the multi-animation zone fix: a joint
// whose idle frame 0 carries a positioning offset (a banner mount, a windmill
// hub) must have that offset baked into its node default TRS, so the static
// (not-playing) pose is correct even when the viewer plays a different clip.
func TestFrame0BakedIntoDefaultPose(t *testing.T) {
	aset := &eqoa.ActionSet{
		DictID: 0xABCD, NumFrames: 2, NumChannels: 1, TimeScale: 1, FPS: 1,
		Channels: []eqoa.ActionChannel{{
			BoneID: 1,
			Frames: []eqoa.BoneTransform{
				{Rotation: [4]float32{0, 0, 0, 1}, Scale: 1, Position: [3]float32{1, 20, 3}},
				{Rotation: [4]float32{0, 0, 0, 1}, Scale: 1, Position: [3]float32{1, 20, 3}},
			},
		}},
	}
	asset := &eqoa.Asset{
		ID:        0xB,
		Hierarchy: &eqoa.HSpriteHierarchy{Joints: minimalJoints(2)},
		BoneMap:   map[int32]int32{1: 1},
		Actions:   []*eqoa.ActionSet{aset},
	}
	b := NewBuilder()
	if _, err := ExportAssetToBuilder(b, bytes.NewReader(nil), asset, binary.LittleEndian, nil, true); err != nil {
		t.Fatalf("export: %v", err)
	}
	var found bool
	for _, n := range b.Doc.Nodes {
		if n.Name == "Joint_1" {
			found = true
			if len(n.Translation) != 3 || n.Translation[0] != 1 || n.Translation[1] != 20 || n.Translation[2] != 3 {
				t.Errorf("Joint_1 default translation = %v, want [1 20 3] (frame 0)", n.Translation)
			}
		}
	}
	if !found {
		t.Fatal("Joint_1 node not found")
	}
}

// TestSingleLayerActionNotDuplicated is the regression guard for the warped
// clockwork gears: a prop whose action-pair has only one contributing layer
// must emit exactly one animation. Emitting both the merged clip and an
// identical lone layer left two clips targeting the same joints; a play-all
// zone viewer compounded them and flung each member off its rotate-about-center
// axle.
func TestSingleLayerActionNotDuplicated(t *testing.T) {
	asset := &eqoa.Asset{
		ID:        0xB6CEF0F7,
		Hierarchy: &eqoa.HSpriteHierarchy{Joints: minimalJoints(3)},
		BoneMap:   map[int32]int32{0: 0, 1: 1, 2: 2},
		Actions:   []*eqoa.ActionSet{actionTargeting(0xABCD, 0, 1, 2)},
	}
	anims := exportAnims(t, asset)
	if len(anims) != 1 {
		names := make([]string, len(anims))
		for i, a := range anims {
			names[i] = a.Name
		}
		t.Fatalf("single-layer action produced %d animations %v, want 1 (merged only)", len(anims), names)
	}
}

// TestTwoLayerActionKeepsLayers guards that a genuine upper+lower pair (disjoint
// joints) still emits the merged clip plus both individual layers, so the
// dedup fix doesn't strip real layered animation.
func TestTwoLayerActionKeepsLayers(t *testing.T) {
	asset := &eqoa.Asset{
		ID:        0xC0FFEE,
		Hierarchy: &eqoa.HSpriteHierarchy{Joints: minimalJoints(4)},
		BoneMap:   map[int32]int32{0: 0, 1: 1, 2: 2, 3: 3},
		Actions: []*eqoa.ActionSet{
			actionTargeting(0xABCD, 0, 1), // "upper" → joints 0,1
			actionTargeting(0xABCD, 2, 3), // "lower" → joints 2,3
		},
	}
	anims := exportAnims(t, asset)
	if len(anims) != 3 {
		names := make([]string, len(anims))
		for i, a := range anims {
			names[i] = a.Name
		}
		t.Fatalf("two-layer pair produced %d animations %v, want 3 (merged + 2 layers)", len(anims), names)
	}
	// animations[0] is the merged full-body clip: it must cover all four joints.
	if got := len(anims[0].Channels); got != 8 { // 4 joints × (rot+trans)
		t.Errorf("merged clip has %d channels, want 8 (4 joints × 2 paths)", got)
	}
}
