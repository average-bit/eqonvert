package eqoa

import (
	"bytes"
	"encoding/binary"
	"io"
	"math"
)

type Asset struct {
	ID           uint32
	ObjectType   uint16
	Offset       int64
	Meshes       []*Mesh
	Hierarchy    *HSpriteHierarchy
	HierarchyErr error // set when hierarchy exists but failed to parse
	Actions      []*ActionSet
	BoneMap      map[int32]int32 // 0x5000: animation channel BoneID → joint index
	MatPalObj    *ESFObject
	// Attachments is the 0x2500 HSpriteAttachments array belonging to Hierarchy:
	// which resource (weapon, shield, skin) hangs off which of its joints.
	Attachments []HSpriteAttachment
	// AttachmentsErr is set when a 0x2500 object exists but failed to parse.
	AttachmentsErr error
	// AttachSlots is the 0x2920 CSpriteASlotList belonging to Hierarchy: which
	// joint each of a CSprite's item-attach slots hangs off. This is the
	// character-side counterpart of Attachments — a CSprite carries 0x2920 and
	// never 0x2500, an HSprite the reverse.
	AttachSlots []CSpriteAttachSlot
	// AttachSlotsErr is set when a 0x2920 object exists but failed to parse.
	AttachSlotsErr error
	// AnimTable is the CSprite's 0x2910 v3 animation table (see
	// ParseCSpriteAnimations), or nil when the sprite has none: not a CSprite,
	// the undecoded v0 table in DEBUG, or a table that failed to parse
	// (AnimTableErr). The glTF export names and pairs its clips from it.
	AnimTable []CSpriteAnimation
	// AnimTableErr is set when a v3 0x2910 exists but failed to parse.
	AnimTableErr error
	// SizeFactor is the CSprite's 0x2710 size factor (CSpriteHeader), or 0
	// when the sprite has none or it is not a positive finite number. The glTF
	// export applies it as the scale of the sprite's root node.
	SizeFactor float32
}

func IsSprite(objType uint16) bool {
	switch objType {
	case 0x2000, 0x2700, 0x2200, 0x2310, 0x2320, 0x2C00, 0x2A10:
		return true
	}
	return false
}

// IsSkinnedSprite reports whether objType is a CSprite (0x2700) or HSprite
// (0x2200): a model whose GLB also draws the sub-sprite meshes beneath it.
func IsSkinnedSprite(objType uint16) bool {
	return objType == 0x2700 || objType == 0x2200
}

// IsSubSprite reports whether objType is a sub-sprite mesh (0x2310 or 0x2320).
// Under a CSprite or HSprite such a mesh is a part of that model, not a model of
// its own.
func IsSubSprite(objType uint16) bool {
	return objType == 0x2310 || objType == 0x2320
}

func isSpriteHeader(objType uint16) bool {
	switch objType {
	case 0x2001, 0x2710, 0x2210, 0x2311, 0x2321, 0x2C10, 0x2A11:
		return true
	}
	return false
}

