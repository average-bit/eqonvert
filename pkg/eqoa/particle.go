package eqoa

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
)

// Particle / effect emitters (ESF object types 0xC000 ParticleDefinition and
// 0xC100 ParticleSprite). These carry no renderable mesh — the client spawns
// particles at runtime from these parameters — so eqonvert exports them as
// tagged emitter placeholders. The binary layout is recovered from
// ParseParticleSpriteObj__10VIESFParse and
// ParseParticleDefinition__10VIESFParseR9VIObjFileR20VIParticleDefinition.

// ParticleMotif is one emission layer inside a ParticleDefinition. The first
// motif is unnamed; additional motifs are prefixed with a 32-byte name.
type ParticleMotif struct {
	Name            string     `json:"name,omitempty"`
	Friction        float32    `json:"friction"`
	Birthrate       float32    `json:"birthrate"`
	BirthrateVar    float32    `json:"birthrate_var"`
	Lifespan        float32    `json:"lifespan"`
	LifespanVar     float32    `json:"lifespan_var"`
	Velocity        float32    `json:"velocity"`
	VelocityVar     float32    `json:"velocity_var"`
	StartSize       float32    `json:"start_size"`
	StartSizeVar    float32    `json:"start_size_var"`
	EndSize         float32    `json:"end_size"`
	EndSizeVar      float32    `json:"end_size_var"`
	InheritVelocity float32    `json:"inherit_velocity"`
	DeltaSpawn      float32    `json:"delta_spawn"`
	StartColorVar   [4]float32 `json:"start_color_var"`
	EndColorVar     [4]float32 `json:"end_color_var"`
	// Gradient is the 32-stop RGBA color ramp particles fade through over their
	// lifetime — what gives fire its orange→black and water its blue tint.
	Gradient       [32][4]float32 `json:"gradient"`
	GradientRepeat float32        `json:"gradient_repeat"`
	InnerOffset    [3]float32     `json:"inner_offset"`
	InnerHprVar    [3]float32     `json:"inner_hpr_var"`
	OuterOffset    [3]float32     `json:"outer_offset"`
	OuterHprVar    [3]float32     `json:"outer_hpr_var"`
	NozzleAxis     [3]float32     `json:"nozzle_axis"`
	NozzleHprVar   [3]float32     `json:"nozzle_hpr_var"`
	GravityOn      int32          `json:"gravity_on"`
}

// ParticleDefinition is the full emitter parameter set (ESF 0xC000).
type ParticleDefinition struct {
	TextureDictID uint32          `json:"texture_dict_id"`
	BlendMode     int32           `json:"blend_mode"`
	ZWrite        bool            `json:"z_write"`
	ZTest         bool            `json:"z_test"`
	TextureConfig int32           `json:"texture_config"`
	Motifs        []ParticleMotif `json:"motifs"`
	// Texture is the particle sprite image (the 0xC000's 0x1000 Surface child),
	// decoded but not JSON-serialised — the caller embeds it and references it by
	// glTF texture index.
	Texture *Surface `json:"-"`
}

// ParticleSprite is a placed emitter (ESF 0xC100). DefRef references a shared
// ParticleDefinition by dictID; Def is the inline definition when present.
type ParticleSprite struct {
	DefRef uint32              `json:"def_ref,omitempty"`
	Flag   int32               `json:"flag,omitempty"`
	Def    *ParticleDefinition `json:"definition,omitempty"`
}

// ParseParticleSprite decodes a 0xC100 ParticleSprite object. Its children are a
// 0xC101 header (the definition dictID) and a 0xC000 ParticleDefinition. The
// definition in turn holds: 0xC010 (texture dictID), a 0x1000 Surface (the
// particle texture), and 0xC020 — the flat parameter block that
// ParseParticleDefinition decodes.
func ParseParticleSprite(r io.ReadSeeker, obj *ESFObject, order binary.ByteOrder) (*ParticleSprite, error) {
	ps := &ParticleSprite{}
	for _, c := range obj.Children {
		switch uint16(c.Header.ObjectType) {
		case 0xC101: // ParticleSpriteHeader — first word is the definition dictID.
			hb, err := c.ReadBody(r)
			if err != nil {
				return nil, err
			}
			if len(hb) >= 4 {
				ps.DefRef = order.Uint32(hb[0:4])
			}
		case 0xC000: // ParticleDefinition container.
			var def *ParticleDefinition
			var tex *Surface
			for _, gc := range c.Children {
				switch uint16(gc.Header.ObjectType) {
				case 0xC020: // flat parameter block
					pb, err := gc.ReadBody(r)
					if err != nil {
						return nil, err
					}
					def, err = ParseParticleDefinition(pb, int(gc.Header.ObjectVersion), order)
					if err != nil {
						return nil, err
					}
				case 0x1000: // the particle sprite texture — best-effort (skip on error)
					if sb, err := gc.ReadBody(r); err == nil {
						if s, err := ParseSurface(sb, order); err == nil {
							tex = s
						}
					}
				}
			}
			if def != nil {
				def.Texture = tex
				ps.Def = def
			}
		}
	}
	return ps, nil
}

