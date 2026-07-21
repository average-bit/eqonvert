package eqoa

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
)

// buildParticleBody encodes a 0xC020 parameter block (version 1, with gravity).
func buildParticleBody(motifNames []string) []byte {
	buf := new(bytes.Buffer)
	wi := func(v int32) { binary.Write(buf, binary.LittleEndian, v) }
	wu := func(v uint32) { binary.Write(buf, binary.LittleEndian, v) }
	wf := func(v float32) { binary.Write(buf, binary.LittleEndian, v) }
	wc := func(c [4]float32) { wf(c[0]); wf(c[1]); wf(c[2]); wf(c[3]) }

	wu(0xDEADBEEF) // textureDictID
	wi(2)          // blendMode
	wi(1)          // zWrite
	wi(0)          // zTest
	wi(7)          // textureConfig
	wi(int32(len(motifNames)) - 1)

	for i, name := range motifNames {
		if i != 0 {
			nm := make([]byte, 0x20)
			copy(nm, name)
			buf.Write(nm)
		}
		// 13 scalars: friction..deltaSpawn — use the index so we can assert.
		for s := 0; s < 13; s++ {
			wf(float32(i*100 + s))
		}
		wc([4]float32{0.1, 0.2, 0.3, 0.4}) // startColorVar
		wc([4]float32{0.5, 0.6, 0.7, 0.8}) // endColorVar
		for g := 0; g < 32; g++ {
			wc([4]float32{float32(g) / 32, 0, 0, 1})
		}
		wf(1.5) // gradientRepeat
		for v := 0; v < 6; v++ {
			wf(float32(v)); wf(0); wf(0) // 6 vec3
		}
		wi(int32(1)) // gravityOn (version 1)
	}
	return buf.Bytes()
}

func TestParseParticleDefinition(t *testing.T) {
	body := buildParticleBody([]string{"", "flame"})
	def, err := ParseParticleDefinition(body, 1, binary.LittleEndian)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if def.TextureDictID != 0xDEADBEEF {
		t.Errorf("texture = 0x%X, want 0xDEADBEEF", def.TextureDictID)
	}
	if def.BlendMode != 2 || !def.ZWrite || def.ZTest || def.TextureConfig != 7 {
		t.Errorf("header = blend %d zw %v zt %v cfg %d", def.BlendMode, def.ZWrite, def.ZTest, def.TextureConfig)
	}
	if len(def.Motifs) != 2 {
		t.Fatalf("motifs = %d, want 2", len(def.Motifs))
	}
	if def.Motifs[0].Name != "" || def.Motifs[1].Name != "flame" {
		t.Errorf("names = %q,%q", def.Motifs[0].Name, def.Motifs[1].Name)
	}
	// Motif 0 friction=0, birthrate=1 (i*100+s with i=0); motif 1 friction=100.
	if def.Motifs[0].Friction != 0 || def.Motifs[0].Birthrate != 1 {
		t.Errorf("motif0 friction=%v birthrate=%v", def.Motifs[0].Friction, def.Motifs[0].Birthrate)
	}
	if def.Motifs[1].Friction != 100 {
		t.Errorf("motif1 friction=%v, want 100", def.Motifs[1].Friction)
	}
	if def.Motifs[0].GravityOn != 1 {
		t.Errorf("gravity = %d, want 1", def.Motifs[0].GravityOn)
	}
	if def.Motifs[0].Gradient[16][0] != 16.0/32 {
		t.Errorf("gradient[16].r = %v, want %v", def.Motifs[0].Gradient[16][0], 16.0/32)
	}
	if def.Motifs[0].NozzleAxis != [3]float32{4, 0, 0} {
		t.Errorf("nozzleAxis = %v, want {4 0 0}", def.Motifs[0].NozzleAxis)
	}
}

func TestParseParticleDefinitionVersion0NoGravity(t *testing.T) {
	// version 0 omits the trailing gravity word; a single-motif body must decode
	// with exactly the pre-gravity byte count.
	full := buildParticleBody([]string{""})
	body := full[:len(full)-4] // drop the gravity int32
	def, err := ParseParticleDefinition(body, 0, binary.LittleEndian)
	if err != nil {
		t.Fatalf("parse v0: %v", err)
	}
	if len(def.Motifs) != 1 || def.Motifs[0].GravityOn != 0 {
		t.Errorf("v0 motif gravity = %d, want 0 (unset)", def.Motifs[0].GravityOn)
	}
}

func TestParseParticleDefinitionRejectsGarbage(t *testing.T) {
	// A short/garbage body must error, not panic or return a huge motif count.
	if _, err := ParseParticleDefinition([]byte{1, 2, 3}, 1, binary.LittleEndian); err == nil {
		t.Error("expected error on short body")
	}
	bad := make([]byte, 24)
	binary.LittleEndian.PutUint32(bad[20:], math.MaxUint32-1) // motifCount-1 huge
	if _, err := ParseParticleDefinition(bad, 1, binary.LittleEndian); err == nil {
		t.Error("expected error on implausible motif count")
	}
}
