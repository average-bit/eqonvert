package eqoa

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
)

// header2710 is a 0x2710 v4 body: id, six box floats, u32, size factor, 3 x u32.
func header2710(id uint32, size float32) []byte {
	return le(id, float32(-1), float32(-2), float32(-3), float32(1), float32(2), float32(3), 2, size, 9, 0, 1)
}

func TestParseCSpriteHeader(t *testing.T) {
	h, err := ParseCSpriteHeader(header2710(0x125E78F0, 0.6), binary.LittleEndian)
	if err != nil {
		t.Fatal(err)
	}
	want := CSpriteHeader{ID: 0x125E78F0, Unknown4: [6]float32{-1, -2, -3, 1, 2, 3}, Unknown28: 2, SizeFactor: 0.6, Unknown36: 9, Unknown44: 1}
	if h != want {
		t.Errorf("got %+v, want %+v", h, want)
	}
	if h, err := ParseCSpriteHeader(header2710(1, 0.5)[:36], binary.LittleEndian); err != nil || h.SizeFactor != 0.5 || h.Unknown36 != 0 {
		t.Errorf("36-byte body: %+v, %v", h, err)
	}
	if _, err := ParseCSpriteHeader(make([]byte, 32), binary.LittleEndian); err == nil {
		t.Error("32-byte body: want error")
	}
}

func loadSpriteBytes(t *testing.T, sprite []byte) *Asset {
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

func TestLoadAssetSizeFactor(t *testing.T) {
	for _, tc := range []struct {
		size float32
		want float32
	}{{0.8, 0.8}, {1.2, 1.2}, {1, 1}, {0, 0}, {-1, 0}, {float32(math.NaN()), 0}, {float32(math.Inf(1)), 0}} {
		a := loadSpriteBytes(t, esfObj(0x2700, 6, nil, esfObj(0x2710, 4, header2710(0x10, tc.size))))
		if a.SizeFactor != tc.want || a.ID != 0x10 {
			t.Errorf("size %v: SizeFactor %v (id 0x%X), want %v", tc.size, a.SizeFactor, a.ID, tc.want)
		}
	}
	// A short header (id only) and a non-CSprite give no size factor.
	if a := loadSpriteBytes(t, esfObj(0x2700, 6, nil, esfObj(0x2710, 4, le(0x10)))); a.SizeFactor != 0 {
		t.Errorf("short header: SizeFactor %v", a.SizeFactor)
	}
	if a := loadSpriteBytes(t, esfObj(0x2200, 0, nil, esfObj(0x2710, 4, header2710(0x10, 0.5)))); a.SizeFactor != 0 {
		t.Errorf("HSprite: SizeFactor %v", a.SizeFactor)
	}
}

func TestReadCSpriteRecordsHeader(t *testing.T) {
	sprite := esfObj(0x2700, 6, nil, esfObj(0x2710, 4, header2710(0x3E0EBFE1, 0.8)))
	r := bytes.NewReader(sprite)
	obj, _, err := ReadObject(r, binary.LittleEndian, 0, map[uint32]*ESFObject{})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := ReadCSpriteRecords(r, obj, binary.LittleEndian)
	if err != nil || rec.Header == nil || rec.Header.ID != 0x3E0EBFE1 || rec.Header.SizeFactor != 0.8 {
		t.Errorf("header %+v, %v", rec.Header, err)
	}
}
