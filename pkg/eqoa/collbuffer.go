package eqoa

import (
	"encoding/binary"
	"fmt"
	"math"
)

// CollBuffer is a decoded zone collision buffer (ESF object type 0x4200).
//
// The on-disk 0x4200 body is a SIMPLE SEQUENTIAL STREAM, not the packed
// rooms/groups/verts BVH the runtime builds from it. The layout was recovered
// from the client deserializer ParseCollBuffer__10VIESFParse and verified to
// consume every byte of all 371 CollBuffers in ZONE0107.ESF exactly.
//
// Stream layout (little-endian on disc), where `ver` is the ESF object header's
// ObjectVersion field:
//
//	if ver >= 2 { type = i32 }  else { type = 0 }   // vertex encoding 0/1/2
//	a         = i32                                  // Init arg (unused for geometry)
//	numStrips = i32                                  // number of strip records
//	c         = i32                                  // Init arg (unused for geometry)
//	if ver >= 2 { packing = i32 } else { packing = 0 }
//	// scale is DERIVED, not stored: scale = 1 / 2^packing
//	for s in 0..numStrips {
//	    vertCount = i32
//	    groupId   = i32   // surface/material group; drives BeginPrimGroup in client
//	    beginArg  = i32   // per-strip Begin() arg (surface/flags; unused for geometry)
//	    for v in 0..vertCount {
//	        type 0: pos = { f32, f32, f32 }                       // 12 bytes, full precision
//	        type 1: pos = { i16, i16, i16 } * scale               // 6 bytes, quantized
//	        type 2: q = { i16, i16, i16 }, k = i16                // 8 bytes
//	                pos = q*scale + baseVert[k]  (or, in the rare grouped
//	                sub-variant, k is a VertexGroup id and baseVert is not added)
//	    }
//	}
//
// Coordinates are EQOA world space: x=East, y=Height, z=North.
//
// Topology (confirmed from CollideBufferV__9VICollide...): each strip record is a
// triangle STRIP of vertCount verts -> (vertCount-2) triangles with alternating
// winding (even i => v[i],v[i+1],v[i+2]; odd i => v[i+1],v[i],v[i+2]).
type CollBuffer struct {
	Type      int32   // 0, 1, or 2 — vertex encoding
	Packing   int32   // quantization shift; Scale = 1/2^Packing
	Scale     float32 // derived quantization scale
	Positions [][3]float32
	// Triangles is a flat triangle-list index buffer (3 indices per triangle),
	// already expanded from the per-strip triangle strips with corrected winding.
	Triangles []uint32
	// Strips records, for each strip, the number of vertices it contributed. The
	// verts of strip s occupy a contiguous run in Positions; useful for callers
	// that want to preserve strip boundaries.
	Strips []CollStrip
}

// CollStrip describes one triangle-strip record inside a CollBuffer.
type CollStrip struct {
	VertStart int    // index of first vertex in CollBuffer.Positions
	VertCount int    // number of vertices in the strip
	GroupID   uint32 // surface/material group id
}

