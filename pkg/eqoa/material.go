package eqoa

import (
	"encoding/binary"
	"math"
)

type MaterialLayer struct {
	Flags     int32
	TexID     uint32
	WrapMode  int32
	BlendMode int32
	Color     [4]float32
	// Scroll is the layer's per-frame UV scroll rate (u, v), read from the last
	// 8 bytes of the 68-byte layer. A non-zero scroll is the engine's marker for
	// an ANIMATED surface — scrolling water, lava and cloud layers use it — so it
	// is the reliable signal for "this is water" in the zone exporter (texture
	// DictIDs are per-zone; the scroll rate is intrinsic to the material).
	Scroll [2]float32
}

type Material struct {
	DictID uint32
	Layers []MaterialLayer
}

// layerStride is the on-disk size of one material layer: flags, texture DictID, wrap and blend
// modes, RGBA modulate, a 3x3 UV matrix, an LOD bias and a 2-float UV scroll rate.
const layerStride = 4 + 4 + 4 + 4 + 4 + 36 + 4 + 8 // = 68

// ParseMaterialBody decodes a material and its layers.
//
// The header layout is taken from the client's own ParseMaterial__10VIESFParse (0x0040c6d8 in
// the retail SLUS char-creation snapshot), which reads:
//
//	u32 numLayers
//	u32 flags          (version > 1)
//	RGBA emissive      (version > 2)
//	layer[numLayers]   (68 bytes each)
//
// This previously read a DictID first whenever version > 1, which shifted everything by four
// bytes on the v=3 materials that characters use. numLayers then came out as 0, the loop never
// ran, and the numLayers==0 fallback below picked up what happened to be layer 0's texture ID.
// Single-texture lookups therefore worked by accident while every additional layer -- and every
// layer colour, wrap mode and blend mode -- was silently discarded.
//
// The dropped colour is the per-race skin tone: it is how an Erudite gets (109,83,77) on the
// same flesh textures a Human wears at (255,255,255). Losing it is why exported Erudites had a
// correct dark face on a pale body.
func ParseMaterialBody(data []byte, version int16, order binary.ByteOrder) (*Material, error) {
	m := &Material{}
	if len(data) < 4 {
		return m, nil
	}

	offset := 0
	numLayers := int(order.Uint32(data[0:4]))
	offset += 4
	if version > 1 {
		offset += 4 // flags
	}
	if version > 2 {
		offset += 4 // emissive RGBA
	}

	// Guard against reading a length out of a body that does not match this layout. Zone
	// materials reach the same function, and a bad count here would either allocate wildly or
	// walk off the end -- so anything that does not fit falls through to the legacy path below
	// rather than being trusted.
	if numLayers < 0 || numLayers > 16 || offset+numLayers*layerStride > len(data) {
		numLayers = 0
		offset = 0
		if version > 1 && len(data) >= 4 {
			m.DictID = order.Uint32(data[0:4])
			offset += 4
		}
		if len(data) >= offset+4 {
			numLayers = int(order.Uint32(data[offset : offset+4]))
			offset += 4
		}
		if version > 1 {
			offset += 4
		}
		if version > 2 {
			offset += 4
		}
		if numLayers < 0 || numLayers > 16 {
			numLayers = 0
		}
	}

	for i := 0; i < numLayers; i++ {
		if len(data) < offset+layerStride {
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

		// UV scroll rate — the last 8 bytes of the 68-byte layer (after the 3x3
		// UV matrix and the LOD bias). Non-zero = an animated surface.
		layer.Scroll = [2]float32{
			math.Float32frombits(order.Uint32(data[offset+60 : offset+64])),
			math.Float32frombits(order.Uint32(data[offset+64 : offset+68])),
		}

		offset += layerStride
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
