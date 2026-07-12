package eqoa

import (
	"encoding/binary"
)

type MaterialLayer struct {
	Flags     int32
	TexID     uint32
	WrapMode  int32
	BlendMode int32
	Color     [4]float32
}

type Material struct {
	DictID uint32
	Layers []MaterialLayer
}

func ParseMaterialBody(data []byte, version int16, order binary.ByteOrder) (*Material, error) {
	offset := 0
	m := &Material{}
	if version > 1 {
		if len(data) >= 4 {
			m.DictID = order.Uint32(data[0:4])
			offset += 4
		}
	}

	if len(data) < offset+4 {
		return m, nil
	}

	numLayers := int(order.Uint32(data[offset : offset+4]))
	offset += 4

	if version > 1 {
		offset += 4 // Skip tessellate
	}
	if version > 2 {
		offset += 4 // Skip emissive color
	}

	for i := 0; i < numLayers; i++ {
		if len(data) < offset+40 {
			break
		}
		layer := MaterialLayer{}
		layer.Flags = int32(order.Uint32(data[offset : offset+4]))
		layer.TexID = order.Uint32(data[offset+4 : offset+8])
		layer.WrapMode = int32(order.Uint32(data[offset+8 : offset+12]))
		layer.BlendMode = int32(order.Uint32(data[offset+12 : offset+16]))
		// Zone terrain materials (ver=3) store the Surface DictID in Flags and a
		// small UV/wrap index in TexID. Character/item materials use Flags for
		// render flags (always small) and TexID for the actual Surface DictID
		// (always large). Normalize zone terrain: promote Flags to TexID so that
		// surface lookups work uniformly across all material types.
		if uint32(layer.Flags) > 0xFFFF && layer.TexID <= 0xFF {
			layer.TexID = uint32(layer.Flags)
		}

		cr := data[offset+16]
		cg := data[offset+17]
		cb := data[offset+18]
		ca := data[offset+19]
		layer.Color = [4]float32{float32(cr) / 255.0, float32(cg) / 255.0, float32(cb) / 255.0, float32(ca) / 255.0}

		offset += 20 + 36 + 12 // coords + uv_transform + lod/uv_rates
		m.Layers = append(m.Layers, layer)
	}

	// Zone-format materials (version 3, numLayers=0) store a direct surface
	// DictID at the layer-start offset instead of using the standard layer
	// structure. Synthesise a one-entry layer so texture lookup can proceed.
	if numLayers == 0 && len(data) >= offset+4 {
		if texID := order.Uint32(data[offset : offset+4]); texID != 0 {
			m.Layers = append(m.Layers, MaterialLayer{TexID: texID})
		}
	}

	return m, nil
}
