package cmd

import "testing"

// TestSkipSubSprite verifies that a sub-sprite mesh (0x2310/0x2320) under a
// CSprite or HSprite is skipped only when that parent's GLB was written, so a
// failed or empty parent never loses the geometry.
func TestSkipSubSprite(t *testing.T) {
	drawn := &spriteOwner{id: 0x1234, drawn: true}
	failed := &spriteOwner{id: 0x1234}
	cases := []struct {
		name    string
		objType uint16
		owner   *spriteOwner
		want    bool
	}{
		{"0x2320 under drawn parent", 0x2320, drawn, true},
		{"0x2310 under drawn parent", 0x2310, drawn, true},
		{"0x2320 under failed parent", 0x2320, failed, false},
		{"0x2320 at top level", 0x2320, nil, false},
		{"0x2700 under drawn parent", 0x2700, drawn, false},
		{"0x2000 under drawn parent", 0x2000, drawn, false},
	}
	for _, c := range cases {
		if got := skipSubSprite(c.objType, c.owner); got != c.want {
			t.Errorf("%s: skipSubSprite = %v, want %v", c.name, got, c.want)
		}
	}
}
