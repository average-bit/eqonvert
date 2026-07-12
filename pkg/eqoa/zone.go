package eqoa

import (
	"encoding/binary"
	"fmt"
	"math"
)

// ZoneActor represents a single actor placement in a zone (0x6000).
// Confirmed layout via Ghidra decompile of VIESFParse__ParseZoneActor (0x40ff78):
//
//	[0:4]   uint32      InstanceID — DictID of this placement record
//	[4:16]  [3]float32  Position   — world X, Y, Z
//	[16:28] [3]float32  Rotation   — Euler angles (NOT quaternion)
//	[28:32] uint32      SpriteID   — model hash ID (7th float, bit-reinterpreted)
//	[32:36] [4]uint8    Color      — RGBA tint; typically [r,g,b,0xFF]
type ZoneActor struct {
	InstanceID uint32
	Position   [3]float32
	Rotation   [3]float32
	SpriteID   uint32
	Color      [4]uint8
}

// ParseZoneActor parses the 36-byte ZoneActor object body.
func ParseZoneActor(data []byte, order binary.ByteOrder) (*ZoneActor, error) {
	if len(data) < 36 {
		return nil, fmt.Errorf("ZoneActor data too short: %d", len(data))
	}

	a := &ZoneActor{
		InstanceID: order.Uint32(data[0:4]),
	}
	for i := range a.Position {
		a.Position[i] = math.Float32frombits(order.Uint32(data[4+i*4:]))
	}
	for i := range a.Rotation {
		a.Rotation[i] = math.Float32frombits(order.Uint32(data[16+i*4:]))
	}
	// Bytes 28-31: the sprite model hash ID, stored as float32 in the file but
	// used as a uint32 lookup key — reinterpret the bits directly.
	a.SpriteID = order.Uint32(data[28:32])
	copy(a.Color[:], data[32:36])

	return a, nil
}

// ZoneRoom represents a room/chunk definition in a zone (0x3240).
type ZoneRoom struct {
	DictID   uint32
	Unknown1 uint32
	Position [3]float32
	Size     [3]float32
	Unknown2 [2]uint32
}

func ParseZoneRoom(data []byte, order binary.ByteOrder) (*ZoneRoom, error) {
	if len(data) < 40 {
		return nil, fmt.Errorf("ZoneRoom data too short: %d", len(data))
	}

	r := &ZoneRoom{}
	r.DictID = order.Uint32(data[0:4])
	if len(data) >= 8 {
		r.Unknown1 = order.Uint32(data[4:8])
	}

	// Assuming Position starts at 8
	if len(data) >= 20 {
		for i := 0; i < 3; i++ {
			bits := order.Uint32(data[8+i*4 : 12+i*4])
			r.Position[i] = math.Float32frombits(bits)
		}
	}

	// Assuming Size starts at 20
	if len(data) >= 32 {
		for i := 0; i < 3; i++ {
			bits := order.Uint32(data[20+i*4 : 24+i*4])
			r.Size[i] = math.Float32frombits(bits)
		}
	}

	// Remaining
	if len(data) >= 40 {
		r.Unknown2[0] = order.Uint32(data[32:36])
		if len(data) >= 44 {
			r.Unknown2[1] = order.Uint32(data[36:40])
		}
	}

	return r, nil
}
