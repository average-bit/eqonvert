package eqoa

import (
	"bytes"
	"encoding/binary"
	"io"
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
}

func IsSprite(objType uint16) bool {
	switch objType {
	case 0x2000, 0x2700, 0x2200, 0x2310, 0x2320, 0x2C00, 0x2A10:
		return true
	}
	return false
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
	boneMapObj := findBoneMapSibling(obj, hierObj)

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

// findBoneMapSibling locates the 0x5000 BoneMap that belongs to the given
// hierarchy object: the first 0x5000 appearing after the hierarchy in its
// parent's child list.  Returns nil when the hierarchy is absent or no such
// sibling exists.
func findBoneMapSibling(root *ESFObject, hier *ESFObject) *ESFObject {
	if hier == nil {
		return nil
	}
	var search func(obj *ESFObject) *ESFObject
	search = func(obj *ESFObject) *ESFObject {
		hierIdx := -1
		for i, child := range obj.Children {
			if child == hier {
				hierIdx = i
				break
			}
		}
		if hierIdx >= 0 {
			for _, child := range obj.Children[hierIdx+1:] {
				if uint16(child.Header.ObjectType) == 0x5000 {
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
