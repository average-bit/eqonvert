package eqoa

import (
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
)

// EQOA bitmap fonts (object 0x7000) — format confirmed by Ghidra decompile
// of the parser FUN_00411288 and validated by exact-length parses of both
// beta fonts (ARIAL: 256 glyphs, ARIALUI: 7,573-glyph Shift-JIS font):
//
//	0x7010 FontHeader: u32 dictID, u32 numGlyphs, u32 height, u32 mode
//	                   (+ u32 extra when ObjectVersion != 0)
//	0x7020 widths:     numGlyphs × u8 advance widths
//	0x7030 glyph data: u32 paletteCount, paletteCount × RGBA bytes,
//	                   then per glyph:
//	                     u16 charCode   (ASCII or JIS — see SjisToJis in VIFont)
//	                     u32 rowBytes
//	                     height × rowBytes indexed pixels
//
// mode selects the row pixel format (five converters in the engine);
// mode 1 = one palette index per byte (rowBytes == width), mode 0 = packed
// 4-bit indices.  Pixels are indices into the RGBA palette.
type Font struct {
	DictID    uint32
	NumGlyphs int
	Height    int
	Mode      int
	Palette   []color.NRGBA
	Glyphs    []FontGlyph
}

type FontGlyph struct {
	Code  uint16
	Width int
	// Pixels is Width×Height NRGBA, already palette-resolved.
	Pixels *image.NRGBA
}

// ParseFont assembles a Font from the three child bodies of a 0x7000 object.
func ParseFont(header, widths, data []byte, order binary.ByteOrder) (*Font, error) {
	if len(header) < 16 {
		return nil, fmt.Errorf("font header too short")
	}
	f := &Font{
		DictID:    order.Uint32(header[0:]),
		NumGlyphs: int(order.Uint32(header[4:])),
		Height:    int(order.Uint32(header[8:])),
		Mode:      int(order.Uint32(header[12:])),
	}
	if f.NumGlyphs <= 0 || f.NumGlyphs > 65536 || f.Height <= 0 || f.Height > 128 {
		return nil, fmt.Errorf("implausible font header: glyphs=%d height=%d", f.NumGlyphs, f.Height)
	}
	if len(widths) < f.NumGlyphs {
		return nil, fmt.Errorf("width table too short: %d < %d", len(widths), f.NumGlyphs)
	}

	p := 0
	if len(data) < 4 {
		return nil, fmt.Errorf("glyph data too short")
	}
	palCount := int(order.Uint32(data[p:]))
	p += 4
	if palCount < 0 || palCount > 256 || p+palCount*4 > len(data) {
		return nil, fmt.Errorf("implausible palette count %d", palCount)
	}
	f.Palette = make([]color.NRGBA, palCount)
	for i := range f.Palette {
		f.Palette[i] = color.NRGBA{data[p], data[p+1], data[p+2], data[p+3]}
		p += 4
	}

	lookup := func(idx int) color.NRGBA {
		if idx < len(f.Palette) {
			c := f.Palette[idx]
			// The glyph coverage lives in the index magnitude; palettes in
			// practice are flat white — use the index as alpha ramp so the
			// atlas is directly usable as an alpha-blended text texture.
			if palCount == 256 {
				c.A = uint8(min(idx*17, 255)) // indices observed 0..15
			} else {
				c.A = uint8(min(idx*255/max(palCount-1, 1), 255))
			}
			return c
		}
		return color.NRGBA{}
	}

	for gi := 0; gi < f.NumGlyphs; gi++ {
		if p+6 > len(data) {
			return nil, fmt.Errorf("glyph %d: stream truncated", gi)
		}
		code := order.Uint16(data[p:])
		rowBytes := int(order.Uint32(data[p+2:]))
		p += 6
		need := f.Height * rowBytes
		if rowBytes < 0 || p+need > len(data) {
			return nil, fmt.Errorf("glyph %d: rows out of range", gi)
		}
		w := int(widths[gi])
		img := image.NewNRGBA(image.Rect(0, 0, max(w, 1), f.Height))
		for y := 0; y < f.Height; y++ {
			row := data[p+y*rowBytes : p+(y+1)*rowBytes]
			for x := 0; x < w; x++ {
				var idx int
				if rowBytes >= w { // one index per byte
					idx = int(row[x])
				} else { // packed 4-bit, low nibble first
					b := row[x/2]
					if x%2 == 0 {
						idx = int(b & 0x0F)
					} else {
						idx = int(b >> 4)
					}
				}
				img.SetNRGBA(x, y, lookup(idx))
			}
		}
		p += need
		f.Glyphs = append(f.Glyphs, FontGlyph{Code: code, Width: w, Pixels: img})
	}
	return f, nil
}
