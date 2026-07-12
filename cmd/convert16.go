package cmd

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
)

// .16 files are raw 640×448 16-bit fullscreen images (loading screens,
// error screens) in the PS2 GS PSMCT16 pixel format: little-endian uint16,
// A1 B5 G5 R5 — red in the low bits.  The file is exactly 640*448*2 =
// 573440 bytes with no header.
const (
	screen16Width  = 640
	screen16Height = 448
	screen16Size   = screen16Width * screen16Height * 2
)

// convert16Data decodes a .16 image and writes it as PNG.
func convert16Data(data []byte, outPath string) error {
	if len(data) < screen16Size {
		return fmt.Errorf("file too small for 640x448x16bpp: %d bytes", len(data))
	}
	img := image.NewNRGBA(image.Rect(0, 0, screen16Width, screen16Height))
	for i := 0; i < screen16Width*screen16Height; i++ {
		px := uint16(data[i*2]) | uint16(data[i*2+1])<<8
		r := uint8(px & 0x1F)
		g := uint8((px >> 5) & 0x1F)
		b := uint8((px >> 10) & 0x1F)
		// Expand 5 bits to 8 (replicate high bits into low).
		img.SetNRGBA(i%screen16Width, i/screen16Width, color.NRGBA{
			R: r<<3 | r>>2,
			G: g<<3 | g>>2,
			B: b<<3 | b>>2,
			A: 255, // fullscreen images: the alpha bit is not meaningful
		})
	}
	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}
