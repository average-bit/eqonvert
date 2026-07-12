package eqoa

import (
	"math"
)

type Mat4 [16]float32

// QuatNormalize returns the unit quaternion (XYZW); degenerate input yields identity.
func QuatNormalize(q [4]float32) [4]float32 {
	lenSq := q[0]*q[0] + q[1]*q[1] + q[2]*q[2] + q[3]*q[3]
	if lenSq < 1e-12 {
		return [4]float32{0, 0, 0, 1}
	}
	inv := float32(1.0 / math.Sqrt(float64(lenSq)))
	return [4]float32{q[0] * inv, q[1] * inv, q[2] * inv, q[3] * inv}
}

// QuatConjugate returns the conjugate (= inverse for unit quaternions), XYZW.
func QuatConjugate(q [4]float32) [4]float32 {
	return [4]float32{-q[0], -q[1], -q[2], q[3]}
}

// QuatMul returns the Hamilton product a⊗b (XYZW layout), satisfying
// R(QuatMul(a,b)) == R(a)·R(b) for the rotation-matrix convention used in
// FromRotationTranslationScale.
func QuatMul(a, b [4]float32) [4]float32 {
	ax, ay, az, aw := a[0], a[1], a[2], a[3]
	bx, by, bz, bw := b[0], b[1], b[2], b[3]
	return [4]float32{
		aw*bx + ax*bw + ay*bz - az*by,
		aw*by - ax*bz + ay*bw + az*bx,
		aw*bz + ax*by - ay*bx + az*bw,
		aw*bw - ax*bx - ay*by - az*bz,
	}
}

// QuatRotateVec rotates vector v by unit quaternion q (XYZW).
func QuatRotateVec(q [4]float32, v [3]float32) [3]float32 {
	x, y, z, w := q[0], q[1], q[2], q[3]
	// t = 2 * cross(q.xyz, v)
	tx := 2 * (y*v[2] - z*v[1])
	ty := 2 * (z*v[0] - x*v[2])
	tz := 2 * (x*v[1] - y*v[0])
	// v' = v + w*t + cross(q.xyz, t)
	return [3]float32{
		v[0] + w*tx + y*tz - z*ty,
		v[1] + w*ty + z*tx - x*tz,
		v[2] + w*tz + x*ty - y*tx,
	}
}

func Identity() Mat4 {
	return Mat4{
		1, 0, 0, 0,
		0, 1, 0, 0,
		0, 0, 1, 0,
		0, 0, 0, 1,
	}
}

func FromRotationTranslationScale(q [4]float32, t [3]float32, s [3]float32) Mat4 {
	m := Identity()

	x, y, z, w := q[0], q[1], q[2], q[3]
	x2 := x + x
	y2 := y + y
	z2 := z + z
	xx := x * x2
	xy := x * y2
	xz := x * z2
	yy := y * y2
	yz := y * z2
	zz := z * z2
	wx := w * x2
	wy := w * y2
	wz := w * z2

	m[0] = (1 - (yy + zz)) * s[0]
	m[1] = (xy + wz) * s[0]
	m[2] = (xz - wy) * s[0]

	m[4] = (xy - wz) * s[1]
	m[5] = (1 - (xx + zz)) * s[1]
	m[6] = (yz + wx) * s[1]

	m[8] = (xz + wy) * s[2]
	m[9] = (yz - wx) * s[2]
	m[10] = (1 - (xx + yy)) * s[2]

	m[12] = t[0]
	m[13] = t[1]
	m[14] = t[2]

	return m
}

func (a Mat4) Multiply(b Mat4) Mat4 {
	var out Mat4
	for i := 0; i < 4; i++ { // col
		for j := 0; j < 4; j++ { // row
			out[i*4+j] = a[0*4+j]*b[i*4+0] +
				a[1*4+j]*b[i*4+1] +
				a[2*4+j]*b[i*4+2] +
				a[3*4+j]*b[i*4+3]
		}
	}
	return out
}

func (m Mat4) Inverse() Mat4 {
	// Specialized inverse for TRS matrices (affine)
	// For general matrices we'd need a full 4x4 inverse
	// But EQOA joints are usually just TRS.

	// Translation part
	t := [3]float32{m[12], m[13], m[14]}

	// Rotation/Scale part (upper 3x3)
	rs := [9]float32{
		m[0], m[1], m[2],
		m[4], m[5], m[6],
		m[8], m[9], m[10],
	}

	det := rs[0]*(rs[4]*rs[8]-rs[5]*rs[7]) -
		rs[1]*(rs[3]*rs[8]-rs[5]*rs[6]) +
		rs[2]*(rs[3]*rs[7]-rs[4]*rs[6])

	if math.Abs(float64(det)) < 1e-6 {
		return Identity()
	}

	invDet := 1.0 / det

	invRS := [9]float32{
		(rs[4]*rs[8] - rs[5]*rs[7]) * invDet,
		-(rs[1]*rs[8] - rs[2]*rs[7]) * invDet,
		(rs[1]*rs[5] - rs[2]*rs[4]) * invDet,
		-(rs[3]*rs[8] - rs[5]*rs[6]) * invDet,
		(rs[0]*rs[8] - rs[2]*rs[6]) * invDet,
		-(rs[0]*rs[5] - rs[2]*rs[3]) * invDet,
		(rs[3]*rs[7] - rs[4]*rs[6]) * invDet,
		-(rs[0]*rs[7] - rs[1]*rs[6]) * invDet,
		(rs[0]*rs[4] - rs[1]*rs[3]) * invDet,
	}

	out := Identity()
	out[0], out[1], out[2] = invRS[0], invRS[1], invRS[2]
	out[4], out[5], out[6] = invRS[3], invRS[4], invRS[5]
	out[8], out[9], out[10] = invRS[6], invRS[7], invRS[8]

	out[12] = -(t[0]*out[0] + t[1]*out[4] + t[2]*out[8])
	out[13] = -(t[0]*out[1] + t[1]*out[5] + t[2]*out[9])
	out[14] = -(t[0]*out[2] + t[1]*out[6] + t[2]*out[10])

	return out
}