// ParseParticleDefinition decodes a 0xC000 ParticleDefinition body. version is
// the object header's ObjectVersion (v1+ stores a per-motif gravity flag).
func ParseParticleDefinition(body []byte, version int, order binary.ByteOrder) (*ParticleDefinition, error) {
	p := 0
	need := func(n int) bool { return p+n <= len(body) }
	ru32 := func() uint32 { v := order.Uint32(body[p:]); p += 4; return v }
	ri32 := func() int32 { return int32(ru32()) }
	rf32 := func() float32 { return math.Float32frombits(ru32()) }
	rcolor := func() [4]float32 { return [4]float32{rf32(), rf32(), rf32(), rf32()} }
	rvec3 := func() [3]float32 { return [3]float32{rf32(), rf32(), rf32()} }

	if !need(6 * 4) {
		return nil, fmt.Errorf("ParticleDefinition: body too short (%d bytes)", len(body))
	}
	def := &ParticleDefinition{}
	def.TextureDictID = ru32()
	def.BlendMode = ri32()
	def.ZWrite = ri32() != 0
	def.ZTest = ri32() != 0
	def.TextureConfig = ri32()
	motifCount := ri32() + 1 // stored value is count-1
	if motifCount < 1 || motifCount > 4096 {
		return nil, fmt.Errorf("ParticleDefinition: implausible motif count %d", motifCount)
	}

	for i := int32(0); i < motifCount; i++ {
		var m ParticleMotif
		if i != 0 {
			if !need(0x20) {
				return nil, fmt.Errorf("ParticleDefinition: truncated motif %d name", i)
			}
			m.Name = cstr(body[p : p+0x20])
			p += 0x20
		}
		// 13 scalar attributes.
		if !need(13 * 4) {
			return nil, fmt.Errorf("ParticleDefinition: truncated motif %d scalars", i)
		}
		m.Friction = rf32()
		m.Birthrate = rf32()
		m.BirthrateVar = rf32()
		m.Lifespan = rf32()
		m.LifespanVar = rf32()
		m.Velocity = rf32()
		m.VelocityVar = rf32()
		m.StartSize = rf32()
		m.StartSizeVar = rf32()
		m.EndSize = rf32()
		m.EndSizeVar = rf32()
		m.InheritVelocity = rf32()
		m.DeltaSpawn = rf32()
		// start/end color var (RGBA) then 32-stop gradient.
		if !need((2 + 32) * 16) {
			return nil, fmt.Errorf("ParticleDefinition: truncated motif %d colors", i)
		}
		m.StartColorVar = rcolor()
		m.EndColorVar = rcolor()
		for g := 0; g < 32; g++ {
			m.Gradient[g] = rcolor()
		}
		if !need(4 + 6*12) {
			return nil, fmt.Errorf("ParticleDefinition: truncated motif %d vectors", i)
		}
		m.GradientRepeat = rf32()
		m.InnerOffset = rvec3()
		m.InnerHprVar = rvec3()
		m.OuterOffset = rvec3()
		m.OuterHprVar = rvec3()
		m.NozzleAxis = rvec3()
		m.NozzleHprVar = rvec3()
		if version >= 1 {
			if !need(4) {
				return nil, fmt.Errorf("ParticleDefinition: truncated motif %d gravity", i)
			}
			m.GravityOn = ri32()
		}
		def.Motifs = append(def.Motifs, m)
	}
	return def, nil
}

// cstr trims a fixed-size C string at its first NUL.
func cstr(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}