func LoadAsset(r io.ReadSeeker, obj *ESFObject, order binary.ByteOrder) (*Asset, error) {
	asset := &Asset{
		ID:         obj.DictID,
		ObjectType: uint16(obj.Header.ObjectType),
		Offset:     obj.Offset,
	}

	if asset.ID == 0 {
		for _, child := range obj.Children {
			if isSpriteHeader(uint16(child.Header.ObjectType)) {
				body, _ := child.ReadBody(r)
				if len(body) >= 4 {
					asset.ID = order.Uint32(body[0:4])
				}
			}
		}
	}

	if asset.ObjectType == 0x2700 {
		for _, child := range obj.Children {
			if uint16(child.Header.ObjectType) != 0x2710 {
				continue
			}
			if body, err := child.ReadBody(r); err == nil {
				if h, err := ParseCSpriteHeader(body, order); err == nil {
					f := float64(h.SizeFactor)
					if f > 0 && !math.IsInf(f, 0) {
						asset.SizeFactor = h.SizeFactor
					}
				}
			}
			break
		}
	}

	var primObjs []*ESFObject
	var matPalObj *ESFObject
	var hierObj *ESFObject
	var actionObjs []*ESFObject

	collectInternal(obj, &primObjs, &matPalObj, &hierObj, &actionObjs)

	// The skeleton's BoneMap is the 0x5000 sibling that follows the 0x2400
	// hierarchy in the same child list (engine parse order in FUN_0040cdb0:
	// hierarchy first, then FUN_0040e430 expects the very next object to be
	// 0x5000).  Sprites also carry other 0x5000 RefMaps (sound/effect refs) at
	// container level, so "first 0x5000 anywhere" picks the wrong one.
	boneMapObj := findSiblingAfter(obj, hierObj, 0x5000)

	// The 0x2500 attachment array is likewise a sibling that follows the
	// hierarchy: ParseHSpriteObj (FUN_0040cdb0) reads 0x2400, then 0x5000, then
	// the optional 0x2450 triggers, then 0x2500 — all from the same child list.
	attachObj := findSiblingAfter(obj, hierObj, 0x2500)

	// A CSprite's 0x2920 attach-slot list sits in the same place relative to the
	// hierarchy: ParseCSpriteObj (@ 0x0040e5a8) reads 0x2400, then the 0x5000
	// bone map, the optional 0x2450 triggers, the skin list, the play list, the
	// optional node-ID list, and only then 0x2920 — all from the CSprite's own
	// child list.  (0x2920 is emitted only when the CSprite's ObjectVersion != 0.)
	slotObj := findSiblingAfter(obj, hierObj, 0x2920)

	asset.MatPalObj = matPalObj

	for _, pObj := range primObjs {
		mesh, err := ParsePrimBuffer(r, pObj, order)
		if err == nil && mesh != nil {
			asset.Meshes = append(asset.Meshes, mesh)
		}
	}

	if hierObj != nil {
		body, _ := hierObj.ReadBody(r)
		h, err := ParseHSpriteHierarchy(bytes.NewReader(body), order, hierObj.Header.ObjectVersion)
		if err != nil {
			asset.HierarchyErr = err
		} else {
			asset.Hierarchy = h
		}
	}

	if boneMapObj != nil {
		body, err := boneMapObj.ReadRaw(r)
		if err == nil {
			m, err := ParseBoneMap(body, order)
			if err == nil {
				asset.BoneMap = m
			}
		}
	}

	if attachObj != nil {
		body, err := attachObj.ReadBody(r)
		if err != nil {
			asset.AttachmentsErr = err
		} else {
			at, err := ParseHSpriteAttachments(body, order)
			if err != nil {
				asset.AttachmentsErr = err
			} else {
				asset.Attachments = at
			}
		}
	}

	if slotObj != nil {
		body, err := slotObj.ReadBody(r)
		if err != nil {
			asset.AttachSlotsErr = err
		} else {
			sl, err := ParseCSpriteAttachSlots(body, order)
			if err != nil {
				asset.AttachSlotsErr = err
			} else {
				asset.AttachSlots = sl
			}
		}
	}

	// The 0x2910 animation table is a direct child of the CSprite.
	if asset.ObjectType == 0x2700 {
		for _, c := range obj.Children {
			if uint16(c.Header.ObjectType) != 0x2910 || c.Header.ObjectVersion != 3 {
				continue
			}
			body, err := c.ReadBody(r)
			if err == nil {
				asset.AnimTable, err = ParseCSpriteAnimations(body, c.Header.ObjectVersion, order)
			}
			if err != nil {
				asset.AnimTable, asset.AnimTableErr = nil, err
			}
			break
		}
	}

	for _, aObj := range actionObjs {
		// Read the full raw body (ReadRaw bypasses child-object bookkeeping, giving us all
		// ObjectSize bytes regardless of what the parser consumed as sub-objects).
		// ObjectVersion is the format-version indicator for 0x2600 ActionSet objects —
		// NumberOfSubObjects is always 0 (no actual children), consistent with HSpriteHierarchy
		// which also uses ObjectVersion as its version discriminant.
		body, err := aObj.ReadRaw(r)
		if err != nil {
			continue
		}
		a, err := ParseActionSet(bytes.NewReader(body), order, int32(aObj.Header.ObjectVersion))
		if err == nil && len(a.Channels) > 0 {
			asset.Actions = append(asset.Actions, a)
		}
	}

	return asset, nil
}

func collectInternal(obj *ESFObject, prims *[]*ESFObject, matPal **ESFObject, hier **ESFObject, actions *[]*ESFObject) {
	for _, child := range obj.Children {
		switch uint16(child.Header.ObjectType) {
		case 0x1200, 0x1210:
			*prims = append(*prims, child)
		case 0x1110:
			if *matPal == nil {
				*matPal = child
			}
		case 0x2400:
			if *hier == nil {
				*hier = child
			}
		case 0x2600:
			*actions = append(*actions, child)
		}
		collectInternal(child, prims, matPal, hier, actions)
	}
}

// findSiblingAfter locates the first object of type objType that appears after
// anchor in anchor's own child list — the way the engine's sequential parser
// consumes an HSprite's trailing objects (0x5000 BoneMap, 0x2500 attachments)
// right after the 0x2400 hierarchy.  Returns nil when anchor is absent or no
// such sibling exists.  Scoping to anchor's siblings matters: a sprite also
// carries unrelated objects of the same type at container level (e.g. 0x5000
// sound/effect RefMaps), and "first match anywhere" would pick the wrong one.
func findSiblingAfter(root *ESFObject, anchor *ESFObject, objType uint16) *ESFObject {
	if anchor == nil {
		return nil
	}
	var search func(obj *ESFObject) *ESFObject
	search = func(obj *ESFObject) *ESFObject {
		anchorIdx := -1
		for i, child := range obj.Children {
			if child == anchor {
				anchorIdx = i
				break
			}
		}
		if anchorIdx >= 0 {
			for _, child := range obj.Children[anchorIdx+1:] {
				if uint16(child.Header.ObjectType) == objType {
					return child
				}
			}
			return nil
		}
		for _, child := range obj.Children {
			if found := search(child); found != nil {
				return found
			}
		}
		return nil
	}
	return search(root)
}
