package eqoa

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
)

// BoneTransform is a single keyframe for one channel: quaternion XYZW + uniform
// scale + position XYZ.
// Layout confirmed by Ghidra decompile of FUN_0040d6b8 (ParseActionSet):
//
//	Uncompressed: 8 × float32 = 32 bytes per frame
//	Compressed:   8 × int16   = 16 bytes per frame
//	  rot quantization:   × 2^-15 (1/32768)
//	  scale/pos quantization: × 2^-9  (1/512 = 0.001953125)
type BoneTransform struct {
	Rotation [4]float32 // quaternion XYZW
	Scale    float32
	Position [3]float32
}

// ActionChannel is one animation track: the bone it drives plus its keyframes.
//
// BoneID is a hash-like identifier resolved through the sprite's BoneMap
// (object type 0x5000) to a joint index.  Confirmed by Ghidra decompile of
// FUN_0041b6d8 (StartAnimation): for each channel entry the engine looks up
// the ID in the map owned by the object at sprite+0x180 (the 0x5000 BoneMap)
// and binds the channel to the resulting joint index; channels whose ID is
// absent from the map are silently skipped.  This is how one ActionSet is
// shared across skeletons with different joint layouts.
type ActionChannel struct {
	BoneID int32
	Frames []BoneTransform // NumFrames entries
}

// ActionSet is the parsed EQOA animation set (object type 0x2600).
//
// The body is CHANNEL-major, not frame-major:
//
//	dictID, [comprType], numChannels, numFrames, [extraCount], timeScale, [fps, flags]
//	then numChannels × { int32 boneID; numFrames × BoneTransform }
//
// Confirmed by Ghidra decompile of FUN_00421ea8 (init): the entry array at
// +0x24 has numChannels (+0xc, FIRST count) entries of {id, dataOffset} with
// offsets advancing by numFrames (+0x10, SECOND count); and FUN_00401a28 /
// FUN_00401410 use (float)(+0x10) and (float)(+0x10 - 1) as animation END
// POSITIONS — only meaningful if +0x10 is the frame count.
//
// ObjectVersion encoding (from VIObjFile numSubObjects check in FUN_0040d6b8):
//
//	0: numChannels + numFrames + timeScale; fps=1.0, uncompressed
//	1: numChannels + numFrames + timeScale + fps + flags; uncompressed
//	2: compressionType + numChannels + numFrames + timeScale + fps + flags
//	3+: compressionType + numChannels + numFrames + extraCount + timeScale + fps + flags
type ActionSet struct {
	DictID      uint32
	NumChannels int32
	NumFrames   int32
	TimeScale   float32
	FPS         float32
	Channels    []ActionChannel
}

