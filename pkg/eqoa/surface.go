package eqoa

import (
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
)

type Surface struct {
	DictID  uint32
	Width   int32
	Height  int32
	Depth   int32
	Mip     int32
	Palette []color.Color
	Mips    [][]byte
}

func ParseSurface(data []byte, order binary.ByteOrder) (*Surface, error) {
	if len(data) < 20 {
		return nil, fmt.Errorf("surface data too short")
	}

	s := &Surface{}
	s.DictID = order.Uint32(data[0:4])
	s.Width = int32(order.Uint32(data[4:8]))
	s.Height = int32(order.Uint32(data[8:12]))
	s.Depth = int32(order.Uint32(data[12:16]))
	s.Mip = int32(order.Uint32(data[16:20]))

	offset := 20
	if s.Depth < 2 { // Paletted
		if len(data) < offset+4 {
			return nil, fmt.Errorf("surface data too short for palette size")
		}
		paletteEntries := int(order.Uint32(data[offset : offset+4]))
		offset += 4
		paletteSize := paletteEntries * 4
		if len(data) < offset+paletteSize {
			return nil, fmt.Errorf("surface data too short for palette")
		}
		s.Palette = make([]color.Color, paletteEntries)
		for i := 0; i < paletteEntries; i++ {
			r := data[offset+i*4+0]
			g := data[offset+i*4+1]
			b := data[offset+i*4+2]
			// PS2 palette alpha is 0-128 (128 = fully opaque); scale to 0-255 for PNG.
			// >= 128 saturates to 255 so fully-opaque pixels don't map to 254.
			a := data[offset+i*4+3]
			if a >= 128 {
				a = 255
			} else {
				a = a * 2
			}
			s.Palette[i] = color.RGBA{r, g, b, a}
		}
		offset += paletteSize

		// NOTE: ESF-stored CLUTs are LINEAR, not in the PS2 GS "swizzled" order.
		// A previous PSMT8 256-color CLUT de-swizzle (swapping palette entries
		// [8..15]<->[16..23] per 32-entry block) was WRONG here: it scrambled any
		// texture whose art sweeps those index ranges — e.g. item-icon faded
		// radial gradients turned into pink/green speckle, while textures that
		// avoided those indices looked fine (the "random" symptom). Verified by
		// three-way decode of ITEMICON 0xB8E7D93C (128x64, PSMT8, 256-color): the
		// linear palette renders the correct smooth gradient; the de-swizzle does
		// not. So no CLUT reorder is applied.

		// PS2 color-key: magenta (R=255, G=0, B=255) palette entries were rendered
		// transparent by the GS color-comparison feature. Their stored alpha was
		// typically 128 (=255 scaled) because alpha-blending was not used for those
		// surfaces. Zero the alpha so AlphaMode() returns MASK and ToImage() produces
		// transparent pixels instead of solid pink.
		for i, c := range s.Palette {
			if rc, ok := c.(color.RGBA); ok && rc.R == 255 && rc.G == 0 && rc.B == 255 {
				s.Palette[i] = color.RGBA{255, 0, 255, 0}
			}
		}
	}

	if s.Mip > 0 {
		s.Mips = make([][]byte, s.Mip)
		currW, currH := s.Width, s.Height
		for i := 0; i < int(s.Mip); i++ {
			if len(data) < offset+4 {
				break
			}
			// The 4-byte value is the row stride in bytes (confirmed by Ghidra
			// decompile of FUN_0040c040: the game loops height times reading
			// exactly stride bytes per row). Total mip bytes = stride × height.
			stride := int(order.Uint32(data[offset : offset+4]))
			offset += 4

			mipSize := stride * int(currH)
			if mipSize <= 0 {
				mipSize = mipPixelBytes(int(s.Depth), int(currW), int(currH))
			}
			if len(data) < offset+mipSize {
				break
			}
			s.Mips[i] = data[offset : offset+mipSize]
			offset += mipSize

			currW /= 2
			currH /= 2
			if currW < 1 {
				currW = 1
			}
			if currH < 1 {
				currH = 1
			}
		}
	}

	return s, nil
}

