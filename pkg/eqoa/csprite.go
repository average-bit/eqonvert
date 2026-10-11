package eqoa

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
)

// CSprite (0x2700) records beyond the header, mesh, skeleton and attach slots
// that LoadAsset reads. Each layout below was decoded from the bytes of both
// discs (Frontiers and the original release) and every cross-reference named in
// a comment holds for every CSprite on both discs (855 Frontiers, 419 vanilla,
// in CHAR, CHARSEL, DEBUG and the zone files). A field gets a name only when the data proves what it is; every other
// field is UnknownN, where N is its byte offset in the record.

// cspriteChildTypes is the child list of a CSprite by its ObjectVersion, in
// file order. The list is fixed per version on both discs: each version adds
// records to the end of the one before it. v3, with no 0xB070, 0x2450 or 0x2930,
// appears only in DEBUG. CHAR holds v5 (one sprite), v6 and, on Frontiers, v7;
// the Frontiers zone files hold v7.
var cspriteChildTypes = map[int16][]uint16{
	3: {0x2710, 0x1110, 0x5000, 0x2800, 0x2610, 0x2400, 0x5000, 0x2900, 0x2910, 0x2915, 0x2920},
	5: {0x2710, 0x1110, 0x5000, 0xB070, 0x2800, 0x2610, 0x2400, 0x5000, 0x2450, 0x2900, 0x2910, 0x2915, 0x2920, 0x2930},
	6: {0x2710, 0x1110, 0x5000, 0xB070, 0x2800, 0x2610, 0x2400, 0x5000, 0x2450, 0x2900, 0x2910, 0x2915, 0x2920, 0x2930, 0x2940},
	7: {0x2710, 0x1110, 0x5000, 0xB070, 0x2800, 0x2610, 0x2400, 0x5000, 0x2450, 0x2900, 0x2910, 0x2915, 0x2920, 0x2930, 0x2940, 0x2950, 0x2960},
}

// CSpriteChildTypes returns the child object types a CSprite of the given
// ObjectVersion carries, in file order, or nil for a version not seen on disc.
// v6 is v5 plus 0x2940; v7 is v6 plus 0x2950 and 0x2960 (particle effects).
func CSpriteChildTypes(version int16) []uint16 {
	return append([]uint16(nil), cspriteChildTypes[version]...)
}

// CSpriteHeader is the 0x2710 v4 record (48 bytes on both discs), the first
// child of every CSprite.
type CSpriteHeader struct {
	// ID is the CSprite's id (the 0x2700 has no body of its own).
	ID uint32
	// Unknown4 is six floats; each of the first three is below the one three
	// places later, the pattern of a box min and max. It is several times wider
	// than the mesh, so not its tight bounds. Not proven.
	Unknown4  [6]float32
	Unknown28 uint32 // 0..3
	// SizeFactor is a uniform size factor. Sprites that share one skeleton and
	// the same converted geometry differ in all six Unknown4 floats by exactly
	// their SizeFactor ratio: leopards 0x125E78F0..F3 (0.6, 0.8, 1.0, 1.2) and
	// alligators 0x3E0EBFE1 (0.8) and 0x88806543 (0.5).
	SizeFactor float32
	Unknown36  uint32 // small integers (mostly 9)
	Unknown40  uint32
	Unknown44  uint32
}

// ParseCSpriteHeader decodes a 0x2710 v4 body. It needs at least the 36 bytes
// up to SizeFactor; Unknown36..44 stay 0 when the body is shorter than 48.
func ParseCSpriteHeader(body []byte, order binary.ByteOrder) (CSpriteHeader, error) {
	if len(body) < 36 {
		return CSpriteHeader{}, fmt.Errorf("CSpriteHeader: body too short (%d bytes)", len(body))
	}
	u := func(k int) uint32 { return order.Uint32(body[k : k+4]) }
	h := CSpriteHeader{ID: u(0), Unknown28: u(28), SizeFactor: math.Float32frombits(u(32))}
	for i := range h.Unknown4 {
		h.Unknown4[i] = math.Float32frombits(u(4 + 4*i))
	}
	if len(body) >= 48 {
		h.Unknown36, h.Unknown40, h.Unknown44 = u(36), u(40), u(44)
	}
	return h, nil
}