// ParseActionSet parses the raw body of a 0x2600 ActionSet object.
// numSubObjects is the NumberOfSubObjects field from the 12-byte object header
// and determines which optional header fields are present.
func ParseActionSet(r io.Reader, order binary.ByteOrder, numSubObjects int32) (*ActionSet, error) {
	readI32 := func() (int32, error) {
		var v int32
		return v, binary.Read(r, order, &v)
	}
	readU32 := func() (uint32, error) {
		var v uint32
		return v, binary.Read(r, order, &v)
	}
	readF32 := func() (float32, error) {
		var v float32
		return v, binary.Read(r, order, &v)
	}

	dictID, err := readU32()
	if err != nil {
		return nil, fmt.Errorf("ActionSet: DictID: %w", err)
	}
	a := &ActionSet{DictID: dictID}

	// compressionType: 0=float32, 1=int16 quantized
	compressionType := int32(0)
	if numSubObjects > 1 {
		compressionType, err = readI32()
		if err != nil {
			return nil, fmt.Errorf("ActionSet: compressionType: %w", err)
		}
	}

	a.NumChannels, err = readI32()
	if err != nil {
		return nil, fmt.Errorf("ActionSet: numChannels: %w", err)
	}
	a.NumFrames, err = readI32()
	if err != nil {
		return nil, fmt.Errorf("ActionSet: numFrames: %w", err)
	}

	// extraChannelCount is present but ignored for GLB export (it's a secondary effect channel)
	if numSubObjects >= 3 {
		if _, err = readI32(); err != nil {
			return nil, fmt.Errorf("ActionSet: extraChannelCount: %w", err)
		}
	}

	a.TimeScale, err = readF32()
	if err != nil {
		return nil, fmt.Errorf("ActionSet: timeScale: %w", err)
	}

	// When numSubObjects == 0 (version 0), the game stores no fps field and
	// defaults to 1.0 (confirmed: Ghidra FUN_0040d6b8 sets uStack_b8 = 0x3f800000
	// = 1.0f when lVar3 == 0).  Versions 1+ store fps explicitly.
	a.FPS = 1.0
	if numSubObjects > 0 {
		a.FPS, err = readF32()
		if err != nil {
			return nil, fmt.Errorf("ActionSet: fps: %w", err)
		}
		if _, err = readI32(); err != nil { // flags2 — skip
			return nil, fmt.Errorf("ActionSet: flags2: %w", err)
		}
	}
	if a.FPS <= 0 {
		a.FPS = 1.0
	}

	if a.NumChannels <= 0 || a.NumFrames <= 0 {
		return a, nil
	}

	a.Channels = make([]ActionChannel, a.NumChannels)
	for ci := range a.Channels {
		ch := &a.Channels[ci]
		ch.BoneID, err = readI32()
		if err != nil {
			return a, fmt.Errorf("ActionSet: channel %d boneID: %w", ci, err)
		}
		ch.Frames = make([]BoneTransform, a.NumFrames)

		for fi := range ch.Frames {
			bt := &ch.Frames[fi]
			if compressionType == 0 {
				// Uncompressed: 8 × float32
				for k := range bt.Rotation {
					bt.Rotation[k], err = readF32()
					if err != nil {
						return a, fmt.Errorf("ActionSet: channel %d frame %d rot[%d]: %w", ci, fi, k, err)
					}
				}
				bt.Scale, err = readF32()
				if err != nil {
					return a, fmt.Errorf("ActionSet: channel %d frame %d scale: %w", ci, fi, err)
				}
				for k := range bt.Position {
					bt.Position[k], err = readF32()
					if err != nil {
						return a, fmt.Errorf("ActionSet: channel %d frame %d pos[%d]: %w", ci, fi, k, err)
					}
				}
			} else {
				// Compressed: 8 × int16
				// rot[4]:   × 2^-15 (1/32768)
				// scale/pos × 2^-9  (1/512)
				const rotScale = 1.0 / 32768.0
				const posScale = 1.0 / 512.0
				raw := make([]int16, 8)
				if err := binary.Read(r, order, raw); err != nil {
					return a, fmt.Errorf("ActionSet: channel %d frame %d compressed: %w", ci, fi, err)
				}
				bt.Rotation[0] = float32(raw[0]) * rotScale
				bt.Rotation[1] = float32(raw[1]) * rotScale
				bt.Rotation[2] = float32(raw[2]) * rotScale
				bt.Rotation[3] = float32(raw[3]) * rotScale
				// Re-normalize the quaternion to counteract quantization drift
				bt.Rotation = normalizeQuat(bt.Rotation)
				bt.Scale = float32(raw[4]) * posScale
				bt.Position[0] = float32(raw[5]) * posScale
				bt.Position[1] = float32(raw[6]) * posScale
				bt.Position[2] = float32(raw[7]) * posScale
			}
		}
	}

	return a, nil
}

// ParseBoneMap parses the raw body of a 0x5000 BoneMap object:
//
//	dictID (u32), count (i32), then count × { int32 boneID, int32 jointIndex }
//
// Confirmed by Ghidra decompile of FUN_0040e430: the engine inserts each pair
// into a map at object+8, and FUN_0041b6d8 resolves animation channel BoneIDs
// through it to joint indices.
func ParseBoneMap(body []byte, order binary.ByteOrder) (map[int32]int32, error) {
	if len(body) < 8 {
		return nil, fmt.Errorf("BoneMap: body too short (%d bytes)", len(body))
	}
	count := int32(order.Uint32(body[4:8]))
	if count < 0 || len(body) < 8+int(count)*8 {
		return nil, fmt.Errorf("BoneMap: count %d exceeds body size %d", count, len(body))
	}
	m := make(map[int32]int32, count)
	for i := 0; i < int(count); i++ {
		off := 8 + i*8
		boneID := int32(order.Uint32(body[off : off+4]))
		jointIdx := int32(order.Uint32(body[off+4 : off+8]))
		m[boneID] = jointIdx
	}
	return m, nil
}

func normalizeQuat(q [4]float32) [4]float32 {
	lenSq := q[0]*q[0] + q[1]*q[1] + q[2]*q[2] + q[3]*q[3]
	if lenSq < 1e-8 {
		return [4]float32{0, 0, 0, 1}
	}
	inv := float32(1.0 / math.Sqrt(float64(lenSq)))
	return [4]float32{q[0] * inv, q[1] * inv, q[2] * inv, q[3] * inv}
}
