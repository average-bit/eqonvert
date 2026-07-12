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

var ObjTypeNames = map[int]string{
	0x1000: "Surface",
	0x1001: "SurfaceArray",
	0x1100: "Material",
	0x1101: "MaterialArray",
	0x1110: "MaterialPalette",
	0x1111: "MaterialPaletteHeader",
	0x1200: "PrimBuffer",
	0x1210: "SkinPrimBuffer",
	0x2000: "SimpleSprite",
	0x2001: "SimpleSpriteHeader",
	0x2200: "HSprite",
	0x2210: "HSpriteHeader",
	0x2220: "HSpriteArray",
	0x2310: "SimpleSubSprite",
	0x2311: "SimpleSubSpriteHeader",
	0x2320: "SkinSubSprite",
	0x2321: "SkinSubSprite2",
	0x2400: "HSpriteHierarchy",
	0x2450: "HSpriteTriggers",
	0x2500: "HSpriteAttachments",
	0x2600: "HSpriteAnim",
	0x2700: "CSprite",
	0x2710: "CSpriteHeader",
	0x2800: "CSpriteArray",
	0x2a10: "LODSprite",
	0x2a20: "LodSpriteArray",
	0x2b00: "PointLight",
	0x2c00: "GroupSprite",
	0x2c10: "GroupSpriteHeader",
	0x2c20: "GroupSpriteArray",
	0x2c30: "GroupSpriteMembers",
	0x2d00: "PointSprite",
	0x2e00: "StreamAudioSprite",
	0x2e10: "StreamAudioSpriteHeader",
	0x2f00: "FloraSprite",
	0x3000: "Zone",
	0x3100: "ZoneResources",
	0x3200: "ZoneBase",
	0x3220: "ZoneTree",
	0x3230: "ZoneRooms",
	0x3240: "ZoneRoom",
	0x3250: "ZonePreTranslations",
	0x3270: "ZoneRoomActors",
	0x3280: "ZoneRoomActors2",
	0x3290: "ZoneActors",
	0x32a0: "ZoneRoomStaticLightings2",
	0x32b0: "ZoneStaticLightnings",
	0x32c0: "ZoneStaticTable",
	0x32d0: "ZoneFlora",
	0x4200: "CollBuffer",
	0x5000: "RefMap",
	0x6000: "ZoneActor",
	0x6010: "StaticLighting",
	0x6020: "StaticLightingObj",
	0x6030: "ZoneRoomStaticLightings3",
	0x6040: "ZoneRoomActors3",
	0x7000: "Font",
	0x8000: "Root",
	0x8100: "World",
	0x8200: "WorldBase",
	0x8210: "WorldZoneProxies",
	0x8220: "WorldBaseHeader",
	0x8230: "WorldTree",
	0x8240: "WorldRegions",
	0x9000: "ResourceTable",
	0xa000: "ResourceDir",
	0xa010: "ResourceDir2",
	0xb000: "Adpcm",
	0xb010: "AdpcmHeader",
	0xb020: "AdpcmSampleData",
	0xb030: "Xm",
	0xb040: "XmHeader",
	0xb060: "XmSampleData",
	0xb100: "SoundSprite",
	0xc000: "ParticleDefinition",
	0xc100: "ParticleSprite",
	0xc101: "ParticleSpriteHeader",
	0xc200: "SpellEffect",
	0xc300: "EffectVolumeSprite",
	0xc310: "EffectVolumeSpriteHeader",
}

func GetObjectTypeName(objType int) string {
	if name, ok := ObjTypeNames[objType]; ok {
		return name
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

		switch uint16(h.ObjectType) {
		case 0x1000, 0x1100, 0x1200, 0x1210, 0x2000, 0x2200, 0x2700, 0x2C00, 0x2A10, 0x2310, 0x2320, 0x2C30, 0xA000, 0xB000, 0xB030, 0xB100, 0x6000, 0x6020, 0x3240:
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