// CSpriteMeshRef is the 0x2900 record (v0, 12 bytes, one per CSprite).
type CSpriteMeshRef struct {
	Unknown0 uint32 // 1 in every sample (as is the 0x2800 child count)
	// MeshID is the id of the CSprite's mesh: the dword at the sibling 0x2800
	// raw +24, which is the first body dword of the one grandchild under 0x2800 —
	// a 0x2321 (2800 > 2320 > 2321) or a 0x2A50 (2800 > 2A40 > 2A50).
	MeshID   uint32
	Unknown8 uint32 // 0 in every sample
}

// ParseCSpriteMeshRef decodes a 0x2900 body.
func ParseCSpriteMeshRef(body []byte, order binary.ByteOrder) (CSpriteMeshRef, error) {
	if len(body) < 12 {
		return CSpriteMeshRef{}, fmt.Errorf("CSpriteMeshRef: body too short (%d bytes)", len(body))
	}
	return CSpriteMeshRef{
		Unknown0: order.Uint32(body[0:4]),
		MeshID:   order.Uint32(body[4:8]),
		Unknown8: order.Uint32(body[8:12]),
	}, nil
}

// CSpriteAnimation is one record of the 0x2910 v3 animation table.
type CSpriteAnimation struct {
	// ActionID is a 0x2600 dictID in the same CSprite's 0x2610.
	ActionID uint32
	// Action2ID is 0, or the 0x2600 that follows ActionID in 0x2610, with the
	// same frame count. A second clip played with the first.
	Action2ID uint32
	// AnimID is the animation id (strictly increasing within a sprite).
	AnimID    uint32
	Unknown12 float32 // signed, mostly 0.3..2.0
	Unknown16 uint32  // 0 or 1
	// SoundID is 0 or a sound: the DictID of a 0xB010 AdpcmHeader on the disc.
	SoundID   uint32
	Unknown24 float32
	Unknown28 float32
	// Sound2ID is 0 or a second sound (0xB010 DictID); set in 16 Frontiers and
	// 12 vanilla records.
	Sound2ID  uint32
	Unknown36 float32
	Unknown40 uint32
}

// cspriteAnimationSize is the on-disc size of one 0x2910 v3 record.
const cspriteAnimationSize = 44

// ParseCSpriteAnimations decodes a 0x2910 body: u32 count, then count 44-byte
// records. Only version 3 is decoded; v0 (DEBUG only) has a different size.
func ParseCSpriteAnimations(body []byte, version int16, order binary.ByteOrder) ([]CSpriteAnimation, error) {
	if version != 3 {
		return nil, fmt.Errorf("CSpriteAnimations: version %d not decoded", version)
	}
	count, err := recordCount("CSpriteAnimations", body, cspriteAnimationSize, order)
	if err != nil {
		return nil, err
	}
	out := make([]CSpriteAnimation, count)
	for i := range out {
		r := body[4+i*cspriteAnimationSize:]
		u := func(k int) uint32 { return order.Uint32(r[k : k+4]) }
		f := func(k int) float32 { return math.Float32frombits(u(k)) }
		out[i] = CSpriteAnimation{
			ActionID: u(0), Action2ID: u(4), AnimID: u(8), Unknown12: f(12),
			Unknown16: u(16), SoundID: u(20), Unknown24: f(24), Unknown28: f(28),
			Sound2ID: u(32), Unknown36: f(36), Unknown40: u(40),
		}
	}
	return out, nil
}