// ParseCollBuffer decodes a 0x4200 CollBuffer object body.
//
// version is the ESF object header's ObjectVersion (needed because type/packing
// are only present in the stream for version >= 2).
//
// baseVerts is the type-2 base-vertex table: the zone's ZonePreTranslations
// (0x3250) array, indexed by the per-vertex baseIdx k (stride vec3, world space).
// This was verified against the client: ParseCollBuffer__10VIESFParse reads the
// base from *(VIZone+0x78)+k*0xc, and ParseZonePreTranslations__10VIESFParse is
// the sole writer of that VIZone+0x74/+0x78 array. Pass the full unfiltered
// 0x3250 list. Pass nil when unavailable; type-2 vertices then decode with a zero
// base (correct only where every baseIdx is 0). Out-of-range indices fall back to
// zero.
func ParseCollBuffer(body []byte, order binary.ByteOrder, version int, baseVerts [][3]float32) (*CollBuffer, error) {
	p := 0
	need := func(n int) bool { return p+n <= len(body) }

	ri32 := func() int32 { v := int32(order.Uint32(body[p:])); p += 4; return v }
	ri16 := func() int16 { v := int16(order.Uint16(body[p:])); p += 2; return v }
	rf32 := func() float32 { v := math.Float32frombits(order.Uint32(body[p:])); p += 4; return v }

	headerWords := 4
	if version >= 2 {
		headerWords = 5
	}
	if !need(headerWords * 4) {
		return nil, fmt.Errorf("CollBuffer body too short for header: %d bytes", len(body))
	}

	cb := &CollBuffer{}
	if version >= 2 {
		cb.Type = ri32()
	}
	_ = ri32() // a (Init arg)
	numStrips := ri32()
	_ = ri32() // c (Init arg)
	if version >= 2 {
		cb.Packing = ri32()
	}
	if cb.Type < 0 || cb.Type > 2 {
		return nil, fmt.Errorf("CollBuffer unknown vertex type %d", cb.Type)
	}
	if numStrips < 0 {
		return nil, fmt.Errorf("CollBuffer negative strip count %d", numStrips)
	}
	cb.Scale = float32(1.0) / float32(math.Pow(2, float64(cb.Packing)))

	for s := int32(0); s < numStrips; s++ {
		if !need(12) {
			return nil, fmt.Errorf("CollBuffer truncated at strip %d header", s)
		}
		vertCount := ri32()
		groupID := uint32(ri32())
		_ = ri32() // beginArg
		if vertCount < 0 {
			return nil, fmt.Errorf("CollBuffer strip %d negative vertCount %d", s, vertCount)
		}

		vertStart := len(cb.Positions)
		for v := int32(0); v < vertCount; v++ {
			var pos [3]float32
			switch cb.Type {
			case 0:
				if !need(12) {
					return nil, fmt.Errorf("CollBuffer truncated in type-0 vertex")
				}
				pos[0], pos[1], pos[2] = rf32(), rf32(), rf32()
			case 1:
				if !need(6) {
					return nil, fmt.Errorf("CollBuffer truncated in type-1 vertex")
				}
				pos[0] = float32(ri16()) * cb.Scale
				pos[1] = float32(ri16()) * cb.Scale
				pos[2] = float32(ri16()) * cb.Scale
			case 2:
				if !need(8) {
					return nil, fmt.Errorf("CollBuffer truncated in type-2 vertex")
				}
				qx := float32(ri16()) * cb.Scale
				qy := float32(ri16()) * cb.Scale
				qz := float32(ri16()) * cb.Scale
				k := int(ri16())
				var base [3]float32
				// k indexes the ZonePreTranslations base pool (standard sub-variant,
				// client *(collbuffer+8)==0). A rare grouped sub-variant reuses this
				// field as a VertexGroup id and adds no base; it is not present in the
				// observed zone data (adding the base yields a coherent world-space
				// hull rather than the scatter a mis-added group id would produce), so
				// only the standard decode is implemented here.
				if baseVerts != nil && k >= 0 && k < len(baseVerts) {
					base = baseVerts[k]
				}
				pos[0] = qx + base[0]
				pos[1] = qy + base[1]
				pos[2] = qz + base[2]
			}
			cb.Positions = append(cb.Positions, pos)
		}

		cb.Strips = append(cb.Strips, CollStrip{
			VertStart: vertStart,
			VertCount: int(vertCount),
			GroupID:   groupID,
		})

		// Expand this strip into a triangle list with alternating winding.
		for i := 0; i+2 < int(vertCount); i++ {
			a := uint32(vertStart + i)
			b := uint32(vertStart + i + 1)
			c := uint32(vertStart + i + 2)
			if i%2 == 0 {
				cb.Triangles = append(cb.Triangles, a, b, c)
			} else {
				cb.Triangles = append(cb.Triangles, b, a, c)
			}
		}
	}

	return cb, nil
}