// mipPixelBytes returns the number of raw pixel bytes for one mip level.
// Depth encoding confirmed by Ghidra decompile of VIESFParse__ParseSurfaceObj (0x40c040):
//
//	0 = 4-bit PSMT4 (paletted, 2 pixels/byte)
//	1 = 8-bit PSMT8 (paletted, 1 pixel/byte)
//	2 = 16-bit PSMCT16 (direct, 2 bytes/pixel)
//	3 = 24-bit PSM24  (direct, 3 bytes/pixel)
//	4 = 32-bit PSMCT32 (direct, 4 bytes/pixel)
func mipPixelBytes(depth, w, h int) int {
	switch depth {
	case 0:
		n := (w * h) / 2
		if n < 1 {
			n = 1
		}
		return n
	case 1:
		return w * h
	case 2:
		return w * h * 2
	case 3:
		return w * h * 3
	case 4:
		return w * h * 4
	}
	return w * h
}

// AlphaMode returns the glTF alphaMode string appropriate for this surface.
//
//	depth 3 (PSM24)         → OPAQUE  (no alpha channel)
//	depth 2 (PSMCT16)       → MASK    (1-bit A in A1B5G5R5)
//	depth 0/1 (PSMT4/PSMT8) → inspect palette: all-255 → OPAQUE, 0-or-255 only → MASK, else → BLEND
//	depth 4 (PSMCT32)       → BLEND   (full 8-bit alpha)
func (s *Surface) AlphaMode() string {
	switch s.Depth {
	case 3:
		return "OPAQUE"
	case 2:
		return "MASK"
	case 0, 1:
		// Paletted textures (PSMT4/PSMT8): the PS2 GS rendered these with alpha-test
		// (discard pixels below a threshold), not alpha-blending. Intermediate palette
		// alpha values (e.g. PS2 alpha=23 → GLTF alpha=46) are "nearly transparent"
		// background entries used as soft color keys. Using BLEND causes these to
		// produce visible pink/colored halos around foliage in GLTF viewers; MASK
		// with the default alphaCutoff=0.5 correctly discards them.
		allOpaque := true
		for _, c := range s.Palette {
			_, _, _, a := c.RGBA()
			if uint8(a>>8) != 255 {
				allOpaque = false
				break
			}
		}
		if allOpaque {
			return "OPAQUE"
		}
		return "MASK"
	default: // PSMCT32
		return "BLEND"
	}
}

// TranslucentFraction returns the fraction of mip-0 pixels whose palette alpha
// falls in the mid band (neither near-transparent nor near-opaque) — the
// signature of a genuine translucency gradient (sheer cloth) rather than a hard
// cutout mask. The band starts at 64 to skip foliage "soft-key" entries (PS2
// alpha ~23 → ~46 after scaling), which would otherwise read as translucent.
// Only meaningful for paletted depths (0/1); returns 0 otherwise.
//
// This is advisory: callers in the character/item path use it to upgrade a
// gradient surface from MASK to BLEND. Zones deliberately do NOT, so foliage
// keeps its alpha-test MASK (BLEND there produces colored halos).
func (s *Surface) TranslucentFraction() float64 {
	if len(s.Mips) == 0 || len(s.Palette) == 0 {
		return 0
	}
	px := s.Mips[0]
	total := int(s.Width) * int(s.Height)
	if total == 0 {
		return 0
	}
	const aLo, aHi = 64, 208
	alphaOf := func(pIdx int) uint8 {
		if pIdx < len(s.Palette) {
			_, _, _, a := s.Palette[pIdx].RGBA()
			return uint8(a >> 8)
		}
		return 255
	}
	mid := 0
	switch s.Depth {
	case 0: // 4-bit PSMT4: low nibble = even pixel, high nibble = odd pixel
		for i := 0; i < total; i++ {
			b := i / 2
			if b >= len(px) {
				break
			}
			var pIdx int
			if i%2 == 0 {
				pIdx = int(px[b] & 0x0F)
			} else {
				pIdx = int((px[b] >> 4) & 0x0F)
			}
			if a := alphaOf(pIdx); a >= aLo && a <= aHi {
				mid++
			}
		}
	case 1: // 8-bit PSMT8: 1 byte per pixel
		for i := 0; i < total; i++ {
			if i >= len(px) {
				break
			}
			if a := alphaOf(int(px[i])); a >= aLo && a <= aHi {
				mid++
			}
		}
	default:
		return 0
	}
	return float64(mid) / float64(total)
}