// ParseCSpriteSlotTable decodes a 0x2915 v1 body: u32 count, then count
// (int32 slot, int32 nodeIndex) pairs. Every NodeIndex is below the 0x2400
// joint count, and every pair of the sprite's 0x2920 list appears here
// unchanged, so this is a longer slot-to-joint table. Unlike 0x2920 the slot
// goes past 2; what slots 3 and up are for is unknown.
func ParseCSpriteSlotTable(body []byte, order binary.ByteOrder) ([]CSpriteAttachSlot, error) {
	count, err := recordCount("CSpriteSlotTable", body, attachSlotRecordSize, order)
	if err != nil {
		return nil, err
	}
	out := make([]CSpriteAttachSlot, count)
	for i := range out {
		off := 4 + i*attachSlotRecordSize
		out[i] = CSpriteAttachSlot{
			Slot:      int32(order.Uint32(body[off : off+4])),
			NodeIndex: int32(order.Uint32(body[off+4 : off+8])),
		}
	}
	return out, nil
}

// CSpriteParticle is one 0xC000 ParticleDefinition embedded in a CSprite's
// 0x2950 (CSprite v7 only).
type CSpriteParticle struct {
	// ID is the 0xC010 dword: the definition's own id, which 0x2960 refers to.
	ID uint32
	// SurfaceDictID is the DictID of the definition's 0x1000 Surface. It equals
	// Def.TextureDictID (0xC020 word 0) in every definition on both discs.
	SurfaceDictID uint32
	Def           *ParticleDefinition
}

// ParseCSpriteParticles decodes a 0x2950 object. It has no body; its children
// are 0xC000 ParticleDefinitions, each with 0xC010 (id), a 0x1000 Surface and
// 0xC020 (parameters, see ParseParticleDefinition). Most 0x2950s are empty.
func ParseCSpriteParticles(r io.ReadSeeker, obj *ESFObject, order binary.ByteOrder) ([]CSpriteParticle, error) {
	var out []CSpriteParticle
	for _, def := range obj.Children {
		if uint16(def.Header.ObjectType) != 0xC000 {
			continue
		}
		var p CSpriteParticle
		for _, c := range def.Children {
			switch uint16(c.Header.ObjectType) {
			case 0xC010:
				b, err := c.ReadBody(r)
				if err != nil {
					return nil, err
				}
				if len(b) < 4 {
					return nil, fmt.Errorf("CSpriteParticles: 0xC010 too short (%d bytes)", len(b))
				}
				p.ID = order.Uint32(b[0:4])
			case 0x1000:
				p.SurfaceDictID = c.DictID
			case 0xC020:
				b, err := c.ReadBody(r)
				if err != nil {
					return nil, err
				}
				if p.Def, err = ParseParticleDefinition(b, int(c.Header.ObjectVersion), order); err != nil {
					return nil, err
				}
			}
		}
		out = append(out, p)
	}
	return out, nil
}

// CSpriteParticleUse is one 32-byte record of a 0x2960 (CSprite v7 only):
// where the sprite emits one of its 0x2950 particle effects.
type CSpriteParticleUse struct {
	// ParticleID is the ID of a CSpriteParticle in the same sprite's 0x2950.
	ParticleID uint32
	Unknown4   uint32 // 0 in every record
	Unknown8   uint32 // 0 in every record
	Unknown12  uint32 // below the 0x2400 joint count in every record
	Unknown16  uint32 // 0 or 1
	Unknown20  uint32 // 0 in every record
	Unknown24  uint32 // one of two values, the same for every record in a sprite
	Unknown28  uint32 // 500 in every record
}

// cspriteParticleUseSize is the on-disc size of one 0x2960 record.
const cspriteParticleUseSize = 32

