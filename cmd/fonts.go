package cmd

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"

	"github.com/average-bit/eqonvert/pkg/eqoa"
)

// writeFonts exports every Font (0x7000) as a glyph-atlas PNG (alpha
// channel = coverage, directly usable as a text texture) plus a metrics
// JSON mapping each character code to its atlas cell and advance width.
func writeFonts(r io.ReadSeeker, objects []*eqoa.ESFObject, order binary.ByteOrder, prefix string, outDir string, verbose bool) {
	count := 0

	var walk func(o *eqoa.ESFObject)
	walk = func(o *eqoa.ESFObject) {
		if uint16(o.Header.ObjectType) == 0x7000 {
			var header, widths, data []byte
			for _, c := range o.Children {
				switch uint16(c.Header.ObjectType) {
				case 0x7010:
					header, _ = c.ReadRaw(r)
				case 0x7020:
					widths, _ = c.ReadRaw(r)
				case 0x7030:
					data, _ = c.ReadRaw(r)
				}
			}
			if header != nil && widths != nil && data != nil {
				if f, err := eqoa.ParseFont(header, widths, data, order); err == nil {
					if exportFont(f, prefix, outDir) == nil {
						count++
					}
				} else if verbose {
					fmt.Printf("  Font: %v\n", err)
				}
			}
		}
		for _, c := range o.Children {
			walk(c)
		}
	}
	for _, obj := range objects {
		walk(obj)
	}

	if verbose && count > 0 {
		fmt.Printf("  → %d font(s) written as atlas PNG + metrics JSON\n", count)
	}
}

type fontGlyphMeta struct {
	Code  int `json:"code"`
	X     int `json:"x"`
	Y     int `json:"y"`
	Width int `json:"width"`
}

func exportFont(f *eqoa.Font, prefix string, outDir string) error {
	// Grid atlas: square-ish layout of fixed cells (maxWidth × height).
	cellW := 1
	for _, g := range f.Glyphs {
		if g.Width > cellW {
			cellW = g.Width
		}
	}
	cols := int(math.Ceil(math.Sqrt(float64(len(f.Glyphs)))))
	rows := (len(f.Glyphs) + cols - 1) / cols
	atlas := image.NewNRGBA(image.Rect(0, 0, cols*cellW, rows*f.Height))

	metas := make([]fontGlyphMeta, 0, len(f.Glyphs))
	for i, g := range f.Glyphs {
		x, y := (i%cols)*cellW, (i/cols)*f.Height
		draw.Draw(atlas, image.Rect(x, y, x+g.Width, y+f.Height), g.Pixels, image.Point{}, draw.Src)
		metas = append(metas, fontGlyphMeta{Code: int(g.Code), X: x, Y: y, Width: g.Width})
	}

	pngPath := filepath.Join(outDir, fmt.Sprintf("%s_font_0x%X.png", prefix, f.DictID))
	fp, err := os.Create(pngPath)
	if err != nil {
		return err
	}
	if err := png.Encode(fp, atlas); err != nil {
		fp.Close()
		return err
	}
	fp.Close()

	// OS-installable TrueType conversion (256-glyph cp1252 fonts; the CJK
	// font would need a JIS→Unicode table and stays atlas/JSON-only).
	if ttf := buildTTF(f); ttf != nil {
		ttfPath := filepath.Join(outDir, fmt.Sprintf("%s_font_0x%X.ttf", prefix, f.DictID))
		if err := os.WriteFile(ttfPath, ttf, 0644); err != nil {
			return err
		}
	}

	meta := map[string]any{
		"dict_id":    fmt.Sprintf("0x%X", f.DictID),
		"height":     f.Height,
		"mode":       f.Mode,
		"cell_width": cellW,
		"atlas":      filepath.Base(pngPath),
		"note":       "codes are ASCII for 256-glyph fonts, JIS for CJK fonts (client converts Shift-JIS via VIFont::SjisToJis)",
		"glyphs":     metas,
	}
	jsonPath := filepath.Join(outDir, fmt.Sprintf("%s_font_0x%X.json", prefix, f.DictID))
	jf, err := os.Create(jsonPath)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(jf)
	enc.SetIndent("", " ")
	err = enc.Encode(meta)
	jf.Close()
	return err
}
