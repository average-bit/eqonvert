package eqoa

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// animRecord is one 0x2910 v3 record: ActionID, Action2ID, AnimID, then the
// fields the export does not read.
func animRecord(action, action2, anim uint32) []byte {
	return le(action, action2, anim, float32(1), 0, 0, float32(0), float32(0), 0, float32(1), 0)
}

func loadSprite(t *testing.T, sprite []byte) *Asset {
	t.Helper()
	r := bytes.NewReader(sprite)
	obj, _, err := ReadObject(r, binary.LittleEndian, 0, map[uint32]*ESFObject{})
	if err != nil {
		t.Fatal(err)
	}
	a, err := LoadAsset(r, obj, binary.LittleEndian)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestLoadAssetReadsAnimTable(t *testing.T) {
	table := append(le(2), animRecord(0xA1, 0xA2, 0)...)
	table = append(table, animRecord(0xB1, 0, 0x0E)...)
	a := loadSprite(t, esfObj(0x2700, 6, nil, esfObj(0x2710, 4, le(0x10)), esfObj(0x2910, 3, table)))
	if a.AnimTableErr != nil || len(a.AnimTable) != 2 {
		t.Fatalf("AnimTable %+v, err %v", a.AnimTable, a.AnimTableErr)
	}
	if r := a.AnimTable[1]; r.ActionID != 0xB1 || r.Action2ID != 0 || r.AnimID != 0x0E {
		t.Errorf("record 1 %+v", r)
	}
}

func TestLoadAssetAnimTableOnlyV3CSprite(t *testing.T) {
	table := append(le(1), animRecord(0xA1, 0, 0)...)
	if a := loadSprite(t, esfObj(0x2700, 3, nil, esfObj(0x2910, 0, table))); a.AnimTable != nil || a.AnimTableErr != nil {
		t.Errorf("v0 0x2910 should not be read: %+v %v", a.AnimTable, a.AnimTableErr)
	}
	if a := loadSprite(t, esfObj(0x2200, 0, nil, esfObj(0x2910, 3, table))); a.AnimTable != nil {
		t.Errorf("only a CSprite has a 0x2910 table: %+v", a.AnimTable)
	}
	if a := loadSprite(t, esfObj(0x2700, 6, nil, esfObj(0x2910, 3, le(3)))); a.AnimTable != nil || a.AnimTableErr == nil {
		t.Errorf("short table: want an error, got %+v %v", a.AnimTable, a.AnimTableErr)
	}
}
