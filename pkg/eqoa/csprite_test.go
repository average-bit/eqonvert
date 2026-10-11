package eqoa

import (
	"bytes"
	"encoding/binary"
	"math"
	"reflect"
	"testing"
)

// le encodes uint32 and float32 values little-endian, in order.
func le(vals ...any) []byte {
	buf := new(bytes.Buffer)
	for _, v := range vals {
		switch x := v.(type) {
		case int:
			binary.Write(buf, binary.LittleEndian, uint32(x))
		case uint32:
			binary.Write(buf, binary.LittleEndian, x)
		case float32:
			binary.Write(buf, binary.LittleEndian, math.Float32bits(x))
		default:
			panic("le: unsupported value")
		}
	}
	return buf.Bytes()
}

// esfObj encodes one ESF object: header, children, then its own body.
func esfObj(objType, version uint16, body []byte, children ...[]byte) []byte {
	var inner []byte
	for _, c := range children {
		inner = append(inner, c...)
	}
	inner = append(inner, body...)
	out := new(bytes.Buffer)
	binary.Write(out, binary.LittleEndian, objType)
	binary.Write(out, binary.LittleEndian, version)
	binary.Write(out, binary.LittleEndian, int32(len(inner)))
	binary.Write(out, binary.LittleEndian, int32(len(children)))
	out.Write(inner)
	return out.Bytes()
}

func TestCSpriteChildTypes(t *testing.T) {
	v5, v6, v7 := CSpriteChildTypes(5), CSpriteChildTypes(6), CSpriteChildTypes(7)
	if !reflect.DeepEqual(v6, append(append([]uint16(nil), v5...), 0x2940)) {
		t.Errorf("v6 = %X, want v5 + 2940", v6)
	}
	if !reflect.DeepEqual(v7, append(append([]uint16(nil), v6...), 0x2950, 0x2960)) {
		t.Errorf("v7 = %X, want v6 + 2950, 2960", v7)
	}
	if CSpriteChildTypes(4) != nil {
		t.Error("version 4 was never seen on disc; want nil")
	}
	v7[0] = 0 // the caller's copy, not the table
	if CSpriteChildTypes(7)[0] != 0x2710 {
		t.Error("CSpriteChildTypes returned the shared table")
	}
}

func TestParseCSpriteMeshRef(t *testing.T) {
	got, err := ParseCSpriteMeshRef(le(1, 0xD7B2B79E, 0), binary.LittleEndian)
	if err != nil {
		t.Fatal(err)
	}
	if want := (CSpriteMeshRef{Unknown0: 1, MeshID: 0xD7B2B79E}); got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if _, err := ParseCSpriteMeshRef(le(1, 2), binary.LittleEndian); err == nil {
		t.Error("8-byte body: want error")
	}
}

