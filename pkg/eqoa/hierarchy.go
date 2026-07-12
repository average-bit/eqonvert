package eqoa

import (
	"encoding/binary"
	"fmt"
	"io"
)

// HSpriteHierarchy represents a skeletal structure (object type 0x2400).
// Confirmed binary layout by Ghidra decompile of FUN_0040d168 / FUN_0041ae00:
//
//	If ObjectVersion != 0: int32 headerFloatCount, then headerFloatCount float32s (LOD distances)
//	int32 jointCount
//	Per joint (36 bytes, +4 if ObjectVersion != 0):
//	  [0:4]   int32      ParentIndex  (-1 = root)
//	  [4:20]  [4]float32 Rotation     (quaternion XYZW — WORLD/model space, see below)
//	  [20:24] float32    Scale
//	  [24:36] [3]float32 Position     (WORLD/model space)
//	  [36:40] int32      Flags        (only present when ObjectVersion != 0; LOD level per joint)
//	  [40:40+4N] float32[N] LODWeights (N = headerFloatCount; only present when ObjectVersion >= 2;
//	                         one per-LOD blend weight per joint, matching the N LOD distance tiers
//	                         in HeaderFloats — total per-joint size is 40 + 4N bytes for ver>=2)
//
// Transform semantics — the stored TRS is the joint's bind pose in MODEL (world)
// space, NOT parent-relative.  Confirmed by Ghidra decompile of FUN_0041ae00:
// for non-root joints the engine combines the joint's raw matrix with the
// parent's raw matrix (FUN_003d1e38) and DECOMPOSES the result into the
// joint's default LOCAL TRS (FUN_003d4068 → jointState+0xd0) — i.e. it derives
// local = world relative to parentWorld at load time.  Root joints store their
// raw TRS as the default local directly.
// Empirically verified on CHAR_0x8C9B4B39 (mesh 1.78 units tall): head joint
// raw Y=1.825, T-pose arm chain raw Y≈1.44 horizontal, leg chain 1.02→0.58→
// 0.12→0.0 — all model-space positions matching the mesh.
// Animation keyframes (0x2600) hold LOCAL TRS that replaces the default
// (FUN_0041dd98); e.g. spine channel position (0,0.080,0.010) == world(J1) −
// world(J0) exactly.
type HSpriteHierarchy struct {
	HeaderFloats []float32
	Joints       []Joint
}

// Joint represents a single bone in the hierarchy.
type Joint struct {
	ParentIndex int32
	Rotation    [4]float32 // quaternion XYZW
	Scale       float32
	Position    [3]float32
	Flags       int32 // only populated when ObjectVersion != 0
}

// ParseHSpriteHierarchy reads a hierarchy body.
// version is the ObjectVersion field from the containing 0x2400 object header.
func ParseHSpriteHierarchy(r io.Reader, order binary.ByteOrder, version int16) (*HSpriteHierarchy, error) {
	h := &HSpriteHierarchy{}

	if version != 0 {
		var floatCount int32
		if err := binary.Read(r, order, &floatCount); err != nil {
			return nil, err
		}
		h.HeaderFloats = make([]float32, floatCount)
		for i := range h.HeaderFloats {
			if err := binary.Read(r, order, &h.HeaderFloats[i]); err != nil {
				return nil, err
			}
		}
	}

	var jointCount int32
	if err := binary.Read(r, order, &jointCount); err != nil {
		return nil, err
	}

	h.Joints = make([]Joint, jointCount)
	for i := range h.Joints {
		j := &h.Joints[i]
		if err := binary.Read(r, order, &j.ParentIndex); err != nil {
			return nil, err
		}
		if j.ParentIndex < -1 || j.ParentIndex >= int32(len(h.Joints)) {
			return nil, fmt.Errorf("joint %d: ParentIndex %d out of range (jointCount=%d) — likely version mismatch", i, j.ParentIndex, len(h.Joints))
		}
		for k := range j.Rotation {
			if err := binary.Read(r, order, &j.Rotation[k]); err != nil {
				return nil, err
			}
		}
		if err := binary.Read(r, order, &j.Scale); err != nil {
			return nil, err
		}
		for k := range j.Position {
			if err := binary.Read(r, order, &j.Position[k]); err != nil {
				return nil, err
			}
		}
		if version != 0 {
			if err := binary.Read(r, order, &j.Flags); err != nil {
				return nil, err
			}
			// Version 2+: each joint carries headerFloatCount per-joint LOD blend
			// weights (one float per LOD distance tier stored in HeaderFloats).
			// These mirror the LOD distance thresholds in HeaderFloats and are not
			// needed for skeletal animation — read and discard to stay aligned.
			if version >= 2 {
				for range h.HeaderFloats {
					var lodWeight float32
					if err := binary.Read(r, order, &lodWeight); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	return h, nil
}

// ComputeGlobalTransforms returns one world-space Mat4 per joint.  The stored
// joint TRS is already in model/world space (see type comment), so this is a
// direct conversion with no parent-chain accumulation.
func (h *HSpriteHierarchy) ComputeGlobalTransforms() []Mat4 {
	globals := make([]Mat4, len(h.Joints))
	for i, j := range h.Joints {
		globals[i] = FromRotationTranslationScale(j.Rotation, j.Position, [3]float32{j.Scale, j.Scale, j.Scale})
	}
	return globals
}

// LocalTRS returns joint i's bind transform relative to its parent, derived
// from the world-space stored values — the same world→local conversion the
// engine performs at load (FUN_0041ae00).  Root joints return their stored
// TRS unchanged.
func (h *HSpriteHierarchy) LocalTRS(i int) (rot [4]float32, pos [3]float32, scale float32) {
	j := h.Joints[i]
	if j.ParentIndex < 0 {
		return QuatNormalize(j.Rotation), j.Position, j.Scale
	}
	p := h.Joints[j.ParentIndex]
	pq := QuatNormalize(p.Rotation)
	pqInv := QuatConjugate(pq)

	rot = QuatMul(pqInv, QuatNormalize(j.Rotation))

	d := [3]float32{
		j.Position[0] - p.Position[0],
		j.Position[1] - p.Position[1],
		j.Position[2] - p.Position[2],
	}
	pos = QuatRotateVec(pqInv, d)
	scale = j.Scale
	if p.Scale != 0 {
		pos[0] /= p.Scale
		pos[1] /= p.Scale
		pos[2] /= p.Scale
		scale = j.Scale / p.Scale
	}
	return rot, pos, scale
}
