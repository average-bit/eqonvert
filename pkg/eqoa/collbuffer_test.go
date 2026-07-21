package eqoa

import (
	"bytes"
	"encoding/binary"
	"io"
	"math"
	"os"
	"testing"
)

// buildCollBody encodes a version-2 CollBuffer stream for testing.
func buildCollBody(typ, packing int32, strips [][]([3]float32), quantScale float32) []byte {
	buf := new(bytes.Buffer)
	w := func(v int32) { binary.Write(buf, binary.LittleEndian, v) }
	w(typ)                // type
	w(0)                  // a
	w(int32(len(strips))) // numStrips
	w(0)                  // c
	w(packing)            // packing
	for _, strip := range strips {
		w(int32(len(strip))) // vertCount
		w(0)                 // groupId
		w(0)                 // beginArg
		for _, p := range strip {
			switch typ {
			case 0:
				binary.Write(buf, binary.LittleEndian, p[0])
				binary.Write(buf, binary.LittleEndian, p[1])
				binary.Write(buf, binary.LittleEndian, p[2])
			case 1:
				binary.Write(buf, binary.LittleEndian, int16(math.Round(float64(p[0]/quantScale))))
				binary.Write(buf, binary.LittleEndian, int16(math.Round(float64(p[1]/quantScale))))
				binary.Write(buf, binary.LittleEndian, int16(math.Round(float64(p[2]/quantScale))))
			case 2:
				binary.Write(buf, binary.LittleEndian, int16(math.Round(float64(p[0]/quantScale))))
				binary.Write(buf, binary.LittleEndian, int16(math.Round(float64(p[1]/quantScale))))
				binary.Write(buf, binary.LittleEndian, int16(math.Round(float64(p[2]/quantScale))))
				binary.Write(buf, binary.LittleEndian, int16(0)) // baseIdx 0
			}
		}
	}
	return buf.Bytes()
}

