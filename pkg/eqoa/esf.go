package eqoa

import (
	"encoding/binary"
	"fmt"
	"io"
)

type ESFFileHeader struct {
	Magic           [4]byte
	NumberOfObjects int32
	FileType        int32
	Unknown1        int32
	Offset          int64
	Unknown2        int64
}

const (
	ESFHeaderSize    = 32
	ObjectHeaderSize = 12
	MagicOBJF        = "OBJF"
	MagicFJBO        = "FJBO"
)

type ObjectHeader struct {
	ObjectType         int16
	ObjectVersion      int16
	ObjectSize         int32
	NumberOfSubObjects int32
}

type ESFObject struct {
	Header     ObjectHeader
	Offset     int64 // Absolute offset in ESF stream
	BodyOffset int64 // Offset where the object's own body starts (after sub-objects)
	BodySize   int32 // Size of the object's own body
	Children   []*ESFObject
	DictID     uint32
	IsZlib     bool
}

// objTypeInfoEntry is the single source of truth for per-object-type metadata.
// Previously this knowledge was duplicated across ObjTypeNames (name lookup),
// the DictID-extraction switch in ReadObject, and the ReadRaw rationale comment.
type objTypeInfoEntry struct {
	// name is the human-readable object-type name.
	name string
	// extractsDictID is true for types whose first 4 body bytes hold a dictionary
	// ID that ReadObject registers in the ESF dictionary map.
	extractsDictID bool
	// subObjsAreFormatFlag is true for types where ObjectHeader.NumberOfSubObjects
	// is a format-version indicator rather than a real nested-object count
	// (e.g. 0x2600 HSpriteAnim/ActionSet, 0xC000 ParticleDefinition). Documentation
	// only for now: parsing of these types is handled by ReadRaw at the call site
	// and this flag is NOT (yet) consulted to change parsing behavior.
	subObjsAreFormatFlag bool
}

// objTypeInfo is the centralized ESF object-type table. It reproduces exactly the
// former ObjTypeNames map (names), the former DictID-extraction switch set
// (extractsDictID), and the ReadRaw format-flag types (subObjsAreFormatFlag).
var objTypeInfo = map[uint16]objTypeInfoEntry{
	0x1000: {name: "Surface", extractsDictID: true},
	0x1001: {name: "SurfaceArray"},
	0x1100: {name: "Material", extractsDictID: true},
	0x1101: {name: "MaterialArray"},
	0x1110: {name: "MaterialPalette"},
	0x1111: {name: "MaterialPaletteHeader"},
	0x1200: {name: "PrimBuffer", extractsDictID: true},
	0x1210: {name: "SkinPrimBuffer", extractsDictID: true},
	0x2000: {name: "SimpleSprite", extractsDictID: true},
	0x2001: {name: "SimpleSpriteHeader"},
	0x2200: {name: "HSprite", extractsDictID: true},
	0x2210: {name: "HSpriteHeader"},
	0x2220: {name: "HSpriteArray"},
	0x2310: {name: "SimpleSubSprite", extractsDictID: true},
	0x2311: {name: "SimpleSubSpriteHeader"},
	0x2320: {name: "SkinSubSprite", extractsDictID: true},
	0x2321: {name: "SkinSubSprite2"},
	0x2400: {name: "HSpriteHierarchy"},
	0x2450: {name: "HSpriteTriggers"},
	0x2500: {name: "HSpriteAttachments"},
	0x2600: {name: "HSpriteAnim", subObjsAreFormatFlag: true},
	0x2700: {name: "CSprite", extractsDictID: true},
	0x2710: {name: "CSpriteHeader"},
	0x2800: {name: "CSpriteArray"},
	0x2a10: {name: "LODSprite", extractsDictID: true},
	0x2a20: {name: "LodSpriteArray"},
	0x2b00: {name: "PointLight"},
	0x2c00: {name: "GroupSprite", extractsDictID: true},
	0x2c10: {name: "GroupSpriteHeader"},
	0x2c20: {name: "GroupSpriteArray"},
	0x2c30: {name: "GroupSpriteMembers", extractsDictID: true},
	0x2d00: {name: "PointSprite"},
	0x2e00: {name: "StreamAudioSprite"},
	0x2e10: {name: "StreamAudioSpriteHeader"},
	0x2f00: {name: "FloraSprite"},
	0x3000: {name: "Zone"},
	0x3100: {name: "ZoneResources"},
	0x3200: {name: "ZoneBase"},
	0x3220: {name: "ZoneTree"},
	0x3230: {name: "ZoneRooms"},
	0x3240: {name: "ZoneRoom", extractsDictID: true},
	0x3250: {name: "ZonePreTranslations"},
	0x3270: {name: "ZoneRoomActors"},
	0x3280: {name: "ZoneRoomActors2"},
	0x3290: {name: "ZoneActors"},
	0x32a0: {name: "ZoneRoomStaticLightings2"},
	0x32b0: {name: "ZoneStaticLightnings"},
	0x32c0: {name: "ZoneStaticTable"},
	0x32d0: {name: "ZoneFlora"},
	0x4200: {name: "CollBuffer"},
	0x5000: {name: "RefMap"},
	0x6000: {name: "ZoneActor", extractsDictID: true},
	0x6010: {name: "StaticLighting"},
	0x6020: {name: "StaticLightingObj", extractsDictID: true},
	0x6030: {name: "ZoneRoomStaticLightings3"},
	0x6040: {name: "ZoneRoomActors3"},
	0x7000: {name: "Font"},
	0x8000: {name: "Root"},
	0x8100: {name: "World"},
	0x8200: {name: "WorldBase"},
	0x8210: {name: "WorldZoneProxies"},
	0x8220: {name: "WorldBaseHeader"},
	0x8230: {name: "WorldTree"},
	0x8240: {name: "WorldRegions"},
	0x9000: {name: "ResourceTable"},
	0xa000: {name: "ResourceDir", extractsDictID: true},
	0xa010: {name: "ResourceDir2"},
	0xb000: {name: "Adpcm", extractsDictID: true},
	0xb010: {name: "AdpcmHeader"},
	0xb020: {name: "AdpcmSampleData"},
	0xb030: {name: "Xm", extractsDictID: true},
	0xb040: {name: "XmHeader"},
	0xb060: {name: "XmSampleData"},
	0xb100: {name: "SoundSprite", extractsDictID: true},
	0xc000: {name: "ParticleDefinition", subObjsAreFormatFlag: true},
	0xc100: {name: "ParticleSprite"},
	0xc101: {name: "ParticleSpriteHeader"},
	0xc200: {name: "SpellEffect"},
	0xc300: {name: "EffectVolumeSprite"},
	0xc310: {name: "EffectVolumeSpriteHeader"},
}

