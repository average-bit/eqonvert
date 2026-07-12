package eqoa

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
)

type Vertex struct {
	Pos     [3]float32
	UV      [2]float32
	Normal  [3]float32
	Color   [4]float32
	Joints  [4]uint8
	Weights [4]uint8
	VGroup  int16
}

type FaceGroup struct {
	MaterialIndex int32
	Vertices      []Vertex
	Indices       []uint32
}

type Mesh struct {
	Type       int32
	PackingPos float32
	PackingUV  float32
	FaceGroups []FaceGroup
}

func ParsePrimBuffer(r io.ReadSeeker, obj *ESFObject, order binary.ByteOrder) (*Mesh, error) {
	data, err := obj.ReadBody(r)
	if err != nil {
		return nil, err
	}

	if len(data) < 28 {
		return nil, fmt.Errorf("PrimBuffer data too short for header: %d", len(data))
	}

	br := bytes.NewReader(data)

	var pbType, nmats, numFaceGroups, totalVerts, p1, p2, p3 int32
	if obj.Header.ObjectVersion > 1 {
		var dictID uint32
		binary.Read(br, order, &dictID)
	}

	binary.Read(br, order, &pbType)
	binary.Read(br, order, &nmats)
	binary.Read(br, order, &numFaceGroups)
	binary.Read(br, order, &totalVerts)
	binary.Read(br, order, &p1)
	binary.Read(br, order, &p2)
	binary.Read(br, order, &p3)

	// Sanity checks
	if numFaceGroups < 0 || numFaceGroups > 10000 {
		return nil, fmt.Errorf("invalid numFaceGroups: %d", numFaceGroups)
	}
	if p1 < 0 || p1 > 31 || p2 < 0 || p2 > 31 {
		return nil, fmt.Errorf("invalid packing factors: p1=%d, p2=%d", p1, p2)
	}

	m := &Mesh{
		Type:       pbType,
		PackingPos: float32(1.0 / math.Pow(2, float64(p1))),
		PackingUV:  float32(1.0 / math.Pow(2, float64(p2))),
		FaceGroups: make([]FaceGroup, 0, numFaceGroups),
	}

	for fi := 0; fi < int(numFaceGroups); fi++ {
		var numVerts, matIdx int32
		if err := binary.Read(br, order, &numVerts); err != nil {
			break
		}
		binary.Read(br, order, &matIdx)

		if numVerts < 0 || numVerts > 10000 {
			return nil, fmt.Errorf("invalid numVerts: %d in face group %d", numVerts, fi)
		}

		fg := FaceGroup{
			MaterialIndex: matIdx,
			Vertices:      make([]Vertex, 0, numVerts),
		}

		for i := 0; i < int(numVerts); i++ {
			// Read coords
			var x, y, z, u, v int16
			binary.Read(br, order, &x)
			binary.Read(br, order, &y)
			binary.Read(br, order, &z)
			binary.Read(br, order, &u)
			binary.Read(br, order, &v)

			// Read normal
			var nx, ny, nz int8
			binary.Read(br, order, &nx)
			binary.Read(br, order, &ny)
			binary.Read(br, order, &nz)

			// Read next 4 bytes (Color or Joint Indices)
			var b1, b2, b3, b4 uint8
			binary.Read(br, order, &b1)
			binary.Read(br, order, &b2)
			binary.Read(br, order, &b3)
			binary.Read(br, order, &b4)

			vgroup := int16(0)
			color := [4]float32{1, 1, 1, 1}
			joints := [4]uint8{0, 0, 0, 0}
			weights := [4]uint8{0, 0, 0, 0}

			if pbType == 5 {
				// Skinned mesh: b1-b4 are joints, next 4 bytes are weights
				joints = [4]uint8{b1, b2, b3, b4}
				binary.Read(br, order, &weights)
			} else {
				// Static mesh: b1-b4 are color (RGBA)
				// PS2 GS vertex colors: 0-128 per channel where 128 = full intensity.
				clamp1 := func(v float32) float32 {
					if v > 1.0 {
						return 1.0
					}
					return v
				}
				color = [4]float32{
					clamp1(float32(b1) / 128.0),
					clamp1(float32(b2) / 128.0),
					clamp1(float32(b3) / 128.0),
					clamp1(float32(b4) / 128.0),
				}
				if pbType == 4 {
					binary.Read(br, order, &vgroup)
				}
			}

			fg.Vertices = append(fg.Vertices, Vertex{
				Pos:     [3]float32{float32(x) * m.PackingPos, float32(y) * m.PackingPos, float32(z) * m.PackingPos},
				UV:      [2]float32{float32(u) * m.PackingUV, float32(v) * m.PackingUV},
				Normal:  [3]float32{float32(nx) / 127.0, float32(ny) / 127.0, float32(nz) / 127.0},
				Color:   color,
				Joints:  joints,
				Weights: weights,
				VGroup:  vgroup,
			})
		}

		if len(fg.Vertices) >= 3 {
			for i := 0; i < len(fg.Vertices)-2; i++ {
				if i%2 == 0 {
					fg.Indices = append(fg.Indices, uint32(i), uint32(i+1), uint32(i+2))
				} else {
					fg.Indices = append(fg.Indices, uint32(i), uint32(i+2), uint32(i+1))
				}
			}
		}
		m.FaceGroups = append(m.FaceGroups, fg)
	}

	return m, nil
}