// ParseCSpriteParticleUses decodes a 0x2960 body: u32 count, then count 32-byte
// records.
func ParseCSpriteParticleUses(body []byte, order binary.ByteOrder) ([]CSpriteParticleUse, error) {
	count, err := recordCount("CSpriteParticleUses", body, cspriteParticleUseSize, order)
	if err != nil {
		return nil, err
	}
	out := make([]CSpriteParticleUse, count)
	for i := range out {
		r := body[4+i*cspriteParticleUseSize:]
		u := func(k int) uint32 { return order.Uint32(r[k : k+4]) }
		out[i] = CSpriteParticleUse{
			ParticleID: u(0), Unknown4: u(4), Unknown8: u(8), Unknown12: u(12),
			Unknown16: u(16), Unknown20: u(20), Unknown24: u(24), Unknown28: u(28),
		}
	}
	return out, nil
}

// MeshSwitch is a 0x2A40, found in a CSprite's 0x2800 in place of a single
// 0x2320. It holds two versions of the mesh and picks one:
//
//	0x2A40
//	├── 0x2A50  id + 6 floats (MeshSwitch.ID, Unknown4)
//	├── 0x2A60  the meshes: two 0x2320 SkinSubSprites
//	└── 0x2A70  one entry per 0x2320, in the same order
//
// On both discs every 0x2A40 has exactly two meshes, the first with more
// vertices than the second, and 0x2A70 gives them 10.0 (15.0 in 8 vanilla
// sprites) and 100.0.
type MeshSwitch struct {
	// ID is the 0x2A50 dword 0; it is what the CSprite's 0x2900 MeshID names.
	ID uint32
	// Unknown4 is six floats; on every 0x2A50, each of the first three is below
	// the one three places later, the pattern of a box min and max. Not proven.
	Unknown4 [6]float32
	// Meshes are the 0x2320 objects under 0x2A60, in file order.
	Meshes  []*ESFObject
	Entries []MeshSwitchEntry
}

// MeshSwitchEntry is one 0x2A70 record.
type MeshSwitchEntry struct {
	// SubSpriteID is the id of the matching 0x2320 in 0x2A60 (its 0x2321 dword
	// 0); entry i names mesh i.
	SubSpriteID uint32
	// Unknown4 is 10.0 or 15.0 for the first, more detailed mesh and 100.0 for
	// the second in every 0x2A40 on both discs. Consistent with a switch
	// distance; not proven.
	Unknown4 float32
}

// ParseMeshSwitch decodes a 0x2A40 object and its 0x2A50, 0x2A60 and 0x2A70
// children.
func ParseMeshSwitch(r io.ReadSeeker, obj *ESFObject, order binary.ByteOrder) (*MeshSwitch, error) {
	ms := &MeshSwitch{}
	for _, c := range obj.Children {
		switch uint16(c.Header.ObjectType) {
		case 0x2A50:
			b, err := c.ReadBody(r)
			if err != nil {
				return nil, err
			}
			if len(b) < 28 {
				return nil, fmt.Errorf("MeshSwitch: 0x2A50 too short (%d bytes)", len(b))
			}
			ms.ID = order.Uint32(b[0:4])
			for i := range ms.Unknown4 {
				ms.Unknown4[i] = math.Float32frombits(order.Uint32(b[4+4*i:]))
			}
		case 0x2A60:
			for _, m := range c.Children {
				if uint16(m.Header.ObjectType) == 0x2320 {
					ms.Meshes = append(ms.Meshes, m)
				}
			}
		case 0x2A70:
			b, err := c.ReadBody(r)
			if err != nil {
				return nil, err
			}
			if ms.Entries, err = ParseMeshSwitchEntries(b, order); err != nil {
				return nil, err
			}
		}
	}
	return ms, nil
}