func TestParseCollBufferType0(t *testing.T) {
	// One strip of 4 verts -> 2 triangles, full-precision floats.
	strip := [][3]float32{{0, 0, 0}, {1, 0, 0}, {0, 0, 1}, {1, 0, 1}}
	body := buildCollBody(0, 0, [][]([3]float32){strip}, 1)

	cb, err := ParseCollBuffer(body, binary.LittleEndian, 2, nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cb.Type != 0 {
		t.Errorf("type = %d, want 0", cb.Type)
	}
	if len(cb.Positions) != 4 {
		t.Fatalf("positions = %d, want 4", len(cb.Positions))
	}
	if cb.Positions[1] != ([3]float32{1, 0, 0}) {
		t.Errorf("pos[1] = %v, want {1 0 0}", cb.Positions[1])
	}
	// 4-vert strip -> 2 triangles -> 6 indices.
	if len(cb.Triangles) != 6 {
		t.Fatalf("triangles = %d indices, want 6", len(cb.Triangles))
	}
	// even i=0 -> (0,1,2); odd i=1 -> (v[2],v[1],v[3]) = (2,1,3).
	wantTris := []uint32{0, 1, 2, 2, 1, 3}
	for i, v := range wantTris {
		if cb.Triangles[i] != v {
			t.Errorf("tri idx[%d] = %d, want %d (full=%v)", i, cb.Triangles[i], v, cb.Triangles)
			break
		}
	}
}

func TestParseCollBufferType1(t *testing.T) {
	// packing=5 -> scale = 1/32 = 0.03125, exactly representable multiples.
	scale := float32(1.0 / 32.0)
	strip := [][3]float32{
		{10, 0.03125 * 3, -5},
		{10.03125, 0, -5},
		{10, 0, -4.96875},
	}
	body := buildCollBody(1, 5, [][]([3]float32){strip}, scale)

	cb, err := ParseCollBuffer(body, binary.LittleEndian, 2, nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cb.Scale != scale {
		t.Errorf("scale = %v, want %v", cb.Scale, scale)
	}
	if len(cb.Positions) != 3 {
		t.Fatalf("positions = %d, want 3", len(cb.Positions))
	}
	for i, want := range strip {
		got := cb.Positions[i]
		for k := 0; k < 3; k++ {
			if math.Abs(float64(got[k]-want[k])) > 1e-4 {
				t.Errorf("pos[%d][%d] = %v, want %v", i, k, got[k], want[k])
			}
		}
	}
	// 3-vert strip -> exactly 1 triangle (0,1,2).
	if len(cb.Triangles) != 3 {
		t.Fatalf("triangles = %d indices, want 3", len(cb.Triangles))
	}
}

// buildType2Body encodes a version-2 type-2 CollBuffer: one strip whose vertices
// are (quantized delta, baseIdx k) pairs. pos = delta*scale + baseVerts[k].
func buildType2Body(packing int32, deltas [][3]float32, baseIdx []int16, scale float32) []byte {
	buf := new(bytes.Buffer)
	w := func(v int32) { binary.Write(buf, binary.LittleEndian, v) }
	w(2)                  // type
	w(0)                  // a
	w(1)                  // numStrips
	w(0)                  // c
	w(packing)            // packing
	w(int32(len(deltas))) // vertCount
	w(0)                  // groupId
	w(0)                  // beginArg
	for i, d := range deltas {
		binary.Write(buf, binary.LittleEndian, int16(math.Round(float64(d[0]/scale))))
		binary.Write(buf, binary.LittleEndian, int16(math.Round(float64(d[1]/scale))))
		binary.Write(buf, binary.LittleEndian, int16(math.Round(float64(d[2]/scale))))
		binary.Write(buf, binary.LittleEndian, baseIdx[i])
	}
	return buf.Bytes()
}

// TestParseCollBufferType2BaseAnchor covers the type-2 ZonePreTranslations
// anchoring recovered from ParseCollBuffer__10VIESFParse: each vertex is
// delta*scale + baseVerts[k]. Guards the fix that stopped type-2 collision from
// collapsing onto the origin.
func TestParseCollBufferType2BaseAnchor(t *testing.T) {
	scale := float32(1.0 / 16.0) // packing=4, exactly representable multiples
	deltas := [][3]float32{{1, 2, 3}, {-4, 0.5, 8}, {0, -1, 0}}
	baseIdx := []int16{2, 0, 1}
	baseVerts := [][3]float32{{100, 0, 0}, {0, 200, 0}, {0, 0, 300}}
	body := buildType2Body(4, deltas, baseIdx, scale)

	eq := func(t *testing.T, got, want [3]float32) {
		t.Helper()
		for k := 0; k < 3; k++ {
			if math.Abs(float64(got[k]-want[k])) > 1e-3 {
				t.Errorf("pos = %v, want %v", got, want)
				return
			}
		}
	}

	t.Run("base applied by index", func(t *testing.T) {
		cb, err := ParseCollBuffer(body, binary.LittleEndian, 2, baseVerts)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if len(cb.Positions) != 3 {
			t.Fatalf("positions = %d, want 3", len(cb.Positions))
		}
		for i := range deltas {
			b := baseVerts[baseIdx[i]]
			eq(t, cb.Positions[i], [3]float32{deltas[i][0] + b[0], deltas[i][1] + b[1], deltas[i][2] + b[2]})
		}
	})

	t.Run("nil base decodes at origin", func(t *testing.T) {
		cb, err := ParseCollBuffer(body, binary.LittleEndian, 2, nil)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		for i := range deltas {
			eq(t, cb.Positions[i], deltas[i])
		}
	})

	t.Run("out-of-range index falls back to zero", func(t *testing.T) {
		// Every baseIdx >= len(baseVerts): each must decode with a zero base.
		oob := buildType2Body(4, deltas, []int16{9, 9, 9}, scale)
		cb, err := ParseCollBuffer(oob, binary.LittleEndian, 2, baseVerts)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		for i := range deltas {
			eq(t, cb.Positions[i], deltas[i])
		}
	})
}

func TestParseCollBufferVersion0NoTypeField(t *testing.T) {
	// version < 2: no type/packing words; type defaults to 0, packing 0.
	buf := new(bytes.Buffer)
	w := func(v int32) { binary.Write(buf, binary.LittleEndian, v) }
	w(0) // a
	w(1) // numStrips
	w(0) // c
	// strip: 3 float verts
	w(3) // vertCount
	w(0) // groupId
	w(0) // beginArg
	for _, p := range [][3]float32{{1, 2, 3}, {4, 5, 6}, {7, 8, 9}} {
		binary.Write(buf, binary.LittleEndian, p[0])
		binary.Write(buf, binary.LittleEndian, p[1])
		binary.Write(buf, binary.LittleEndian, p[2])
	}
	cb, err := ParseCollBuffer(buf.Bytes(), binary.LittleEndian, 1, nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cb.Type != 0 || cb.Packing != 0 {
		t.Errorf("type/packing = %d/%d, want 0/0", cb.Type, cb.Packing)
	}
	if len(cb.Positions) != 3 || cb.Positions[2] != ([3]float32{7, 8, 9}) {
		t.Errorf("positions = %v", cb.Positions)
	}
}

// zone0107Path is the real beta-disc zone ESF used for on-disc validation. The
// test is skipped when it is not present so CI stays green without game assets.
const zone0107Path = "/Users/justinjanes/Development/elfconv/EQOABETADISC/DATA/ZONE0107.ESF"

// TestParseCollBufferRealZone0107 parses every 0x4200 CollBuffer in the real
// ZONE0107.ESF and asserts each stream is fully consumed, both type-1 and type-2
// encodings are exercised, and the merged world-space AABB is plausible terrain.
func TestParseCollBufferRealZone0107(t *testing.T) {
	data, err := os.ReadFile(zone0107Path)
	if err != nil {
		t.Skipf("real zone asset not available: %v", err)
	}

	r := io.ReadSeeker(bytes.NewReader(data))
	_, objects, _, order, err := ParseESF(r)
	if err != nil {
		t.Fatalf("ParseESF: %v", err)
	}

	var colls []*ESFObject
	var walk func(o *ESFObject)
	walk = func(o *ESFObject) {
		if uint16(o.Header.ObjectType) == 0x4200 {
			colls = append(colls, o)
		}
		for _, c := range o.Children {
			walk(c)
		}
	}
	for _, o := range objects {
		walk(o)
	}
	if len(colls) == 0 {
		t.Fatal("no CollBuffers found in ZONE0107.ESF")
	}

	typeSeen := map[int32]int{}
	totalVerts := 0
	totalTris := 0
	min := [3]float32{math.MaxFloat32, math.MaxFloat32, math.MaxFloat32}
	max := [3]float32{-math.MaxFloat32, -math.MaxFloat32, -math.MaxFloat32}

	for _, o := range colls {
		body, err := o.ReadBody(r)
		if err != nil {
			t.Fatalf("ReadBody: %v", err)
		}
		cb, err := ParseCollBuffer(body, order, int(o.Header.ObjectVersion), nil)
		if err != nil {
			t.Fatalf("ParseCollBuffer @0x%X: %v", o.Offset, err)
		}
		typeSeen[cb.Type]++
		totalVerts += len(cb.Positions)
		totalTris += len(cb.Triangles) / 3
		for _, p := range cb.Positions {
			for k := 0; k < 3; k++ {
				if p[k] < min[k] {
					min[k] = p[k]
				}
				if p[k] > max[k] {
					max[k] = p[k]
				}
			}
		}
	}

	t.Logf("CollBuffers=%d types=%v totalVerts=%d totalTris=%d", len(colls), typeSeen, totalVerts, totalTris)
	t.Logf("world AABB min=(%.1f,%.1f,%.1f) max=(%.1f,%.1f,%.1f)", min[0], min[1], min[2], max[0], max[1], max[2])

	if totalVerts < 1000 {
		t.Errorf("suspiciously few collision verts: %d", totalVerts)
	}
	if totalTris < 500 {
		t.Errorf("suspiciously few collision triangles: %d", totalTris)
	}
	// Both quantized encodings should appear in this zone.
	if typeSeen[1] == 0 || typeSeen[2] == 0 {
		t.Errorf("expected both type-1 and type-2 CollBuffers, got %v", typeSeen)
	}
	// Terrain should occupy a plausible horizontal extent (hundreds of world
	// units per axis) and stay within a sane vertical band.
	extX := max[0] - min[0]
	extZ := max[2] - min[2]
	if extX < 100 || extX > 5000 || extZ < 100 || extZ > 5000 {
		t.Errorf("implausible horizontal extent X=%.1f Z=%.1f", extX, extZ)
	}
	if max[1]-min[1] > 5000 {
		t.Errorf("implausible vertical extent Y=%.1f", max[1]-min[1])
	}
}
