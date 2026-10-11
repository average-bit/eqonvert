package gltf

import (
	"strings"
	"testing"

	"github.com/average-bit/eqonvert/pkg/eqoa"
)

func TestSurfaceExtras(t *testing.T) {
	// pick one real id from each curated set
	var waterID, lavaID, magicID, iceID uint32
	for id, kind := range animatedSurfaceKind {
		switch kind {
		case "water":
			waterID = id
		case "lava":
			lavaID = id
		case "magic":
			magicID = id
		}
	}
	for id := range iceSurfaceDictIDs {
		iceID = id
		break
	}

	cases := []struct {
		name   string
		mat    *eqoa.Material
		want   []string // substrings that must appear
		absent []string // substrings that must NOT appear
		nilEx  bool
	}{
		{
			name:  "plain static surface -> no extras",
			mat:   &eqoa.Material{Layers: []eqoa.MaterialLayer{{TexID: 0x1234}}},
			nilEx: true,
		},
		{
			name: "scrolling surface -> animated + scroll",
			mat:  &eqoa.Material{Layers: []eqoa.MaterialLayer{{TexID: 0x1234, Scroll: [2]float32{0.2, 0}}}},
			want: []string{`"eqoaAnimated":true`, `"eqoaScroll"`},
		},
		{
			name:   "scrolling water surface -> eqoaWater + animated, not ice",
			mat:    &eqoa.Material{Layers: []eqoa.MaterialLayer{{TexID: waterID, Scroll: [2]float32{0.1, 0}}}},
			want:   []string{`"eqoaWater":true`, `"eqoaAnimated":true`},
			absent: []string{`"eqoaIce"`},
		},
		{
			name:   "water DictID WITHOUT scroll -> NO role tag (static reuse)",
			mat:    &eqoa.Material{Layers: []eqoa.MaterialLayer{{TexID: waterID}}},
			nilEx:  true,
		},
		{
			name:   "scrolling lava surface -> eqoaLava, not water",
			mat:    &eqoa.Material{Layers: []eqoa.MaterialLayer{{TexID: lavaID, Scroll: [2]float32{0, 0.1}}}},
			want:   []string{`"eqoaLava":true`},
			absent: []string{`"eqoaWater"`},
		},
		{
			name:   "scrolling magic surface -> eqoaMagic, not water",
			mat:    &eqoa.Material{Layers: []eqoa.MaterialLayer{{TexID: magicID, Scroll: [2]float32{0.05, 0}}}},
			want:   []string{`"eqoaMagic":true`},
			absent: []string{`"eqoaWater"`},
		},
		{
			name:   "known ice surface -> eqoaIce, not water",
			mat:    &eqoa.Material{Layers: []eqoa.MaterialLayer{{TexID: iceID}}},
			want:   []string{`"eqoaIce":true`},
			absent: []string{`"eqoaWater"`},
		},
		{
			name: "scrolling water -> both animated and water",
			mat:  &eqoa.Material{Layers: []eqoa.MaterialLayer{{TexID: waterID, Scroll: [2]float32{0, 0.5}}}},
			want: []string{`"eqoaAnimated":true`, `"eqoaWater":true`},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ex := surfaceExtras(tc.mat)
			if tc.nilEx {
				if ex != nil {
					t.Fatalf("expected nil extras, got %s", ex)
				}
				return
			}
			if ex == nil {
				t.Fatal("expected extras, got nil")
			}
			s := string(ex)
			for _, w := range tc.want {
				if !strings.Contains(s, w) {
					t.Errorf("extras %s missing %q", s, w)
				}
			}
			for _, a := range tc.absent {
				if strings.Contains(s, a) {
					t.Errorf("extras %s should not contain %q", s, a)
				}
			}
		})
	}
}