func (s *Surface) ToImage(mipIndex int) (image.Image, error) {
	if s.Width == 0 || s.Height == 0 {
		return nil, fmt.Errorf("invalid dimensions")
	}
	if len(s.Mips) == 0 {
		return nil, fmt.Errorf("no mip data")
	}
	if mipIndex < 0 || mipIndex >= len(s.Mips) {
		mipIndex = 0
	}

	mipW := int(s.Width)
	mipH := int(s.Height)
	for i := 0; i < mipIndex; i++ {
		mipW /= 2
		mipH /= 2
	}
	if mipW < 1 {
		mipW = 1
	}
	if mipH < 1 {
		mipH = 1
	}

	img := image.NewRGBA(image.Rect(0, 0, mipW, mipH))
	pixels := s.Mips[mipIndex]

	switch s.Depth {
	case 0: // 4-bit PSMT4, paletted — stored linearly: low nibble = even pixel, high nibble = odd pixel
		for y := 0; y < mipH; y++ {
			for x := 0; x < mipW; x++ {
				linearIdx := y*mipW + x
				byteIdx := linearIdx / 2
				if byteIdx >= len(pixels) {
					break
				}
				var pIdx int
				if linearIdx%2 == 0 {
					pIdx = int(pixels[byteIdx] & 0x0F)
				} else {
					pIdx = int((pixels[byteIdx] >> 4) & 0x0F)
				}
				if pIdx < len(s.Palette) {
					img.Set(x, y, s.Palette[pIdx])
				}
			}
		}

	case 1: // 8-bit PSMT8, paletted — stored linearly, 1 byte per pixel
		for y := 0; y < mipH; y++ {
			for x := 0; x < mipW; x++ {
				idx := y*mipW + x
				if idx >= len(pixels) {
					break
				}
				pIdx := int(pixels[idx])
				if pIdx < len(s.Palette) {
					img.Set(x, y, s.Palette[pIdx])
				}
			}
		}

	case 3: // 24-bit PSM24: R, G, B — no alpha channel, fully opaque
		for y := 0; y < mipH; y++ {
			for x := 0; x < mipW; x++ {
				i := (y*mipW + x) * 3
				if i+3 > len(pixels) {
					break
				}
				img.Set(x, y, color.RGBA{pixels[i], pixels[i+1], pixels[i+2], 255})
			}
		}

	case 2: // 16-bit PSMCT16: A(1)B(5)G(5)R(5), little-endian
		for y := 0; y < mipH; y++ {
			for x := 0; x < mipW; x++ {
				i := (y*mipW + x) * 2
				if i+2 > len(pixels) {
					break
				}
				v := uint16(pixels[i]) | uint16(pixels[i+1])<<8
				r := uint8((v&0x1F)<<3) | uint8((v&0x1F)>>2)
				g := uint8(((v>>5)&0x1F)<<3) | uint8(((v>>5)&0x1F)>>2)
				b := uint8(((v>>10)&0x1F)<<3) | uint8(((v>>10)&0x1F)>>2)
				var a uint8
				if v>>15 != 0 {
					a = 255
				}
				img.Set(x, y, color.RGBA{r, g, b, a})
			}
		}

	case 4: // 32-bit PSMCT32: R, G, B, A (A is 0-128, scale to 0-255)
		for y := 0; y < mipH; y++ {
			for x := 0; x < mipW; x++ {
				i := (y*mipW + x) * 4
				if i+4 > len(pixels) {
					break
				}
				a := pixels[i+3]
				if a >= 128 {
					a = 255
				} else {
					a = a * 2
				}
				img.Set(x, y, color.RGBA{pixels[i], pixels[i+1], pixels[i+2], a})
			}
		}
	}

	return img, nil
}

