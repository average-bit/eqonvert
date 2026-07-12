package cmd

import (
	"bytes"
	"encoding/binary"
	"sort"

	"github.com/average-bit/eqonvert/pkg/eqoa"
)

// buildTTF converts a parsed EQOA bitmap font into a standard TrueType font
// installable on any OS.  Each glyph's coverage bitmap is thresholded and
// converted to pixel-rectangle contours (the classic bitmap→TTF approach:
// crisp at the native size, blocky when scaled — faithful to the source).
//
// Coordinate system: 64 font units per pixel, y-up, baseline placed so the
// bottom `descent` pixel rows of the cell hang below it.  Character codes
// are mapped as cp1252 (the 256-glyph fonts' encoding); fonts with more
// glyphs (the Shift-JIS CJK font) are not converted — their atlas + metrics
// JSON carry the data instead, since a faithful TTF would need a full
// JIS→Unicode table.
const ttfPxUnit = 64

// cp1252 high-range (0x80–0x9F) to Unicode; the rest of the range is
// identity with Latin-1.
var cp1252High = [32]rune{
	0x20AC, 0x81, 0x201A, 0x0192, 0x201E, 0x2026, 0x2020, 0x2021,
	0x02C6, 0x2030, 0x0160, 0x2039, 0x0152, 0x8D, 0x017D, 0x8F,
	0x90, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014,
	0x02DC, 0x2122, 0x0161, 0x203A, 0x0153, 0x9D, 0x017E, 0x0178,
}

type ttfGlyph struct {
	code                   rune
	advance                int
	contours               [][]ttfPoint // rectangles, clockwise, y-up font units
	xMin, yMin, xMax, yMax int
}

type ttfPoint struct{ x, y int }