// ParseMeshSwitchEntries decodes a 0x2A70 body: u32 count, then count
// (u32 sub-sprite id, f32) pairs.
func ParseMeshSwitchEntries(body []byte, order binary.ByteOrder) ([]MeshSwitchEntry, error) {
	count, err := recordCount("MeshSwitchEntries", body, 8, order)
	if err != nil {
		return nil, err
	}
	out := make([]MeshSwitchEntry, count)
	for i := range out {
		off := 4 + i*8
		out[i] = MeshSwitchEntry{
			SubSpriteID: order.Uint32(body[off : off+4]),
			Unknown4:    math.Float32frombits(order.Uint32(body[off+4 : off+8])),
		}
	}
	return out, nil
}

// recordCount reads the u32 count that starts a counted record list and checks
// that count records of size bytes fit in body.
func recordCount(what string, body []byte, size int, order binary.ByteOrder) (int, error) {
	if len(body) < 4 {
		return 0, fmt.Errorf("%s: body too short (%d bytes)", what, len(body))
	}
	count := int64(order.Uint32(body[0:4]))
	if int64(len(body)) < 4+count*int64(size) {
		return 0, fmt.Errorf("%s: count %d exceeds body size %d", what, count, len(body))
	}
	return int(count), nil
}

// CSpriteRecords gathers the records above for one CSprite.
type CSpriteRecords struct {
	Header     *CSpriteHeader       // 0x2710
	MeshRef    *CSpriteMeshRef      // 0x2900
	Animations []CSpriteAnimation   // 0x2910
	SlotTable  []CSpriteAttachSlot  // 0x2915
	Particles  []CSpriteParticle    // 0x2950 (v7)
	Uses       []CSpriteParticleUse // 0x2960 (v7)
	// MeshSwitch is set when the CSprite's 0x2800 holds a 0x2A40.
	MeshSwitch *MeshSwitch
}

// ReadCSpriteRecords decodes the records of a 0x2700 CSprite. It stops at the
// first record that fails to parse and names it in the error.
func ReadCSpriteRecords(r io.ReadSeeker, obj *ESFObject, order binary.ByteOrder) (*CSpriteRecords, error) {
	if t := uint16(obj.Header.ObjectType); t != 0x2700 {
		return nil, fmt.Errorf("ReadCSpriteRecords: object type 0x%X is not a CSprite", t)
	}
	rec := &CSpriteRecords{}
	for _, c := range obj.Children {
		t := uint16(c.Header.ObjectType)
		var err error
		switch t {
		case 0x2710:
			var b []byte
			if b, err = c.ReadBody(r); err == nil {
				var h CSpriteHeader
				if h, err = ParseCSpriteHeader(b, order); err == nil {
					rec.Header = &h
				}
			}
		case 0x2800:
			for _, gc := range c.Children {
				if uint16(gc.Header.ObjectType) == 0x2A40 {
					rec.MeshSwitch, err = ParseMeshSwitch(r, gc, order)
				}
			}
		case 0x2900:
			var b []byte
			if b, err = c.ReadBody(r); err == nil {
				var m CSpriteMeshRef
				if m, err = ParseCSpriteMeshRef(b, order); err == nil {
					rec.MeshRef = &m
				}
			}
		case 0x2910:
			if c.Header.ObjectVersion != 3 {
				break // v0 (DEBUG only) is not decoded
			}
			var b []byte
			if b, err = c.ReadBody(r); err == nil {
				rec.Animations, err = ParseCSpriteAnimations(b, c.Header.ObjectVersion, order)
			}
		case 0x2915:
			var b []byte
			if b, err = c.ReadBody(r); err == nil {
				rec.SlotTable, err = ParseCSpriteSlotTable(b, order)
			}
		case 0x2950:
			rec.Particles, err = ParseCSpriteParticles(r, c, order)
		case 0x2960:
			var b []byte
			if b, err = c.ReadBody(r); err == nil {
				rec.Uses, err = ParseCSpriteParticleUses(b, order)
			}
		}
		if err != nil {
			return rec, fmt.Errorf("CSprite 0x%X: 0x%X: %w", obj.DictID, t, err)
		}
	}
	return rec, nil
}
