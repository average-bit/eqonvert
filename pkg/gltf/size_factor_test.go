package gltf

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"testing"

	"github.com/average-bit/eqonvert/pkg/eqoa"
)

// sizedAsset is a two-joint skeleton with one clip and the given size factor.
func sizedAsset(size float32) *eqoa.Asset {
	joints := minimalJoints(2)
	joints[1].ParentIndex = 0
	return &eqoa.Asset{
		ID:         0xCAFE,
		Hierarchy:  &eqoa.HSpriteHierarchy{Joints: joints},
		BoneMap:    map[int32]int32{0: 0, 1: 1},
		Actions:    []*eqoa.ActionSet{actionTargeting(0xA1, 0, 1)},
		SizeFactor: size,
	}
}

func TestSizeFactorScalesSpriteRoot(t *testing.T) {
	for _, size := range []float32{0.6, 0.8, 1.2, 0.5} {
		b := NewBuilder()
		root, err := ExportAssetToBuilder(b, bytes.NewReader(nil), sizedAsset(size), binary.LittleEndian, nil, true)
		if err != nil {
			t.Fatal(err)
		}
		n := b.Doc.Nodes[root]
		if n.Name != "Sprite_0xCAFE" || !reflect.DeepEqual(n.Scale, []float32{size, size, size}) {
			t.Errorf("size %v: root %q scale %v", size, n.Name, n.Scale)
		}
		// The skeleton's root joint hangs under the scaled node.
		if len(b.Doc.Skins) != 1 || len(n.Children) == 0 || n.Children[0] != b.Doc.Skins[0].Joints[0] {
			t.Errorf("size %v: root children %v, skin %+v", size, n.Children, b.Doc.Skins)
		}
	}
	for _, size := range []float32{0, 1} {
		b := NewBuilder()
		root, _ := ExportAssetToBuilder(b, bytes.NewReader(nil), sizedAsset(size), binary.LittleEndian, nil, true)
		if s := b.Doc.Nodes[root].Scale; s != nil {
			t.Errorf("size %v: want no scale, got %v", size, s)
		}
	}
}

func TestSizeFactorKeptUnderZoneActorScale(t *testing.T) {
	za := NewZoneAssembler()
	if err := za.AddAnimatedSpriteNode(bytes.NewReader(nil), sizedAsset(0.5), binary.LittleEndian, nil, [3]float32{1, 2, 3}, [3]float32{}, 2); err != nil {
		t.Fatal(err)
	}
	for _, n := range za.b.Doc.Nodes {
		if n.Name == "Sprite_0xCAFE" {
			if !reflect.DeepEqual(n.Scale, []float32{1, 1, 1}) {
				t.Errorf("scale %v, want [1 1 1] (actor 2 x size factor 0.5)", n.Scale)
			}
			return
		}
	}
	t.Fatal("sprite root node not found")
}
