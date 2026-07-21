package eqoa

import (
	"encoding/binary"
	"io"
	"math"
)

// GroupMember is one entry of a GroupSprite's 0x2C30 member-transform array. Each
// member positions a sub-sprite (from the 0x2C20 sprite array, index-aligned)
// within the group's local space. Layout recovered from
// ParseGroupSpriteMembers__10VIESFParse and CalcMemberTransform__13VIGroupSprite:
// per member = u32 dictID, then 7 float32 [rot(3), scale, pos(3)], and the local
// matrix is T(pos)·R_euler(rot)·S(scale).
type GroupMember struct {
	DictID uint32
	Rot    [3]float32 // euler angles (radians)
	Scale  float32
	Pos    [3]float32 // local translation
}

// ParseGroupMembers reads a GroupSprite's (0x2C00) 0x2C30 member array. Returns
// nil if the group has no member array. The members are index-aligned with the
// group's 0x2C20 sprite-array children.
func ParseGroupMembers(group *ESFObject, r io.ReadSeeker, order binary.ByteOrder) []GroupMember {
	var arr *ESFObject
	for _, c := range group.Children {
		if uint16(c.Header.ObjectType) == 0x2C30 {
			arr = c
			break
		}
	}
	if arr == nil {
		return nil
	}
	body, err := arr.ReadBody(r)
	if err != nil {
		return nil
	}
	return parseGroupMemberBody(body, order)
}

// parseGroupMemberBody decodes the 0x2C30 body: an i32 count followed by count
// records of [u32 dictID, f32 rot·3, f32 scale, f32 pos·3] (32 bytes each).
func parseGroupMemberBody(body []byte, order binary.ByteOrder) []GroupMember {
	if len(body) < 4 {
		return nil
	}
	count := int(int32(order.Uint32(body[0:4])))
	if count < 0 {
		return nil
	}
	// Cap the pre-allocation to what the body can actually hold (32 bytes/member):
	// count comes from untrusted disc data, so a bogus value must not reserve GBs
	// before the bounded loop below runs.
	capHint := count
	if fits := (len(body) - 4) / 32; capHint > fits {
		capHint = fits
	}
	f := func(off int) float32 { return math.Float32frombits(order.Uint32(body[off:])) }
	out := make([]GroupMember, 0, capHint)
	p := 4
	for i := 0; i < count && p+32 <= len(body); i++ {
		out = append(out, GroupMember{
			DictID: order.Uint32(body[p:]),
			Rot:    [3]float32{f(p + 4), f(p + 8), f(p + 12)},
			Scale:  f(p + 16),
			Pos:    [3]float32{f(p + 20), f(p + 24), f(p + 28)},
		})
		p += 32
	}
	return out
}

// GroupSpriteArray returns the 0x2C20 sprite-array child of a GroupSprite, whose
// children are the member sub-sprites in the same order as ParseGroupMembers.
func GroupSpriteArray(group *ESFObject) *ESFObject {
	for _, c := range group.Children {
		if uint16(c.Header.ObjectType) == 0x2C20 {
			return c
		}
	}
	return nil
}