// ObjTypeNames is a thin derived view of objTypeInfo (object type -> name),
// retained for backward compatibility with external references (see docs/FORMATS.md).
// Keys are int to match the historical map type.
var ObjTypeNames = func() map[int]string {
	m := make(map[int]string, len(objTypeInfo))
	for t, info := range objTypeInfo {
		m[int(t)] = info.name
	}
	return m
}()

func GetObjectTypeName(objType int) string {
	// Preserve the historical int-keyed lookup exactly: the old ObjTypeNames map
	// had only non-negative keys (0x1000..0xC310), so any objType outside the
	// uint16 range (including negative int16 values sign-extended by callers)
	// missed and fell through to the hex fallback. Guard on range before masking
	// so that behavior is byte-identical.
	if objType >= 0 && objType <= 0xFFFF {
		if info, ok := objTypeInfo[uint16(objType)]; ok {
			return info.name
		}
	}
	return fmt.Sprintf("Unknown(0x%04X)", objType)
}

func ParseESF(r io.ReadSeeker) (*ESFFileHeader, []*ESFObject, map[uint32]*ESFObject, binary.ByteOrder, error) {
	var magic [4]byte
	if err := binary.Read(r, binary.BigEndian, &magic); err != nil {
		return nil, nil, nil, nil, err
	}

	var order binary.ByteOrder
	if string(magic[:]) == MagicOBJF {
		order = binary.BigEndian
	} else if magic[0] == 'F' && magic[1] == 'J' && magic[2] == 'B' && magic[3] == 'O' {
		order = binary.LittleEndian
	} else {
		return nil, nil, nil, nil, fmt.Errorf("invalid ESF magic: %s", string(magic[:]))
	}

	remainingHeader := struct {
		NumberOfObjects int32
		FileType        int32
		Unknown1        int32
		Offset          int64
		Unknown2        int64
	}{}

	if err := binary.Read(r, order, &remainingHeader); err != nil {
		return nil, nil, nil, nil, err
	}

	header := &ESFFileHeader{
		Magic:           magic,
		NumberOfObjects: remainingHeader.NumberOfObjects,
		FileType:        remainingHeader.FileType,
		Unknown1:        remainingHeader.Unknown1,
		Offset:          remainingHeader.Offset,
		Unknown2:        remainingHeader.Unknown2,
	}

	dictionary := make(map[uint32]*ESFObject)
	objects := make([]*ESFObject, 0, header.NumberOfObjects)
	currentOffset := int64(ESFHeaderSize)
	for i := 0; i < int(header.NumberOfObjects); i++ {
		obj, consumed, err := ReadObject(r, order, currentOffset, dictionary)
		if err != nil {
			return header, objects, dictionary, order, err
		}
		objects = append(objects, obj)
		currentOffset += consumed
	}

	return header, objects, dictionary, order, nil
}