// Unswizzle8 converts a swizzled PSMT8 (8-bit paletted) texture to linear order.
//
// PS2 PSMT8 memory layout:
//
//	Page:   128×64 pixels — pagesPerRow = max(1, width/128)
//	Block:  16×16 pixels  — up to 8 blocks wide per page row (min(width/16, 8))
//	Column: 8×4 pixels    — 2 wide × 4 tall per block
//	Within column: rows 0,1,2,3 stored at nibble-offsets 0,16,8,24 (rows 1 and 2 are swapped)
//
// The blocksPerRow calculation uses the texture width rather than the full page width
// so that sub-page textures (< 128 pixels wide) address their compact storage correctly.
func Unswizzle8(width, height int, pixels []byte) []byte {
	out := make([]byte, len(pixels))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			src := unswizzle8_coord(x, y, width)
			if src < len(pixels) {
				out[y*width+x] = pixels[src]
			}
		}
	}
	return out
}

func unswizzle8_coord(x, y, width int) int {
	pagesPerRow := width / 128
	if pagesPerRow < 1 {
		pagesPerRow = 1
	}
	pageX := x / 128
	pageY := y / 64
	pageAddr := (pageY*pagesPerRow + pageX) * 128 * 64
	x %= 128
	y %= 64

	// Use actual texture blocks-per-row, capped at the per-page maximum of 8.
	// Hard-coding 8 is correct only for full-page-wide (128px) textures; sub-page
	// textures store blocks contiguously at texture width, not page width.
	blocksPerRow := width / 16
	if blocksPerRow < 1 {
		blocksPerRow = 1
	}
	if blocksPerRow > 8 {
		blocksPerRow = 8
	}
	blockX := x / 16
	blockY := y / 16
	blockAddr := (blockY*blocksPerRow + blockX) * 16 * 16
	x %= 16
	y %= 16

	columnX := x / 8
	columnY := y / 4
	columnAddr := (columnY*2 + columnX) * 8 * 4
	x %= 8
	y %= 4

	rowOffsets := []int{0, 16, 8, 24}
	return pageAddr + blockAddr + columnAddr + rowOffsets[y] + x
}

// Unswizzle4 converts a swizzled PSMT4 (4-bit paletted) texture to a linear
// slice of palette indices — one byte per pixel (values 0–15).
//
// PS2 PSMT4 memory layout:
//
//	Page:   128×128 pixels — pagesPerRow = max(1, width/128)
//	Block:  32×16 pixels   — up to 4 blocks wide per page row (min(width/32, 4))
//	Column: 32×4 pixels    — 1 wide × 4 tall per block
//	Within column: rows 0,1,2,3 stored at nibble-offsets 0,64,32,96 (rows 1 and 2 are swapped)
func Unswizzle4(width, height int, pixels []byte) []byte {
	out := make([]byte, width*height)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			nibbleIdx := unswizzle4_coord(x, y, width)
			srcByte := nibbleIdx / 2
			if srcByte >= len(pixels) {
				continue
			}
			var pIdx byte
			if nibbleIdx%2 == 0 {
				pIdx = pixels[srcByte] & 0x0F
			} else {
				pIdx = (pixels[srcByte] >> 4) & 0x0F
			}
			out[y*width+x] = pIdx
		}
	}
	return out
}

func unswizzle4_coord(x, y, width int) int {
	pagesPerRow := width / 128
	if pagesPerRow < 1 {
		pagesPerRow = 1
	}
	pageX := x / 128
	pageY := y / 128
	pageAddr := (pageY*pagesPerRow + pageX) * 128 * 128 // nibbles
	x %= 128
	y %= 128

	blocksPerRow := width / 32
	if blocksPerRow < 1 {
		blocksPerRow = 1
	}
	if blocksPerRow > 4 {
		blocksPerRow = 4
	}
	blockX := x / 32
	blockY := y / 16
	blockAddr := (blockY*blocksPerRow + blockX) * 32 * 16 // nibbles
	x %= 32
	y %= 16

	// One column per block row (column width = block width = 32 pixels)
	columnY := y / 4
	columnAddr := columnY * 32 * 4 // nibbles
	y %= 4

	rowOffsets := []int{0, 64, 32, 96}
	return pageAddr + blockAddr + columnAddr + rowOffsets[y] + x
}
