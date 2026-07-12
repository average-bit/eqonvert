package eqoa

import (
	"bytes"
	"io"
)

// SurfaceRegistry is a cross-file lookup for Surface objects by DictID.
//
// ARENA zone objects have materials whose texture DictIDs point to surfaces
// defined in separate ESF files (e.g. TUNARIA.ESF, KARANA.ESF). A two-pass
// ISO or directory conversion populates the registry in pass 1 so that pass 2
// can embed those textures into the GLBs that reference them.
type SurfaceRegistry struct {
	surfaces map[uint32]*Surface
}

func NewSurfaceRegistry() *SurfaceRegistry {
	return &SurfaceRegistry{surfaces: make(map[uint32]*Surface)}
}

// Register records a surface by DictID, FIRST-write-wins — matching the engine,
// whose global surface map inserts a key only if absent (Ghidra: VISurface
// parser FUN_0040c040 skips define when the DictID is already present, via the
// keyed map on the client object). Last-write-wins would pick a different
// surface than the game for any DictID that legitimately collides across files.
func (r *SurfaceRegistry) Register(s *Surface) {
	if _, exists := r.surfaces[s.DictID]; !exists {
		r.surfaces[s.DictID] = s
	}
}

func (r *SurfaceRegistry) Get(dictID uint32) (*Surface, bool) {
	s, ok := r.surfaces[dictID]
	return s, ok
}

func (r *SurfaceRegistry) Len() int { return len(r.surfaces) }

// PopulateFromESFData decompresses (if CESF), parses the ESF, and registers
// every Surface found inside any MatPalObj (0x1110) → surface array (0x1001).
func (r *SurfaceRegistry) PopulateFromESFData(data []byte) error {
	var esfReader io.ReadSeeker

	if len(data) >= 4 && string(data[:4]) == MagicCESF {
		dr, _, err := DecompressCSF(bytes.NewReader(data))
		if err != nil {
			return err
		}
		all, err := io.ReadAll(dr)
		if err != nil {
			return err
		}
		esfReader = bytes.NewReader(all)
	} else {
		esfReader = bytes.NewReader(data)
	}

	_, objects, _, order, err := ParseESF(esfReader)
	if err != nil {
		return err
	}

	var walk func(obj *ESFObject)
	walk = func(obj *ESFObject) {
		// Register surfaces from any 0x1001 (SurfaceArray) found anywhere in the
		// tree — covers both the MatPalette pattern (0x1110 → 0x1001) and the
		// zone-level pattern (0x3100 → 0x1001 directly).
		if uint16(obj.Header.ObjectType) == 0x1001 {
			for _, sObj := range obj.Children {
				body, err := sObj.ReadBody(esfReader)
				if err != nil {
					continue
				}
				s, err := ParseSurface(body, order)
				if err == nil {
					r.Register(s)
				}
			}
			return // don't recurse into surface children
		}
		for _, child := range obj.Children {
			walk(child)
		}
	}
	for _, obj := range objects {
		walk(obj)
	}
	return nil
}
