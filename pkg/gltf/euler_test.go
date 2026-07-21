package gltf

import (
	"math"
	"testing"
)

// apply rotates a vector by the row-major 3×3 matrix m.
func apply(m [9]float32, v [3]float32) [3]float32 {
	return [3]float32{
		m[0]*v[0] + m[1]*v[1] + m[2]*v[2],
		m[3]*v[0] + m[4]*v[1] + m[5]*v[2],
		m[6]*v[0] + m[7]*v[1] + m[8]*v[2],
	}
}

func vecEq(t *testing.T, got, want [3]float32, tol float64) {
	t.Helper()
	for k := 0; k < 3; k++ {
		if math.Abs(float64(got[k]-want[k])) > tol {
			t.Errorf("got %v, want %v", got, want)
			return
		}
	}
}

// TestEulerRotMatrixIdentity: zero angles must produce the identity rotation, so
// props with no heading are placed byte-for-byte where their vertices already sit.
func TestEulerRotMatrixIdentity(t *testing.T) {
	m := EulerRotMatrix([3]float32{0, 0, 0})
	want := [9]float32{1, 0, 0, 0, 1, 0, 0, 0, 1}
	for i := range want {
		if math.Abs(float64(m[i]-want[i])) > 1e-6 {
			t.Fatalf("identity rot = %v", m)
		}
	}
}

// TestEulerRotMatrixYaw90: a 90° yaw about the vertical (Height/Y) axis must map
// East(+X) to North(+Z) and leave Height(+Y) fixed — the common actor case.
func TestEulerRotMatrixYaw90(t *testing.T) {
	m := EulerRotMatrix([3]float32{float32(math.Pi / 2), 0, 0})
	vecEq(t, apply(m, [3]float32{1, 0, 0}), [3]float32{0, 0, 1}, 1e-5) // East -> North
	vecEq(t, apply(m, [3]float32{0, 1, 0}), [3]float32{0, 1, 0}, 1e-5) // Height fixed
}

// applyQuat rotates v by quaternion q=[x,y,z,w].
func applyQuat(q [4]float32, v [3]float32) [3]float32 {
	x, y, z, w := q[0], q[1], q[2], q[3]
	// t = 2 * cross(q.xyz, v)
	tx := 2 * (y*v[2] - z*v[1])
	ty := 2 * (z*v[0] - x*v[2])
	tz := 2 * (x*v[1] - y*v[0])
	// v + w*t + cross(q.xyz, t)
	return [3]float32{
		v[0] + w*tx + (y*tz - z*ty),
		v[1] + w*ty + (z*tx - x*tz),
		v[2] + w*tz + (x*ty - y*tx),
	}
}

// TestMat3ToQuatMatchesMatrix: the quaternion from mat3ToQuat must rotate vectors
// identically to the source EulerRotMatrix, across yaw/pitch/roll including near
// 180° (where a naive trace branch degenerates) — this is what lets the animated
// sprite root use decomposition-free TRS instead of a warp-prone matrix.
func TestMat3ToQuatMatchesMatrix(t *testing.T) {
	rots := [][3]float32{
		{0, 0, 0}, {float32(math.Pi), 0, 0}, {0, float32(math.Pi), 0}, {0, 0, float32(math.Pi)},
		{2.9, 0.1, -0.2}, {0.3, -0.7, 1.1}, {-2.5, 1.4, 2.7},
	}
	probes := [][3]float32{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}, {1.3, -2.1, 0.7}}
	for _, rot := range rots {
		m := EulerRotMatrix(rot)
		q := [4]float32{}
		copy(q[:], mat3ToQuat(m))
		for _, v := range probes {
			mv := apply(m, v)
			qv := applyQuat(q, v)
			for k := 0; k < 3; k++ {
				if math.Abs(float64(mv[k]-qv[k])) > 1e-4 {
					t.Errorf("rot %v vec %v: matrix→%v quat→%v differ", rot, v, mv, qv)
				}
			}
		}
	}
}

// TestEulerRotMatrixOrthonormal: the matrix must be a pure rotation (orthonormal
// rows, unit length) for arbitrary angles, so collision transforms preserve shape
// (no shear/scale leaking in).
func TestEulerRotMatrixOrthonormal(t *testing.T) {
	for _, rot := range [][3]float32{
		{0.3, -0.7, 1.1},
		{2.0, 0.4, -1.3},
		{-1.1, 1.5, 0.2},
	} {
		m := EulerRotMatrix(rot)
		rows := [3][3]float32{
			{m[0], m[1], m[2]},
			{m[3], m[4], m[5]},
			{m[6], m[7], m[8]},
		}
		dot := func(a, b [3]float32) float64 {
			return float64(a[0]*b[0] + a[1]*b[1] + a[2]*b[2])
		}
		for i := 0; i < 3; i++ {
			if math.Abs(dot(rows[i], rows[i])-1) > 1e-5 {
				t.Errorf("rot %v: row %d not unit length", rot, i)
			}
			for j := i + 1; j < 3; j++ {
				if math.Abs(dot(rows[i], rows[j])) > 1e-5 {
					t.Errorf("rot %v: rows %d,%d not orthogonal", rot, i, j)
				}
			}
		}
	}
}