func TestParseCSpriteAnimations(t *testing.T) {
	rec := func(action, action2, anim, sound, sound2 uint32) []byte {
		return le(action, action2, anim, float32(-1.5), 1, sound, float32(0.4), float32(0.05), sound2, float32(1), 0)
	}
	body := append(le(2), rec(0xA1, 0xA2, 1, 0x5150, 0)...)
	body = append(body, rec(0xA3, 0, 4, 0, 0x5151)...)
	got, err := ParseCSpriteAnimations(body, 3, binary.LittleEndian)
	if err != nil {
		t.Fatal(err)
	}
	want := []CSpriteAnimation{
		{ActionID: 0xA1, Action2ID: 0xA2, AnimID: 1, Unknown12: -1.5, Unknown16: 1, SoundID: 0x5150, Unknown24: 0.4, Unknown28: 0.05, Unknown36: 1},
		{ActionID: 0xA3, AnimID: 4, Unknown12: -1.5, Unknown16: 1, Unknown24: 0.4, Unknown28: 0.05, Sound2ID: 0x5151, Unknown36: 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
	if _, err := ParseCSpriteAnimations(body[:len(body)-1], 3, binary.LittleEndian); err == nil {
		t.Error("truncated body: want error")
	}
	if _, err := ParseCSpriteAnimations(body, 0, binary.LittleEndian); err == nil {
		t.Error("version 0: want error (not decoded)")
	}
}

func TestParseCSpriteSlotTable(t *testing.T) {
	got, err := ParseCSpriteSlotTable(le(3, 0, 12, 1, 7, 5, 30), binary.LittleEndian)
	if err != nil {
		t.Fatal(err)
	}
	want := []CSpriteAttachSlot{{0, 12}, {1, 7}, {5, 30}} // slots past 2 are valid here
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if _, err := ParseCSpriteSlotTable(le(2, 0, 1), binary.LittleEndian); err == nil {
		t.Error("count past body: want error")
	}
}

func TestParseCSpriteParticleUses(t *testing.T) {
	got, err := ParseCSpriteParticleUses(le(1, 0x77, 0, 0, 9, 1, 0, 0xC07FA82B, 500), binary.LittleEndian)
	if err != nil {
		t.Fatal(err)
	}
	want := []CSpriteParticleUse{{ParticleID: 0x77, Unknown12: 9, Unknown16: 1, Unknown24: 0xC07FA82B, Unknown28: 500}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if got, err := ParseCSpriteParticleUses(le(0), binary.LittleEndian); err != nil || len(got) != 0 {
		t.Errorf("empty list: got %v, %v", got, err)
	}
}

func TestParseMeshSwitchEntries(t *testing.T) {
	got, err := ParseMeshSwitchEntries(le(2, 0xD7B2B79E, float32(10), 0xC11EB4C8, float32(100)), binary.LittleEndian)
	if err != nil {
		t.Fatal(err)
	}
	want := []MeshSwitchEntry{{0xD7B2B79E, 10}, {0xC11EB4C8, 100}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestReadCSpriteRecords builds a v7 CSprite holding the decoded records and a
// 0x2A40 mesh switch, and checks the cross-references the decode relies on.
func TestReadCSpriteRecords(t *testing.T) {
	const (
		meshA, meshB, switchID = 0x1111, 0x2222, 0x3333
		particleID, surfaceID  = 0x77, 0xDEADBEEF // buildParticleBody's texture
	)
	subSprite := func(id uint32) []byte {
		return esfObj(0x2320, 0, nil, esfObj(0x2321, 0, le(id, 0, 0, 0, 0, 0, 0, 0)))
	}
	sw := esfObj(0x2A40, 0, nil,
		esfObj(0x2A50, 0, le(uint32(switchID), float32(-1), float32(-1), float32(-1), float32(1), float32(1), float32(1))),
		esfObj(0x2A60, 0, nil, subSprite(meshA), subSprite(meshB)),
		esfObj(0x2A70, 0, le(2, uint32(meshA), float32(10), uint32(meshB), float32(100))),
	)
	particle := esfObj(0xC000, 1, nil,
		esfObj(0xC010, 0, le(uint32(particleID))),
		esfObj(0x1000, 0, le(uint32(surfaceID), 0)),
		esfObj(0xC020, 1, buildParticleBody([]string{""})),
	)
	sprite := esfObj(0x2700, 7, nil,
		esfObj(0x2800, 0, nil, sw),
		esfObj(0x2900, 0, le(1, uint32(switchID), 0)),
		esfObj(0x2910, 3, append(le(1), le(0xA1, 0, 1, float32(1), 0, 0, float32(0), float32(0), 0, float32(1), 0)...)),
		esfObj(0x2915, 1, le(1, 0, 4)),
		esfObj(0x2950, 0, nil, particle),
		esfObj(0x2960, 0, le(1, uint32(particleID), 0, 0, 3, 0, 0, 0, 500)),
	)
	r := bytes.NewReader(sprite)
	obj, _, err := ReadObject(r, binary.LittleEndian, 0, map[uint32]*ESFObject{})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := ReadCSpriteRecords(r, obj, binary.LittleEndian)
	if err != nil {
		t.Fatal(err)
	}

	if rec.MeshRef == nil || rec.MeshSwitch == nil || rec.MeshRef.MeshID != rec.MeshSwitch.ID {
		t.Fatalf("0x2900 MeshID should name the 0x2A50 id: %+v %+v", rec.MeshRef, rec.MeshSwitch)
	}
	ms := rec.MeshSwitch
	if len(ms.Meshes) != 2 || len(ms.Entries) != 2 {
		t.Fatalf("mesh switch: %d meshes, %d entries; want 2, 2", len(ms.Meshes), len(ms.Entries))
	}
	if ms.Entries[0].SubSpriteID != meshA || ms.Entries[1].SubSpriteID != meshB || ms.Entries[1].Unknown4 != 100 {
		t.Errorf("entries %+v", ms.Entries)
	}
	if ms.Unknown4 != [6]float32{-1, -1, -1, 1, 1, 1} {
		t.Errorf("0x2A50 floats %v", ms.Unknown4)
	}
	if len(rec.Animations) != 1 || rec.Animations[0].ActionID != 0xA1 {
		t.Errorf("animations %+v", rec.Animations)
	}
	if !reflect.DeepEqual(rec.SlotTable, []CSpriteAttachSlot{{0, 4}}) {
		t.Errorf("slot table %v", rec.SlotTable)
	}
	if len(rec.Particles) != 1 {
		t.Fatalf("particles %+v", rec.Particles)
	}
	p := rec.Particles[0]
	if p.ID != particleID || p.SurfaceDictID != surfaceID || p.Def == nil || p.Def.TextureDictID != surfaceID {
		t.Errorf("particle %+v", p)
	}
	if len(rec.Uses) != 1 || rec.Uses[0].ParticleID != p.ID || rec.Uses[0].Unknown12 != 3 {
		t.Errorf("uses %+v", rec.Uses)
	}
}

func TestReadCSpriteRecordsSkipsAnimationsV0(t *testing.T) {
	sprite := esfObj(0x2700, 3, nil, esfObj(0x2910, 0, le(1, 0, 0, 0, 0)))
	r := bytes.NewReader(sprite)
	obj, _, err := ReadObject(r, binary.LittleEndian, 0, map[uint32]*ESFObject{})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := ReadCSpriteRecords(r, obj, binary.LittleEndian)
	if err != nil || rec.Animations != nil {
		t.Errorf("v0 0x2910 should be left undecoded: %+v, %v", rec, err)
	}
}