func buildTTF(f *eqoa.Font) []byte {
	if f.NumGlyphs > 512 {
		return nil // CJK font — atlas/JSON only
	}
	descent := f.Height / 5 // baseline heuristic: bottom fifth descends
	ascent := f.Height - descent

	// Threshold: half of the strongest index used in the font.
	maxIdx := 1
	for _, g := range f.Glyphs {
		b := g.Pixels.Bounds()
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				if a := int(g.Pixels.NRGBAAt(x, y).A); a > maxIdx {
					maxIdx = a
				}
			}
		}
	}
	thr := uint8(maxIdx / 2)

	var glyphs []ttfGlyph
	for _, g := range f.Glyphs {
		code := rune(g.Code)
		if g.Code >= 0x80 && g.Code <= 0x9F {
			code = cp1252High[g.Code-0x80]
		}
		tg := ttfGlyph{code: code, advance: g.Width * ttfPxUnit}

		// Merge horizontal runs of on-pixels row by row, then extend runs
		// downward across identical consecutive rows.
		type run struct{ x0, x1, y0, y1 int } // pixel coords, y down
		var rects []run
		active := map[[2]int]*run{}
		for y := 0; y < f.Height; y++ {
			rowRuns := map[[2]int]bool{}
			x := 0
			for x < g.Width {
				if g.Pixels.NRGBAAt(x, y).A > thr {
					x0 := x
					for x < g.Width && g.Pixels.NRGBAAt(x, y).A > thr {
						x++
					}
					rowRuns[[2]int{x0, x}] = true
				} else {
					x++
				}
			}
			next := map[[2]int]*run{}
			for k := range rowRuns {
				if prev, ok := active[k]; ok && prev.y1 == y {
					prev.y1 = y + 1
					next[k] = prev
				} else {
					r := &run{k[0], k[1], y, y + 1}
					rects = append(rects, *r)
					next[k] = &rects[len(rects)-1]
				}
			}
			active = next
		}

		first := true
		for _, r := range rects {
			// y-up: pixel row y occupies font units [(hTop-y-1)*u, (hTop-y)*u]
			// with baseline at descent pixels above cell bottom.
			topY := (f.Height - descent - r.y0) * ttfPxUnit
			botY := (f.Height - descent - r.y1) * ttfPxUnit
			x0, x1 := r.x0*ttfPxUnit, r.x1*ttfPxUnit
			tg.contours = append(tg.contours, []ttfPoint{
				{x0, topY}, {x1, topY}, {x1, botY}, {x0, botY}, // clockwise, y-up
			})
			if first || x0 < tg.xMin {
				tg.xMin = x0
			}
			if first || botY < tg.yMin {
				tg.yMin = botY
			}
			if first || x1 > tg.xMax {
				tg.xMax = x1
			}
			if first || topY > tg.yMax {
				tg.yMax = topY
			}
			first = false
		}
		glyphs = append(glyphs, tg)
	}
	sort.Slice(glyphs, func(i, j int) bool { return glyphs[i].code < glyphs[j].code })

	upem := f.Height * ttfPxUnit

	// ── glyf + loca ──────────────────────────────────────────────────────
	glyf := new(bytes.Buffer)
	locas := []uint32{0} // glyph 0 = .notdef (empty)
	locas = append(locas, uint32(glyf.Len()))
	xMinF, yMinF, xMaxF, yMaxF := 0, -descent*ttfPxUnit, 0, ascent*ttfPxUnit
	for _, g := range glyphs {
		if len(g.contours) == 0 {
			locas = append(locas, uint32(glyf.Len()))
			continue
		}
		be := func(v any) { binary.Write(glyf, binary.BigEndian, v) }
		be(int16(len(g.contours)))
		be(int16(g.xMin))
		be(int16(g.yMin))
		be(int16(g.xMax))
		be(int16(g.yMax))
		if g.xMax > xMaxF {
			xMaxF = g.xMax
		}
		np := 0
		for _, c := range g.contours {
			np += len(c)
			be(uint16(np - 1))
		}
		be(uint16(0)) // no instructions
		for range npoints(g.contours) {
			glyf.WriteByte(0x01) // on-curve, full deltas
		}
		px := 0
		for _, c := range g.contours {
			for _, p := range c {
				be(int16(p.x - px))
				px = p.x
			}
		}
		py := 0
		for _, c := range g.contours {
			for _, p := range c {
				be(int16(p.y - py))
				py = p.y
			}
		}
		for glyf.Len()%4 != 0 {
			glyf.WriteByte(0)
		}
		locas = append(locas, uint32(glyf.Len()))
	}
	loca := new(bytes.Buffer)
	for _, v := range locas {
		binary.Write(loca, binary.BigEndian, v)
	}

	numGlyphs := uint16(len(glyphs) + 1)

	// ── hmtx ─────────────────────────────────────────────────────────────
	hmtx := new(bytes.Buffer)
	binary.Write(hmtx, binary.BigEndian, uint16(upem/2)) // .notdef advance
	binary.Write(hmtx, binary.BigEndian, int16(0))
	for _, g := range glyphs {
		binary.Write(hmtx, binary.BigEndian, uint16(g.advance))
		binary.Write(hmtx, binary.BigEndian, int16(g.xMin))
	}

	// ── cmap (format 4) ─────────────────────────────────────────────────
	type seg struct {
		start, end rune
		startGlyph int
	}
	var segs []seg
	for i, g := range glyphs {
		gi := i + 1
		if len(segs) > 0 && segs[len(segs)-1].end+1 == g.code &&
			segs[len(segs)-1].startGlyph+int(g.code-segs[len(segs)-1].start) == gi {
			segs[len(segs)-1].end = g.code
		} else {
			segs = append(segs, seg{g.code, g.code, gi})
		}
	}
	segs = append(segs, seg{0xFFFF, 0xFFFF, 0})
	segCount := len(segs)
	sub := new(bytes.Buffer)
	be16 := func(v uint16) { binary.Write(sub, binary.BigEndian, v) }
	be16(4)
	be16(uint16(16 + segCount*8))
	be16(0)
	be16(uint16(segCount * 2))
	sr := 1
	for sr*2 <= segCount {
		sr *= 2
	}
	be16(uint16(sr))
	es := 0
	for 1<<es < sr {
		es++
	}
	be16(uint16(es - 1))
	be16(uint16(segCount*2 - sr))
	for _, s := range segs {
		be16(uint16(s.end))
	}
	be16(0)
	for _, s := range segs {
		be16(uint16(s.start))
	}
	for _, s := range segs {
		if s.startGlyph == 0 {
			be16(0)
		} else {
			be16(uint16((s.startGlyph - int(s.start)) & 0xFFFF))
		}
	}
	for range segs {
		be16(0) // idRangeOffset
	}
	cmap := new(bytes.Buffer)
	binary.Write(cmap, binary.BigEndian, uint16(0)) // version
	binary.Write(cmap, binary.BigEndian, uint16(1)) // one table
	binary.Write(cmap, binary.BigEndian, uint16(3)) // Windows
	binary.Write(cmap, binary.BigEndian, uint16(1)) // Unicode BMP
	binary.Write(cmap, binary.BigEndian, uint32(12))
	cmap.Write(sub.Bytes())

	// ── head / hhea / maxp / OS2 / name / post ──────────────────────────
	head := new(bytes.Buffer)
	bw := func(v any) { binary.Write(head, binary.BigEndian, v) }
	bw(uint32(0x00010000))
	bw(uint32(0x00010000))
	bw(uint32(0)) // checkSumAdjustment (patched later)
	bw(uint32(0x5F0F3CF5))
	bw(uint16(0x000B)) // flags
	bw(uint16(upem))
	bw(uint64(0))
	bw(uint64(0))
	bw(int16(xMinF))
	bw(int16(yMinF))
	bw(int16(xMaxF))
	bw(int16(yMaxF))
	bw(uint16(0))
	bw(uint16(8))
	bw(int16(2))
	bw(int16(1)) // long loca
	bw(int16(0))

	hhea := new(bytes.Buffer)
	hw := func(v any) { binary.Write(hhea, binary.BigEndian, v) }
	hw(uint32(0x00010000))
	hw(int16(ascent * ttfPxUnit))
	hw(int16(-descent * ttfPxUnit))
	hw(int16(ttfPxUnit)) // lineGap: one pixel
	hw(uint16(xMaxF))
	hw(int16(0))
	hw(int16(0))
	hw(int16(xMaxF))
	hw(int16(1))
	hw(int16(0))
	hw(int16(0))
	for range 4 {
		hw(int16(0))
	}
	hw(int16(0)) // metric format
	hw(numGlyphs)

	maxp := new(bytes.Buffer)
	binary.Write(maxp, binary.BigEndian, uint32(0x00005000)) // v0.5 (no glyf stats needed)
	binary.Write(maxp, binary.BigEndian, numGlyphs)

	name := buildNameTable("EQOA " + fontFamilyName(f.DictID))
	post := new(bytes.Buffer)
	binary.Write(post, binary.BigEndian, uint32(0x00030000))
	binary.Write(post, binary.BigEndian, uint32(0))
	for range 7 {
		binary.Write(post, binary.BigEndian, uint32(0))
	}

	os2 := buildOS2(upem, ascent*ttfPxUnit, descent*ttfPxUnit)

	// maxp v0.5 is not valid for glyf fonts — use v1.0 with counts.
	maxp.Reset()
	mw := func(v any) { binary.Write(maxp, binary.BigEndian, v) }
	mw(uint32(0x00010000))
	mw(numGlyphs)
	maxPts, maxCtr := 4, 1
	for _, g := range glyphs {
		if n := npoints(g.contours); n > maxPts {
			maxPts = n
		}
		if len(g.contours) > maxCtr {
			maxCtr = len(g.contours)
		}
	}
	mw(uint16(maxPts))
	mw(uint16(maxCtr))
	// maxp 1.0 has 13 uint16 fields after numGlyphs; 11 remain
	// (composite/zone/instruction limits — all zero for this font).
	for range 11 {
		mw(uint16(0))
	}

	tables := []struct {
		tag  string
		data []byte
	}{
		{"OS/2", os2}, {"cmap", cmap.Bytes()}, {"glyf", glyf.Bytes()},
		{"head", head.Bytes()}, {"hhea", hhea.Bytes()}, {"hmtx", hmtx.Bytes()},
		{"loca", loca.Bytes()}, {"maxp", maxp.Bytes()}, {"name", name},
		{"post", post.Bytes()},
	}

	// ── sfnt assembly with checksums ─────────────────────────────────────
	n := len(tables)
	sr2 := 1
	for sr2*2 <= n {
		sr2 *= 2
	}
	out := new(bytes.Buffer)
	ow := func(v any) { binary.Write(out, binary.BigEndian, v) }
	ow(uint32(0x00010000))
	ow(uint16(n))
	ow(uint16(sr2 * 16))
	e2 := 0
	for 1<<e2 < sr2 {
		e2++
	}
	ow(uint16(e2))
	ow(uint16(n*16 - sr2*16))

	off := 12 + n*16
	type placed struct {
		tag      string
		checksum uint32
		offset   int
		length   int
	}
	var placedTables []placed
	body := new(bytes.Buffer)
	headOffset := 0
	for _, t := range tables {
		for body.Len()%4 != 0 {
			body.WriteByte(0)
		}
		o := off + body.Len()
		if t.tag == "head" {
			headOffset = o
		}
		placedTables = append(placedTables, placed{t.tag, ttfChecksum(t.data), o, len(t.data)})
		body.Write(t.data)
	}
	for _, p := range placedTables {
		out.WriteString(p.tag)
		ow(p.checksum)
		ow(uint32(p.offset))
		ow(uint32(p.length))
	}
	out.Write(body.Bytes())

	// checkSumAdjustment
	full := out.Bytes()
	total := ttfChecksum(full)
	adj := 0xB1B0AFBA - total
	binary.BigEndian.PutUint32(full[headOffset+8:], adj)
	return full
}

