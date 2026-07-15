package eqoa

import (
	"encoding/binary"
	"io"
)

// ResourceRef points at one resolvable resource inside an ESF stream: the
// absolute byte offset of its object header and the object's total size
// (including the 12-byte header). It is the on-disk form of the client's
// VIResourceElem, parsed from a 0x9000 ResourceTable object.
type ResourceRef struct {
	Offset uint64
	Size   uint32
}

// ResourceTableObjType is the ESF object type that carries the DictID→offset
// directory the client's VIZone::Find uses to stream resources in on approach.
const ResourceTableObjType = 0x9000

// ParseResourceTables walks a parsed ESF tree and unions every 0x9000
// ResourceTable object into a single DictID→ResourceRef map.
//
// Body layout (confirmed on-disc against ARENA, AMBTRACK, TUNARIA, ITEM, CHAR):
//
//	count  u32
//	count × { offset u64, size u32, dictID u32 }   // 16 bytes each
//
// all in the file's byte order. `offset` is absolute within THIS ESF stream and
// points at a resolvable object's 12-byte header; `size` is the object's total
// size including that header. A single file may contain many tables (a
// monolithic world file like TUNARIA has one per zone) — first-seen DictID wins,
// matching the sprite-library merge policy elsewhere in the converter.
func ParseResourceTables(r io.ReadSeeker, objects []*ESFObject, order binary.ByteOrder) map[uint32]ResourceRef {
	dir := make(map[uint32]ResourceRef)
	var walk func(o *ESFObject)
	walk = func(o *ESFObject) {
		if uint16(o.Header.ObjectType) == ResourceTableObjType {
			parseResourceTableInto(r, o, order, dir)
			return
		}
		for _, c := range o.Children {
			walk(c)
		}
	}
	for _, o := range objects {
		walk(o)
	}
	return dir
}

// parseResourceTableInto reads one 0x9000 object body and adds its entries to
// dir (first-seen wins). Malformed / truncated tables are skipped silently.
func parseResourceTableInto(r io.ReadSeeker, obj *ESFObject, order binary.ByteOrder, dir map[uint32]ResourceRef) {
	body, err := obj.ReadBody(r)
	if err != nil || len(body) < 4 {
		return
	}
	count := int(order.Uint32(body[0:4]))
	const entrySize = 16
	for i := 0; i < count; i++ {
		base := 4 + i*entrySize
		if base+entrySize > len(body) {
			break // truncated table — take what parsed cleanly
		}
		off := order.Uint64(body[base : base+8])
		size := order.Uint32(body[base+8 : base+12])
		dictID := order.Uint32(body[base+12 : base+16])
		if dictID == 0 {
			continue
		}
		if _, exists := dir[dictID]; !exists {
			dir[dictID] = ResourceRef{Offset: off, Size: size}
		}
	}
}

// ReadObjectAt parses a single ESF object located at an absolute offset within
// the stream, resolving its geometry the same way ParseESF does (header + nested
// sub-objects + DictID). Use it to materialize a resource pointed at by a
// ResourceRef so it can be fed through LoadAsset. The returned object shares the
// caller's reader; callers must not seek concurrently.
func ReadObjectAt(r io.ReadSeeker, order binary.ByteOrder, offset int64) (*ESFObject, error) {
	dict := make(map[uint32]*ESFObject)
	obj, _, err := ReadObject(r, order, offset, dict)
	return obj, err
}