func ReadObject(r io.ReadSeeker, order binary.ByteOrder, offset int64, dict map[uint32]*ESFObject) (*ESFObject, int64, error) {
	if _, err := r.Seek(offset, io.SeekStart); err != nil {
		return nil, 0, err
	}

	var h ObjectHeader
	if err := binary.Read(r, order, &h); err != nil {
		return nil, 0, err
	}

	obj := &ESFObject{
		Header:   h,
		Offset:   offset,
		Children: make([]*ESFObject, 0, h.NumberOfSubObjects),
	}

	consumedBySubObjects := int64(0)
	for i := 0; i < int(h.NumberOfSubObjects); i++ {
		child, consumed, err := ReadObject(r, order, offset+int64(ObjectHeaderSize)+consumedBySubObjects, dict)
		if err != nil {
			return nil, 0, err
		}
		obj.Children = append(obj.Children, child)
		consumedBySubObjects += consumed
	}

	obj.BodyOffset = offset + int64(ObjectHeaderSize) + consumedBySubObjects
	obj.BodySize = h.ObjectSize - int32(consumedBySubObjects)

	if obj.BodySize < 0 {
		return nil, 0, fmt.Errorf("object size mismatch at 0x%X: size %d, subobjects consumed %d", offset, h.ObjectSize, consumedBySubObjects)
	}

	// Extract DictID if present (usually first 4 bytes of body)
	if obj.BodySize >= 4 {
		r.Seek(obj.BodyOffset, io.SeekStart)
		var id uint32
		binary.Read(r, order, &id)

		// For the subset of object types whose first 4 body bytes hold a
		// dictionary ID (objTypeInfo[...].extractsDictID), register the object
		// in the ESF dictionary map. Types not marked extractsDictID reuse those
		// first 4 bytes for other purposes and must not be indexed.
		if info, ok := objTypeInfo[uint16(h.ObjectType)]; ok && info.extractsDictID {
			if id != 0 {
				obj.DictID = id
				dict[id] = obj
			}
		}

		// Check for nested zlib compression
		r.Seek(obj.BodyOffset, io.SeekStart)
		peek := make([]byte, 2)
		binary.Read(r, binary.BigEndian, &peek)
		if HasZlibHeader(peek) {
			obj.IsZlib = true
		}
	} else if obj.BodySize >= 2 {
		r.Seek(obj.BodyOffset, io.SeekStart)
		peek := make([]byte, 2)
		binary.Read(r, binary.BigEndian, &peek)
		if HasZlibHeader(peek) {
			obj.IsZlib = true
		}
	}

	// Seek to the end of this object to continue parsing
	r.Seek(offset+int64(ObjectHeaderSize)+int64(h.ObjectSize), io.SeekStart)

	return obj, int64(ObjectHeaderSize) + int64(h.ObjectSize), nil
}

// ReadRaw reads the entire ObjectSize bytes starting immediately after the 12-byte object
// header, bypassing any child-object bookkeeping. Used for objects where NumberOfSubObjects
// is a format-version indicator rather than an actual nested-object count (e.g. 0x2600).
func (obj *ESFObject) ReadRaw(r io.ReadSeeker) ([]byte, error) {
	size := int(obj.Header.ObjectSize)
	if size <= 0 {
		return []byte{}, nil
	}
	if _, err := r.Seek(obj.Offset+ObjectHeaderSize, io.SeekStart); err != nil {
		return nil, err
	}
	data := make([]byte, size)
	_, err := io.ReadFull(r, data)
	return data, err
}

func (obj *ESFObject) ReadBody(r io.ReadSeeker) ([]byte, error) {
	if obj.BodySize <= 0 {
		return []byte{}, nil
	}
	data := make([]byte, obj.BodySize)
	if _, err := r.Seek(obj.BodyOffset, io.SeekStart); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, err
	}

	if obj.IsZlib {
		decompressed, err := DecompressZlib(data)
		if err == nil {
			return decompressed, nil
		}
		// Fallback to raw data if decompression fails
	}

	return data, nil
}