func npoints(contours [][]ttfPoint) int {
	n := 0
	for _, c := range contours {
		n += len(c)
	}
	return n
}

func ttfChecksum(data []byte) uint32 {
	var sum uint32
	for i := 0; i+4 <= len(data); i += 4 {
		sum += binary.BigEndian.Uint32(data[i:])
	}
	if rem := len(data) % 4; rem != 0 {
		var last [4]byte
		copy(last[:], data[len(data)-rem:])
		sum += binary.BigEndian.Uint32(last[:])
	}
	return sum
}

func fontFamilyName(dictID uint32) string {
	return "Font " + hex8(dictID)
}

func hex8(v uint32) string {
	const d = "0123456789ABCDEF"
	var b [8]byte
	for i := 7; i >= 0; i-- {
		b[i] = d[v&0xF]
		v >>= 4
	}
	return string(b[:])
}

func buildNameTable(family string) []byte {
	entries := []struct {
		id  uint16
		val string
	}{
		{1, family}, {2, "Regular"}, {3, family}, {4, family},
		{6, family}, {0, "Extracted from EQOA by eqonvert"},
	}
	strs := new(bytes.Buffer)
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.BigEndian, uint16(0))
	binary.Write(buf, binary.BigEndian, uint16(len(entries)))
	binary.Write(buf, binary.BigEndian, uint16(6+len(entries)*12))
	for _, e := range entries {
		start := strs.Len()
		for _, r := range e.val { // UTF-16BE
			binary.Write(strs, binary.BigEndian, uint16(r))
		}
		binary.Write(buf, binary.BigEndian, uint16(3)) // Windows
		binary.Write(buf, binary.BigEndian, uint16(1)) // Unicode BMP
		binary.Write(buf, binary.BigEndian, uint16(0x409))
		binary.Write(buf, binary.BigEndian, e.id)
		binary.Write(buf, binary.BigEndian, uint16(strs.Len()-start))
		binary.Write(buf, binary.BigEndian, uint16(start))
	}
	buf.Write(strs.Bytes())
	return buf.Bytes()
}

func buildOS2(upem, ascent, descent int) []byte {
	b := new(bytes.Buffer)
	w := func(v any) { binary.Write(b, binary.BigEndian, v) }
	w(uint16(1)) // version 1
	w(int16(upem / 2))
	w(uint16(400))
	w(uint16(5))
	w(uint16(0)) // fsType: installable
	for range 5 {
		w(int16(upem / 5))
	}
	for range 7 {
		w(int16(0))
	}
	w(int16(0))
	w(int16(0)) // family class
	for range 10 {
		b.WriteByte(0) // panose
	}
	for range 4 {
		w(uint32(0)) // unicode ranges
	}
	b.WriteString("EQOA")
	w(uint16(0x0040)) // fsSelection: regular
	w(uint16(0x20))
	w(uint16(0xFFFD))
	w(int16(ascent))
	w(int16(-descent))
	w(int16(64))
	w(uint16(ascent + descent))
	w(uint16(0))
	w(uint32(0)) // code page ranges (v1)
	w(uint32(0))
	return b.Bytes()
}
