package cmd

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/average-bit/eqonvert/pkg/eqoa"
	"github.com/average-bit/eqonvert/pkg/gltf"
	"github.com/schollz/progressbar/v3"
	"github.com/spf13/cobra"
)

// spriteLibEntry is the highest-LOD mesh for one LODSprite type.
type spriteLibEntry struct {
	obj   *eqoa.ESFObject
	r     io.ReadSeeker
	order binary.ByteOrder
}

// SpriteLibrary maps LODSprite dictIDs → highest-LOD SimpleSprite + its source reader.
// The reader is always a seekable in-memory buffer, so concurrent field access is not needed.
type SpriteLibrary map[uint32]*spriteLibEntry

// parseSpriteLibrary builds a SpriteLibrary from any ESF that contains
// 0x2A00 containers (SCENE.ESF, ZONE*.ESF, etc.).
func parseSpriteLibrary(r io.ReadSeeker, objects []*eqoa.ESFObject, order binary.ByteOrder) SpriteLibrary {
	lib := SpriteLibrary{}
	// headerDictID reads the first u32 of a sprite's header child (0x2001/0x2210/
	// 0x2c10). Zone actors reference this resource id, which is not always the same
	// value esf.go records on the parent sprite object.
	headerDictID := func(sprite *eqoa.ESFObject, hdrType uint16) uint32 {
		for _, c := range sprite.Children {
			if uint16(c.Header.ObjectType) == hdrType && c.BodySize >= 4 {
				if _, err := r.Seek(c.BodyOffset, io.SeekStart); err != nil {
					return 0
				}
				var id uint32
				if binary.Read(r, order, &id) == nil {
					return id
				}
			}
		}
		return 0
	}
	add := func(id uint32, obj *eqoa.ESFObject) {
		if id != 0 {
			if _, exists := lib[id]; !exists {
				lib[id] = &spriteLibEntry{obj: obj, r: r, order: order}
			}
		}
	}
	var walk func(o *eqoa.ESFObject)
	walk = func(o *eqoa.ESFObject) {
		tc := uint16(o.Header.ObjectType)
		if tc == 0x2A00 {
			var lodID uint32
			var highestLOD *eqoa.ESFObject
			for _, c := range o.Children {
				ct := uint16(c.Header.ObjectType)
				if ct == 0x2A10 {
					lodID = c.DictID
				} else if ct == 0x2A20 && len(c.Children) > 0 {
					highestLOD = c.Children[0]
				}
			}
			if lodID != 0 && highestLOD != nil {
				lib[lodID] = &spriteLibEntry{obj: highestLOD, r: r, order: order}
			}
			return
		}
		// Standalone (non-LOD) sprites: index by both the parent DictID and the
		// header id so actors referencing SimpleSprite/HSprite/GroupSprite geometry
		// (the bulk of zone buildings/props) resolve instead of being skipped.
		switch tc {
		case 0x2000:
			add(o.DictID, o)
			add(headerDictID(o, 0x2001), o)
		case 0x2200:
			add(o.DictID, o)
			add(headerDictID(o, 0x2210), o)
		case 0x2C00:
			add(o.DictID, o)
			add(headerDictID(o, 0x2C10), o)
		case 0x2700:
			// GroupSprites (creatures such as the spawn placeholder) — index so
			// they can be resolved as a placeholder / cross-file reference.
			add(o.DictID, o)
			add(headerDictID(o, 0x2710), o)
		}
		for _, c := range o.Children {
			walk(c)
		}
	}
	for _, o := range objects {
		walk(o)
	}
	return lib
}

// spriteLibFromData parses raw ESF/CSF bytes and extracts LODSprite definitions.
func spriteLibFromData(data []byte) SpriteLibrary {
	var r io.ReadSeeker
	if len(data) >= 4 && string(data[:4]) == eqoa.MagicCESF {
		dr, _, err := eqoa.DecompressCSF(bytes.NewReader(data))
		if err != nil {
			return nil
		}
		all, err := io.ReadAll(dr)
		if err != nil {
			return nil
		}
		r = bytes.NewReader(all)
	} else {
		r = bytes.NewReader(data)
	}
	_, objects, _, order, err := eqoa.ParseESF(r)
	if err != nil {
		return nil
	}
	return parseSpriteLibrary(r, objects, order)
}

// spriteLibFromFile reads a file and returns a SpriteLibrary (nil on error or no sprites).
func spriteLibFromFile(path string) SpriteLibrary {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return spriteLibFromData(data)
}

// resourceSource lazily provides the decompressed ESF stream of one resource
// file. It decompresses at most once — on the first lookup that actually needs
// the file — and caches the bytes. Files whose DictIDs are never resolved during
// zone assembly are therefore never held resident. This is the fix for the
// multi-GB resident memory a full-disc conversion otherwise pays: the old design
// decompressed AND retained every file (TUNARIA ~1 GB, CHAR ~148 MB, …) up front.
type resourceSource struct {
	open func() ([]byte, error) // reads the RAW file bytes (filesystem or ISO)
	once sync.Once
	data []byte
	err  error
}

// reader returns a fresh ReadSeeker over the file's decompressed bytes,
// decompressing (once) on first use. Each call hands out a new bytes.Reader over
// the shared buffer, so independent seeks (ReadObjectAt, then LoadSpriteMaterials)
// never fight over one cursor.
func (s *resourceSource) reader() (io.ReadSeeker, error) {
	s.once.Do(func() {
		raw, err := s.open()
		if err != nil {
			s.err = err
			return
		}
		s.data = decompressResourceBytes(raw)
	})
	if s.err != nil {
		return nil, s.err
	}
	return bytes.NewReader(s.data), nil
}

// decompressResourceBytes returns the decompressed ESF stream for a raw resource
// file (CSF → inflate; plain ESF → returned as-is). Returns nil on error.
func decompressResourceBytes(data []byte) []byte {
	if len(data) >= 4 && string(data[:4]) == eqoa.MagicCESF {
		dr, _, err := eqoa.DecompressCSF(bytes.NewReader(data))
		if err != nil {
			return nil
		}
		all, err := io.ReadAll(dr)
		if err != nil {
			return nil
		}
		return all
	}
	return data
}

// resourceEntry is one DictID's cross-file resource: its byte order, the absolute
// offset/size of the object within the source stream, and a shared lazy source
// for the file it lives in. The object is parsed lazily on lookup (unlike
// spriteLibEntry, which pre-parses).
type resourceEntry struct {
	src   *resourceSource
	order binary.ByteOrder
	ref   eqoa.ResourceRef
}

// ResourceDirectory maps a ZoneActor DictID → the resource that resolves it,
// unioned from the 0x9000 ResourceTable objects across the zone file and its
// shared siblings (CHAR/AMBTRACK/ITEM/ITEMICON + the world/scene file). It is
// the on-disk equivalent of the client's VIZone::Find directory and recovers
// the streamed static props (~62% "zone_actor_skip") plus cross-file
// creature/item placements the local sprite-library scan misses.
type ResourceDirectory map[uint32]*resourceEntry

// resourceDirFromData parses the 0x9000 resource tables out of raw ESF/CSF bytes
// and returns a ResourceDirectory whose entries share one lazy resourceSource.
// The `open` closure re-reads the RAW file bytes on demand (from the filesystem
// or an ISO), so the decompressed stream used here to read the tables is NOT
// retained — it is dropped when this function returns and re-materialized only if
// a lookup actually hits this file.
func resourceDirFromData(data []byte, open func() ([]byte, error)) ResourceDirectory {
	dec := decompressResourceBytes(data)
	if dec == nil {
		return nil
	}
	r := bytes.NewReader(dec)
	_, objects, _, order, err := eqoa.ParseESF(r)
	if err != nil {
		return nil
	}
	refs := eqoa.ParseResourceTables(r, objects, order)
	if len(refs) == 0 {
		return nil
	}
	src := &resourceSource{open: open}
	dir := make(ResourceDirectory, len(refs))
	for id, ref := range refs {
		dir[id] = &resourceEntry{src: src, order: order, ref: ref}
	}
	return dir
}

// buildResourceDirFromDir scans a directory for ESF/CSF files and merges their
// resource directories (first-seen DictID wins). Mirrors buildSpriteLibFromDir.
func buildResourceDirFromDir(dir string) ResourceDirectory {
	merged := ResourceDirectory{}
	filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !isAssetExt(p) {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		// p is the Walk callback's parameter (unique per call), safe to capture.
		for id, entry := range resourceDirFromData(data, func() ([]byte, error) { return os.ReadFile(p) }) {
			if _, exists := merged[id]; !exists {
				merged[id] = entry
			}
		}
		return nil
	})
	return merged
}

var convertZoneCmd = &cobra.Command{
	Use:   "convert-zone <path>",
	Short: "Assemble per-zone GLBs from EQOA zone ESF files",
	// Lives under `eqonvert dev` — the tool's focal point is `convert`, which
	// extracts everything (models, textures, audio, assembled zones with animated
	// props) from a disc in one pass. convert-zone is the standalone zone-assembly
	// entry point, kept for reverse-engineering/debugging.
	Long: `Assembles all terrain sprites within each Zone object into a single GLB.

Accepts:
  - A single zone .esf file (e.g. TUNARIA.ESF)
  - A directory — walks for zone ESF files containing Zone (0x3000) objects
  - A disc image (.iso)

ESF files that contain no Zone objects (CHAR.ESF, ITEM.ESF, etc.) are silently
skipped, so pointing this at an entire disc directory is safe.

Individual per-sprite GLBs from 'convert' are unaffected. This command adds
assembled zone GLBs alongside them.

Output: PREFIX_zone_N.glb per zone + PREFIX_zones.json manifest.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path := args[0]
		outDir := outputDir
		if outDir == "" {
			outDir = "."
		}

		info, err := os.Stat(path)
		if err != nil {
			return err
		}

		if info.IsDir() {
			// Build sprite + light libraries from companion files (SCENE.ESF etc).
			lib := buildSpriteLibFromDir(path)
			lightLib := buildLightLibFromDir(path)
			resDir := buildResourceDirFromDir(path)

			// Pass 1: collect ESF/CSF paths.
			var esfPaths []string
			filepath.Walk(path, func(p string, f os.FileInfo, err error) error {
				if err != nil || f.IsDir() || !isAssetExt(p) {
					return err
				}
				esfPaths = append(esfPaths, p)
				return nil
			})
			// Pass 2: assemble zones with progress bar.
			bar := newBar(len(esfPaths), "Assembling zones")
			totalZones := 0
			for _, p := range esfPaths {
				base := filepath.Base(p)
				bar.Describe(fmt.Sprintf("%-20s", base))
				data, err := os.ReadFile(p)
				if err != nil {
					bar.Add(1)
					continue
				}
				totalZones += convertZoneESFData(data, base, outDir, verbose, lib, lightLib, resDir, nil)
				bar.Add(1)
			}
			bar.Finish()
			logf("%d zone GLB(s) from %d file(s)\n", totalZones, len(esfPaths))
			return nil
		}

		ext := strings.ToUpper(filepath.Ext(path))
		if ext == ".ISO" {
			convertZoneISO(path, outDir)
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lib := buildSpriteLibFromDir(filepath.Dir(path))
		lightLib := buildLightLibFromDir(filepath.Dir(path))
		resDir := buildResourceDirFromDir(filepath.Dir(path))
		if n := convertZoneESFData(data, filepath.Base(path), outDir, verbose, lib, lightLib, resDir, nil); n > 0 {
			logf("%d zone GLB(s)\n", n)
		}
		return nil
	},
}

// buildSpriteLibFromDir scans a directory for ESF/CSF files that contain
// LODSprite geometry (e.g. SCENE.ESF) and returns a merged SpriteLibrary.
func buildSpriteLibFromDir(dir string) SpriteLibrary {
	merged := SpriteLibrary{}
	filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !isAssetExt(p) {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		lib := spriteLibFromData(data)
		for id, entry := range lib {
			if _, exists := merged[id]; !exists {
				merged[id] = entry
			}
		}
		return nil
	})
	return merged
}

// lightDef is a parsed 0x2b00 PointLight: linear RGB color + radius (world units).
type lightDef struct {
	color  [3]float32
	radius float32
}

// LightLibrary maps a PointLight resource DictID → its color/radius. Zone actors
// (0x6000) reference these by modelId; position comes from the actor transform.
type LightLibrary map[uint32]lightDef

// parseLightLibrary collects every 0x2b00 PointLight def in the tree.
// Body layout (verified against SCENE.ESF): [0:4] DictID, [4:8] radius f32,
// [8:20] RGB f32x3 ([20:24] alpha, unused).
func parseLightLibrary(r io.ReadSeeker, objects []*eqoa.ESFObject, order binary.ByteOrder) LightLibrary {
	ll := LightLibrary{}
	var walk func(o *eqoa.ESFObject)
	walk = func(o *eqoa.ESFObject) {
		if uint16(o.Header.ObjectType) == 0x2B00 {
			body, err := o.ReadBody(r)
			if err == nil && len(body) >= 20 {
				did := order.Uint32(body[0:4])
				if did != 0 {
					if _, ok := ll[did]; !ok {
						ll[did] = lightDef{
							radius: math.Float32frombits(order.Uint32(body[4:8])),
							color: [3]float32{
								math.Float32frombits(order.Uint32(body[8:12])),
								math.Float32frombits(order.Uint32(body[12:16])),
								math.Float32frombits(order.Uint32(body[16:20])),
							},
						}
					}
				}
			}
			return
		}
		for _, c := range o.Children {
			walk(c)
		}
	}
	for _, o := range objects {
		walk(o)
	}
	return ll
}

// lightLibFromData parses raw ESF/CSF bytes and extracts PointLight defs.
func lightLibFromData(data []byte) LightLibrary {
	var r io.ReadSeeker
	if len(data) >= 4 && string(data[:4]) == eqoa.MagicCESF {
		dr, _, err := eqoa.DecompressCSF(bytes.NewReader(data))
		if err != nil {
			return nil
		}
		all, err := io.ReadAll(dr)
		if err != nil {
			return nil
		}
		r = bytes.NewReader(all)
	} else {
		r = bytes.NewReader(data)
	}
	_, objects, _, order, err := eqoa.ParseESF(r)
	if err != nil {
		return nil
	}
	return parseLightLibrary(r, objects, order)
}

// buildLightLibFromDir scans a directory for PointLight defs (mirrors
// buildSpriteLibFromDir), first-seen wins.
func buildLightLibFromDir(dir string) LightLibrary {
	merged := LightLibrary{}
	filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !isAssetExt(p) {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		for id, ld := range lightLibFromData(data) {
			if _, ok := merged[id]; !ok {
				merged[id] = ld
			}
		}
		return nil
	})
	return merged
}

func convertZoneISO(isoPath, outDir string) {
	f, err := os.Open(isoPath)
	if err != nil {
		logf("Error opening ISO: %v\n", err)
		return
	}
	defer f.Close()

	isoFilter := func(p string) bool {
		dot := strings.LastIndexByte(p, '.')
		if dot < 0 {
			return false
		}
		ext := p[dot+1:]
		return ext == "CSF" || ext == "ESF"
	}
	scan := newSpinner("Scanning disc…")
	files, err := eqoa.ReadISOFiles(f, isoFilter)
	scan.Finish()
	if err != nil {
		logf("Error reading ISO: %v\n", err)
		return
	}
	// Build sprite + light libraries and the cross-file resource directory from
	// all files on the disc (finds SCENE.ESF etc).
	lib := SpriteLibrary{}
	lightLib := LightLibrary{}
	resDir := ResourceDirectory{}
	for _, isoFile := range files {
		isoFile := isoFile // capture for the lazy re-read closure
		data, err := isoFile.ReadAll(f)
		if err != nil {
			continue
		}
		for id, entry := range spriteLibFromData(data) {
			if _, exists := lib[id]; !exists {
				lib[id] = entry
			}
		}
		for id, ld := range lightLibFromData(data) {
			if _, exists := lightLib[id]; !exists {
				lightLib[id] = ld
			}
		}
		for id, entry := range resourceDirFromData(data, func() ([]byte, error) { return isoFile.ReadAll(f) }) {
			if _, exists := resDir[id]; !exists {
				resDir[id] = entry
			}
		}
	}

	bar := newBar(len(files), "Assembling zones")
	totalZones := 0
	for _, isoFile := range files {
		shortName := isoFile.Path[strings.LastIndexByte(isoFile.Path, '/')+1:]
		bar.Describe(fmt.Sprintf("%-20s", shortName))
		data, err := isoFile.ReadAll(f)
		if err != nil {
			bar.Add(1)
			continue
		}
		n := convertZoneESFData(data, shortName, outDir, false, lib, lightLib, resDir, nil)
		totalZones += n
		bar.Add(1)
	}
	bar.Finish()
	logf("%d zone GLB(s)\n", totalZones)
}

type zoneManifestEntry struct {
	Index       int        `json:"index"`
	GLB         string     `json:"glb"`
	Collision   string     `json:"collision,omitempty"` // sidecar collision GLB, if any
	Name        string     `json:"name,omitempty"`      // in-game area name from map tile data
	SpriteCount int        `json:"sprite_count"`
	MinPos      [3]float32 `json:"min_pos,omitempty"`
	MaxPos      [3]float32 `json:"max_pos,omitempty"`
}

type zoneManifest struct {
	Source string              `json:"source"`
	Zones  []zoneManifestEntry `json:"zones"`
}

// buildGlobalSurfacePool scans all 0x1001 surface arrays in the parsed ESF tree
// and returns a map of DictID → first-encountered Surface. This covers textures
// that live in one zone's palette but are referenced by sprites placed in other
// zones — e.g. the broad-leafed tree's leaf texture (0x63AB3C90) appears in some
// zones' palettes but not in every zone that contains that tree type.
func buildGlobalSurfacePool(objects []*eqoa.ESFObject, r io.ReadSeeker, order binary.ByteOrder) map[uint32]*eqoa.Surface {
	pool := make(map[uint32]*eqoa.Surface)
	var walk func(o *eqoa.ESFObject)
	walk = func(o *eqoa.ESFObject) {
		if uint16(o.Header.ObjectType) == 0x1001 {
			for _, sObj := range o.Children {
				body, _ := sObj.ReadBody(r)
				s, err := eqoa.ParseSurface(body, order)
				if err == nil && s != nil {
					if _, exists := pool[s.DictID]; !exists {
						pool[s.DictID] = s
					}
				}
			}
			return
		}
		for _, c := range o.Children {
			walk(c)
		}
	}
	for _, o := range objects {
		walk(o)
	}
	return pool
}

// subBlock is one ZonePreTranslations (0x3250) anchor selected for a zone: an
// (East, HeightRef, North) world reference plus its terrain-palette index. The
// 0x3250 table is shared across zone boundaries, so only the sub-blocks nearest
// the zone footprint are kept (see zoneConverter.filterSubBlocks).
type subBlock struct {
	East, HeightRef, North float32
	Idx                    int
}

// spriteEntry is a terrain sprite staged during pass 1. Geometry emission is
// deferred until after actor placement so actors precede terrain in the GLB
// primitive list (cleaner scene ordering for viewers).
type spriteEntry struct {
	asset      *eqoa.Asset
	spriteName string
	sb         subBlock
	matStart   int // base GLTF material index for this sprite
}

// zoneConverter holds the per-file context for assembling every Zone (0x3000)
// object in one ESF into GLBs: the decompressed reader, parsed pools, the
// companion libraries, and the spawn-placeholder configuration. One is built by
// newZoneConverter and drives run → buildZone → writeZone.
type zoneConverter struct {
	r        io.ReadSeeker
	order    binary.ByteOrder
	lib      SpriteLibrary
	lightLib LightLibrary
	resDir   ResourceDirectory
	registry *eqoa.SurfaceRegistry
	surfPool map[uint32]*eqoa.Surface

	sourceName string
	prefix     string
	outDir     string
	verbose    bool

	// spawn-placeholder config for unresolved (cross-file) ZoneActors
	useMarker        bool
	placeholderEntry *spriteLibEntry
	placeholderScale float32
}

// newZoneConverter decompresses (if CSF) and parses the ESF, merges the file's
// inline PointLight defs into lightLib, builds the file-wide surface pool, and
// resolves the spawn-placeholder configuration. It returns the converter, the
// Zone (0x3000) objects to build, and ok=false when the data is not a decodable
// ESF or contains no zones (so the caller writes nothing).
func newZoneConverter(data []byte, sourceName, outDir string, verbose bool, lib SpriteLibrary, lightLib LightLibrary, resDir ResourceDirectory, registry *eqoa.SurfaceRegistry) (*zoneConverter, []*eqoa.ESFObject, bool) {
	var esfReader io.ReadSeeker
	if len(data) >= 4 && string(data[:4]) == eqoa.MagicCESF {
		dr, _, err := eqoa.DecompressCSF(bytes.NewReader(data))
		if err != nil {
			return nil, nil, false
		}
		all, err := io.ReadAll(dr)
		if err != nil {
			return nil, nil, false
		}
		esfReader = bytes.NewReader(all)
	} else {
		esfReader = bytes.NewReader(data)
	}

	_, objects, _, order, err := eqoa.ParseESF(esfReader)
	if err != nil {
		return nil, nil, false
	}

	// Merge this file's own PointLight defs (inline lights, e.g. monolithic
	// TUNARIA.ESF) into the companion light library.
	if lightLib == nil {
		lightLib = LightLibrary{}
	}
	for id, ld := range parseLightLibrary(esfReader, objects, order) {
		if _, ok := lightLib[id]; !ok {
			lightLib[id] = ld
		}
	}

	prefix := sourceName
	if dot := strings.LastIndexByte(prefix, '.'); dot >= 0 {
		prefix = prefix[:dot]
	}

	// Collect all Zone (0x3000) objects from the full tree.
	var zones []*eqoa.ESFObject
	var collectZones func(obj *eqoa.ESFObject)
	collectZones = func(obj *eqoa.ESFObject) {
		if uint16(obj.Header.ObjectType) == 0x3000 {
			zones = append(zones, obj)
			return
		}
		for _, child := range obj.Children {
			collectZones(child)
		}
	}
	for _, obj := range objects {
		collectZones(obj)
	}
	if len(zones) == 0 {
		return nil, nil, false
	}

	// File-wide surface pool: textures that appear in some zones' palettes but
	// not in every zone that references them still resolve for sprite materials.
	surfPool := buildGlobalSurfacePool(objects, esfReader, order)

	// Spawn marking: unresolved ZoneActors are cross-file references (mobs, NPCs)
	// we can't resolve to geometry and would otherwise drop silently. Flag each
	// with a placeholder so spawn locations are visible.
	//   --mark-spawns        → the built-in, self-contained marker (default)
	//   ZONE_PLACEHOLDER=hex → advanced override: a real game sprite (must be in
	//                          the cross-file library)
	//   --spawn-scale        → size multiplier (markers are small vs a ~2000-unit zone)
	useMarker := markSpawns
	var placeholderEntry *spriteLibEntry
	placeholderScale := float32(1.0)
	if spawnScale > 0 {
		placeholderScale = float32(spawnScale)
	}
	if idStr := os.Getenv("ZONE_PLACEHOLDER"); idStr != "" && lib != nil {
		hexID := strings.TrimPrefix(strings.TrimPrefix(idStr, "0x"), "0X")
		if id, err := strconv.ParseUint(hexID, 16, 32); err == nil {
			placeholderEntry = lib[uint32(id)]
		}
		if placeholderEntry != nil {
			useMarker = false // a resolvable game sprite overrides the built-in marker
		}
	}

	return &zoneConverter{
		r: esfReader, order: order,
		lib: lib, lightLib: lightLib, resDir: resDir, registry: registry,
		surfPool: surfPool, sourceName: sourceName, prefix: prefix,
		outDir: outDir, verbose: verbose,
		useMarker: useMarker, placeholderEntry: placeholderEntry, placeholderScale: placeholderScale,
	}, zones, true
}

// convertZoneESFData decompresses (if CSF), finds Zone (0x3000) objects, and
// writes one assembled GLB per zone plus a JSON manifest. Files without zones
// are silently skipped. verbose=true prints per-zone progress lines.
// lib provides LODSprite geometry from companion files (e.g. SCENE.ESF); pass nil if unavailable.
// resDir is the cross-file resource directory (0x9000 ResourceTable union) used
// to resolve ZoneActor DictIDs the local sprite scan misses; pass nil to disable.
// Returns the number of zone GLBs written.
func convertZoneESFData(data []byte, sourceName, outDir string, verbose bool, lib SpriteLibrary, lightLib LightLibrary, resDir ResourceDirectory, registry *eqoa.SurfaceRegistry) int {
	c, zones, ok := newZoneConverter(data, sourceName, outDir, verbose, lib, lightLib, resDir, registry)
	if !ok {
		return 0
	}
	return c.run(zones)
}

// run assembles every zone into the output directory and writes the manifest.
// Progress advances only for zones that produce a GLB, matching the skip
// semantics of the original loop.
func (c *zoneConverter) run(zones []*eqoa.ESFObject) int {
	if err := os.MkdirAll(c.outDir, 0755); err != nil {
		return 0
	}
	manifest := zoneManifest{Source: c.sourceName}

	var zoneBar *progressbar.ProgressBar
	if c.verbose {
		zoneBar = newBar(len(zones), fmt.Sprintf("%-20s", c.sourceName))
	}

	for zoneIdx, zoneObj := range zones {
		entry, ok := c.buildZone(zoneIdx, zoneObj)
		if !ok {
			continue
		}
		manifest.Zones = append(manifest.Zones, *entry)
		if progressStep != nil {
			progressStep()
		}
		if zoneBar != nil {
			zoneBar.Describe(fmt.Sprintf("%-14s zone %-4d", c.sourceName, zoneIdx))
			zoneBar.Add(1)
		}
	}

	if zoneBar != nil {
		zoneBar.Finish()
	}

	manifestPath := filepath.Join(c.outDir, c.prefix+"_zones.json")
	if mf, err := os.Create(manifestPath); err == nil {
		enc := json.NewEncoder(mf)
		enc.SetIndent("", "  ")
		enc.Encode(manifest)
		mf.Close()
		if c.verbose {
			logf("manifest → %s\n", manifestPath)
		}
	}

	return len(manifest.Zones)
}

// buildZone assembles a single Zone (0x3000) object into a GLB (plus a collision
// sidecar) and returns its manifest entry. ok=false means the zone was skipped
// (no ZoneResources, no terrain sprites, or a filesystem error) and nothing was
// written.
func (c *zoneConverter) buildZone(zoneIdx int, zoneObj *eqoa.ESFObject) (*zoneManifestEntry, bool) {
	var zoneRes, zoneActorsObj *eqoa.ESFObject
	for _, child := range zoneObj.Children {
		switch uint16(child.Header.ObjectType) {
		case 0x3100:
			zoneRes = child
		case 0x3290:
			zoneActorsObj = child
		}
	}
	if zoneRes == nil {
		return nil, false
	}

	var sprites []*eqoa.ESFObject
	for _, child := range zoneRes.Children {
		if uint16(child.Header.ObjectType) == 0x2310 {
			sprites = append(sprites, child)
		}
	}
	if len(sprites) == 0 {
		return nil, false
	}

	subBlocks, allPTs := c.parseSubBlocks(zoneObj)

	// Count direct 0x1110 (MaterialPalette) children of ZoneResources — that is
	// the number of terrain sub-blocks that belong to THIS zone.
	numTerrainPalettes := 0
	for _, child := range zoneRes.Children {
		if uint16(child.Header.ObjectType) == 0x1110 {
			numTerrainPalettes++
		}
	}
	subBlocks = c.filterSubBlocks(subBlocks, sprites, numTerrainPalettes)

	// Create the assembler, supply the file-wide surface fallback, and load the
	// zone terrain palette materials.
	za := gltf.NewZoneAssembler()
	za.SetPreTranslations(allPTs)
	za.SetFallbackSurfaces(c.surfPool)
	za.LoadZoneResources(c.r, zoneRes, c.order)

	zb := newZoneBuildState(c, za, zoneIdx, subBlocks, allPTs)

	// Pass 1: stage terrain sprites (geometry deferred until after actors).
	entries := zb.loadTerrainSprites(sprites)

	// Place non-terrain sprites (trees, rocks, props, lights, emitters).
	if zoneActorsObj != nil {
		zb.indexZoneSprites(zoneRes)
		zb.walkActors(zoneActorsObj)
		zb.dumpActorDebug()
	}

	// Terrain last: emit after actors so LOD props appear first in the GLB.
	for _, e := range entries {
		za.AddSpriteMeshes(e.asset, e.spriteName, e.sb.East, e.sb.North, e.sb.HeightRef, e.matStart)
		zb.spriteCount++
	}

	// Merge all accumulated per-material geometry into a single mesh.
	za.FinalizeZoneMesh(fmt.Sprintf("zone-%d", zoneIdx))

	collPos, collTris := zb.collectCollision(zoneObj)

	return c.writeZone(zoneIdx, za, zb.spriteCount, collPos, collTris)
}

// parseSubBlocks reads the zone's ZonePreTranslations (0x3250) under its ZoneBase
// (0x3200) child. Each entry is (East, HeightRef, North) — one per sub-block. The
// count often exceeds this zone's terrain-palette count because neighboring zones
// share the same 0x3250 block; filterSubBlocks narrows it. allPTs is the full,
// unfiltered array (kept for per-vertex VGroup lookup and collision anchoring).
func (c *zoneConverter) parseSubBlocks(zoneObj *eqoa.ESFObject) ([]subBlock, []gltf.PreTranslation) {
	var subBlocks []subBlock
	var allPTs []gltf.PreTranslation
	for _, child := range zoneObj.Children {
		if uint16(child.Header.ObjectType) != 0x3200 {
			continue
		}
		for _, gc := range child.Children {
			if uint16(gc.Header.ObjectType) != 0x3250 {
				continue
			}
			body, err := gc.ReadBody(c.r)
			if err != nil || len(body) < 4 {
				break
			}
			count := int(c.order.Uint32(body[0:4]))
			for i := 0; i < count && 4+i*12+12 <= len(body); i++ {
				e := math.Float32frombits(c.order.Uint32(body[4+i*12:]))
				h := math.Float32frombits(c.order.Uint32(body[4+i*12+4:]))
				n := math.Float32frombits(c.order.Uint32(body[4+i*12+8:]))
				subBlocks = append(subBlocks, subBlock{e, h, n, i})
				allPTs = append(allPTs, gltf.PreTranslation{East: e, HeightRef: h, North: n})
			}
		}
		break
	}
	return subBlocks, allPTs
}

// filterSubBlocks narrows a shared 0x3250 array to the sub-blocks that belong to
// THIS zone. It prefers an AABB spatial filter built from the sprite bbox centres
// (always within the current zone), padded by 500 units, and tie-breaks by
// distance to the sprite-AABB centre. When no sprite AABB is available it falls
// back to centroid proximity. Returns the input unchanged when there is nothing
// to narrow (palette count unknown or already ≤ the block count).
func (c *zoneConverter) filterSubBlocks(subBlocks []subBlock, sprites []*eqoa.ESFObject, numTerrainPalettes int) []subBlock {
	if numTerrainPalettes <= 0 || len(subBlocks) <= numTerrainPalettes {
		return subBlocks
	}

	// Pre-scan sprite bboxes to establish the zone's true spatial extent. The
	// 0x3250 table bleeds across zone boundaries (~2000 units), so the union AABB
	// of sprite bbox centres is an unbiased footprint.
	var (
		sprMinE, sprMaxE float32 = math.MaxFloat32, -math.MaxFloat32
		sprMinN, sprMaxN float32 = math.MaxFloat32, -math.MaxFloat32
		hasSprAABB       bool
	)
	for _, sprObj := range sprites {
		c1, c2, ok := gltf.ParseSpriteBBox(c.r, sprObj, c.order)
		if !ok {
			continue
		}
		if c2[0]-c1[0] > 3000 || c2[2]-c1[2] > 3000 {
			continue // skip collision-hull / implausibly large bbox
		}
		ce := (c1[0] + c2[0]) / 2
		cn := (c1[2] + c2[2]) / 2
		if ce < sprMinE {
			sprMinE = ce
		}
		if ce > sprMaxE {
			sprMaxE = ce
		}
		if cn < sprMinN {
			sprMinN = cn
		}
		if cn > sprMaxN {
			sprMaxN = cn
		}
		hasSprAABB = true
	}

	if hasSprAABB {
		const pad = float32(500)
		inZone := make([]subBlock, 0, numTerrainPalettes)
		for _, sb := range subBlocks {
			if sb.East >= sprMinE-pad && sb.East <= sprMaxE+pad &&
				sb.North >= sprMinN-pad && sb.North <= sprMaxN+pad {
				inZone = append(inZone, sb)
			}
		}
		// Tie-break: if the AABB filter still returns more than the palette count,
		// keep the N nearest to the sprite-AABB centre.
		if len(inZone) > numTerrainPalettes {
			cx := (sprMinE + sprMaxE) / 2
			cn := (sprMinN + sprMaxN) / 2
			sort.Slice(inZone, func(i, j int) bool {
				di := (inZone[i].East-cx)*(inZone[i].East-cx) + (inZone[i].North-cn)*(inZone[i].North-cn)
				dj := (inZone[j].East-cx)*(inZone[j].East-cx) + (inZone[j].North-cn)*(inZone[j].North-cn)
				return di < dj
			})
			inZone = inZone[:numTerrainPalettes]
		}
		if len(inZone) > 0 {
			for i := range inZone {
				inZone[i].Idx = i
			}
			return inZone
		}
		return subBlocks
	}

	// Centroid fallback (less accurate when 0x3250 bleeds across zones).
	var centEast, centNorth float32
	for _, sb := range subBlocks {
		centEast += sb.East
		centNorth += sb.North
	}
	centEast /= float32(len(subBlocks))
	centNorth /= float32(len(subBlocks))

	inZone := make([]subBlock, 0, numTerrainPalettes)
	used := make([]bool, len(subBlocks))
	for k := 0; k < numTerrainPalettes; k++ {
		bestDist := float32(math.MaxFloat32)
		bestIdx := -1
		for j, sb := range subBlocks {
			if used[j] {
				continue
			}
			de := sb.East - centEast
			dn := sb.North - centNorth
			d := de*de + dn*dn
			if d < bestDist {
				bestDist = d
				bestIdx = j
			}
		}
		if bestIdx >= 0 {
			sb := subBlocks[bestIdx]
			sb.Idx = k
			inZone = append(inZone, sb)
			used[bestIdx] = true
		}
	}
	return inZone
}

// zoneBuildState holds the mutable accumulators for assembling one zone: the
// glTF assembler, the selected sub-blocks and full pre-translation array, the
// per-instance sprite collision, the particle-texture cache, plus the
// actor-resolution caches and debug counters used while walking ZoneActors.
type zoneBuildState struct {
	c       *zoneConverter
	za      *gltf.ZoneAssembler
	zoneIdx int

	subBlocks []subBlock
	allPTs    []gltf.PreTranslation

	spriteCount int

	// World-space collision accumulated from placed sprite instances (their own
	// 0x4200 buffers transformed by the actor matrix). The zone-tree collision
	// walk skips these sprite subtrees, so this is the sole source of their
	// collision — emitted per instance.
	spriteCollPos  [][3]float32
	spriteCollTris []uint32

	// Cache of embedded particle-sprite textures (keyed by texture dictID) so a
	// shared effect texture is embedded once, not once per emitter instance.
	particleTex map[uint32]int

	// Actor-resolution caches (populated as ZoneActors are walked).
	spriteByID          map[uint32]*eqoa.ESFObject
	spriteMatStart      map[uint32]int
	resObjCache         map[uint32]*eqoa.ESFObject
	placeholderAsset    *eqoa.Asset
	placeholderMatStart int
	placeholderLoaded   bool

	// DEBUG (ZONE_ACTOR_DEBUG): actor-resolution instrumentation.
	actTotal, actLocal, actLib, actDir int
	actLight, actEmpty, actSkip        int
	actPlaceholder, actParticle        int
	unresolved                         map[uint32]int
}

func newZoneBuildState(c *zoneConverter, za *gltf.ZoneAssembler, zoneIdx int, subBlocks []subBlock, allPTs []gltf.PreTranslation) *zoneBuildState {
	return &zoneBuildState{
		c: c, za: za, zoneIdx: zoneIdx,
		subBlocks:      subBlocks,
		allPTs:         allPTs,
		particleTex:    map[uint32]int{},
		spriteByID:     map[uint32]*eqoa.ESFObject{},
		spriteMatStart: map[uint32]int{},
		resObjCache:    map[uint32]*eqoa.ESFObject{},
		unresolved:     map[uint32]int{},
	}
}

// nearestSubBlock returns the nearest in-zone sub-block and its squared distance
// from (ce, cn). Only in-zone sub-blocks are considered.
func (zb *zoneBuildState) nearestSubBlock(ce, cn float32) (subBlock, float32) {
	bestDist := float32(math.MaxFloat32)
	best := zb.subBlocks[0]
	for _, sb := range zb.subBlocks {
		de := ce - sb.East
		dn := cn - sb.North
		d := de*de + dn*dn
		if d < bestDist {
			bestDist = d
			best = sb
		}
	}
	return best, bestDist
}

// loadTerrainSprites stages the zone's terrain SubSprites (deduped by offset):
// each is bbox-filtered, matched to its nearest sub-block, loaded, and assigned a
// base material index (its own 0x1101 array, or the zone palette). Geometry is
// emitted later so actors precede terrain in the GLB.
func (zb *zoneBuildState) loadTerrainSprites(sprites []*eqoa.ESFObject) []spriteEntry {
	var entries []spriteEntry
	processed := make(map[int64]bool)

	for _, spriteObj := range sprites {
		if processed[spriteObj.Offset] {
			continue
		}
		processed[spriteObj.Offset] = true

		if len(zb.subBlocks) == 0 {
			continue
		}
		c1, c2, ok := gltf.ParseSpriteBBox(zb.c.r, spriteObj, zb.c.order)
		if !ok {
			continue
		}
		// Skip implausibly large bbox (pure collision hulls > 3000 units).
		if c2[0]-c1[0] > 3000 || c2[2]-c1[2] > 3000 {
			continue
		}
		ce := (c1[0] + c2[0]) / 2
		cn := (c1[2] + c2[2]) / 2
		sb, sbDistSq := zb.nearestSubBlock(ce, cn)
		// Skip sprites with bad/offset bbox data (> 1500 units from any in-zone
		// sub-block); these would land far outside the zone's world bounds.
		if sbDistSq > 1500*1500 {
			continue
		}

		asset, err := eqoa.LoadAsset(zb.c.r, spriteObj, zb.c.order)
		if err != nil || len(asset.Meshes) == 0 {
			continue
		}

		// Non-terrain sprites embed their own 0x1101 material array; terrain tiles
		// use the zone palette.
		matStart := zb.za.LoadSpriteMaterials(zb.c.r, spriteObj, zb.c.order)
		if matStart < 0 {
			matStart = zb.za.PaletteStart(sb.Idx)
		}

		entries = append(entries, spriteEntry{asset, fmt.Sprintf("Sprite_0x%X", asset.ID), sb, matStart})
	}
	return entries
}

// indexZoneSprites indexes non-terrain sprites from ZoneResources by dictID, the
// fallback for actors whose geometry lives inside the zone file itself. 0x2A10
// (LOD reference stub) and 0x2310 (terrain sub-sprite) are excluded.
func (zb *zoneBuildState) indexZoneSprites(zoneRes *eqoa.ESFObject) {
	var indexSprites func(o *eqoa.ESFObject)
	indexSprites = func(o *eqoa.ESFObject) {
		tc := uint16(o.Header.ObjectType)
		if eqoa.IsSprite(tc) && tc != 0x2310 && tc != 0x2A10 && o.DictID != 0 {
			zb.spriteByID[o.DictID] = o
		}
		for _, c := range o.Children {
			indexSprites(c)
		}
	}
	indexSprites(zoneRes)
}

// walkActors recurses the ZoneActors tree (0x3290→0x3270→0x6040→0x6000) and
// places each 0x6000 ZoneActor.
func (zb *zoneBuildState) walkActors(o *eqoa.ESFObject) {
	if uint16(o.Header.ObjectType) == 0x6000 {
		zb.placeActor(o)
		return
	}
	for _, c := range o.Children {
		zb.walkActors(c)
	}
}

// placeActor decodes one 0x6000 ZoneActor and emits its contribution: a point
// light, a resolved sprite (static baked or animated subtree), a particle
// emitter, or a spawn placeholder when the referenced geometry is a cross-file
// reference the local scan can't resolve.
func (zb *zoneBuildState) placeActor(o *eqoa.ESFObject) {
	c := zb.c
	body, err := o.ReadBody(c.r)
	if err != nil || len(body) < 36 {
		return
	}
	pos := [3]float32{
		math.Float32frombits(c.order.Uint32(body[4:8])),
		math.Float32frombits(c.order.Uint32(body[8:12])),
		math.Float32frombits(c.order.Uint32(body[12:16])),
	}
	// Euler rotation (3 angles): [0] yaw about the vertical (Height) axis; [1]/[2]
	// pitch/roll tilts (props that follow terrain slope — bridges/fences/hedges).
	// body[28:32] is scale (the 7th float), NOT a sprite id — the sprite is the
	// ZoneActor object's DictID. Confirmed via ParseZoneActor (Ghidra FUN_0040ff78):
	// id·pos[3]·rot[3]·scale·color[4].
	rot := [3]float32{
		math.Float32frombits(c.order.Uint32(body[16:20])),
		math.Float32frombits(c.order.Uint32(body[20:24])),
		math.Float32frombits(c.order.Uint32(body[24:28])),
	}
	scale := math.Float32frombits(c.order.Uint32(body[28:32]))
	if scale <= 0 || scale > 100 {
		scale = 1.0
	}

	// Point light: emit a KHR_lights_punctual light at the actor position.
	if ld, ok := c.lightLib[o.DictID]; ok {
		zb.actTotal++
		zb.actLight++
		zb.za.AddPointLight(fmt.Sprintf("Light_0x%08X", o.DictID), pos, ld.color, ld.radius)
		zb.spriteCount++
		return
	}

	zb.actTotal++
	sprObj, sprR, sprOrder, viaLib, viaDir := zb.resolveSprite(o.DictID)
	if sprObj == nil {
		zb.placeUnresolved(o.DictID, pos, rot, scale)
		return
	}

	matStart, cached := zb.spriteMatStart[o.DictID]
	if !cached {
		matStart = zb.za.LoadSpriteMaterials(sprR, sprObj, sprOrder)
		if matStart < 0 {
			matStart = 0
		}
		zb.spriteMatStart[o.DictID] = matStart
	}

	asset, err := eqoa.LoadAsset(sprR, sprObj, sprOrder)
	if err != nil || len(asset.Meshes) == 0 {
		// Particle/effect emitters (0xC100) have no mesh — the client spawns
		// particles at runtime. Export them as tagged emitter markers.
		if uint16(sprObj.Header.ObjectType) == 0xC100 {
			if placeParticleEmitter(zb.za, sprR, sprObj, sprOrder, o.DictID, pos, zb.particleTex) {
				zb.actParticle++
				zb.spriteCount++
				return
			}
		}
		zb.actEmpty++
		return
	}
	switch {
	case viaDir:
		zb.actDir++
	case viaLib:
		zb.actLib++
	default:
		zb.actLocal++
	}
	// Animated/hierarchical props (clockwork, banners, …) are emitted as their
	// own skinned+animated subtree so their animation survives; static props are
	// baked into the flat zone mesh. Fall back to baking if the subtree fails.
	if len(asset.Actions) > 0 {
		if err := zb.za.AddAnimatedSpriteNode(sprR, asset, sprOrder, c.registry, pos, rot, scale); err != nil {
			zb.za.AddSpriteAtWorldPos(asset, pos, rot, scale, matStart)
		}
	} else {
		zb.za.AddSpriteAtWorldPos(asset, pos, rot, scale, matStart)
	}
	// Every placed sprite carries its collision in its own local frame; the
	// zone-tree walk skips placeable sprite subtrees, so emit a per-instance copy
	// transformed by this actor's world placement.
	if collisionExport {
		zb.accumSpriteCollision(sprR, sprObj, sprOrder, pos, rot, scale)
	}
	zb.placeNestedEmitters(sprObj, sprR, sprOrder, pos, rot, scale)
	zb.spriteCount++
}

// resolveSprite locates the geometry a ZoneActor DictID refers to, preferring the
// zone's own ZoneResources, then the companion sprite library, then the
// cross-file resource directory (0x9000). It returns the source object, a reader
// and byte order for it, and which path resolved it (for debug counters). A nil
// object means unresolved (a cross-file mob/character/item reference).
func (zb *zoneBuildState) resolveSprite(dictID uint32) (sprObj *eqoa.ESFObject, sprR io.ReadSeeker, sprOrder binary.ByteOrder, viaLib, viaDir bool) {
	c := zb.c
	sprR = c.r
	sprOrder = c.order
	if spr, ok := zb.spriteByID[dictID]; ok {
		sprObj = spr
		return
	}
	if c.lib != nil {
		if entry, ok := c.lib[dictID]; ok {
			sprObj = entry.obj
			sprR = entry.r
			sprOrder = entry.order
			viaLib = true
			return
		}
	}
	// Cross-file resource directory: the client streams static props
	// (buildings/civil arch) and shared creatures/items in via a DictID→offset
	// table the local sprite scan doesn't cover.
	if c.resDir == nil {
		return
	}
	entry, ok := c.resDir[dictID]
	if !ok {
		return
	}
	obj, cached := zb.resObjCache[dictID]
	if !cached {
		// Materialize the file's decompressed stream lazily — only files actually
		// referenced here are decompressed.
		if r, rerr := entry.src.reader(); rerr == nil {
			if parsed, perr := eqoa.ReadObjectAt(r, entry.order, int64(entry.ref.Offset)); perr == nil {
				// Integrity check: the directory records the full target object size
				// (12-byte header + body), so a correctly-aligned offset must satisfy
				// ObjectSize+12 == ref.Size. A mismatch means a bad offset — skip
				// rather than feed garbage to LoadAsset.
				if entry.ref.Size == 0 || uint32(parsed.Header.ObjectSize)+uint32(eqoa.ObjectHeaderSize) == entry.ref.Size {
					obj = parsed
				}
			}
		}
		zb.resObjCache[dictID] = obj
	}
	if obj != nil {
		// A fresh reader over the (now-cached) source bytes for the subsequent
		// LoadSpriteMaterials/LoadAsset reads.
		if r, rerr := entry.src.reader(); rerr == nil {
			sprObj = obj
			sprR = r
			sprOrder = entry.order
			viaDir = true
		}
	}
	return
}

// placeUnresolved flags an actor whose geometry could not be resolved (a spawn
// point — mob/NPC/character) with a placeholder so the spawn location is visible:
// the built-in marker by default, or a real game sprite when ZONE_PLACEHOLDER
// resolves. Does nothing when spawn marking is disabled.
func (zb *zoneBuildState) placeUnresolved(dictID uint32, pos, rot [3]float32, scale float32) {
	c := zb.c
	zb.actSkip++
	zb.unresolved[dictID]++
	if !c.useMarker && c.placeholderEntry == nil {
		return
	}
	if !zb.placeholderLoaded {
		zb.placeholderLoaded = true
		if c.placeholderEntry != nil {
			zb.placeholderMatStart = zb.za.LoadSpriteMaterials(c.placeholderEntry.r, c.placeholderEntry.obj, c.placeholderEntry.order)
			if zb.placeholderMatStart < 0 {
				zb.placeholderMatStart = 0
			}
			if a, e := eqoa.LoadAsset(c.placeholderEntry.r, c.placeholderEntry.obj, c.placeholderEntry.order); e == nil && len(a.Meshes) > 0 {
				zb.placeholderAsset = a
			}
		} else {
			// Built-in designed marker (embedded .glb) — a bright downward
			// "location pin" pointing at the spawn.
			a, col := spawnMarkerAsset()
			zb.placeholderMatStart = zb.za.AddUnlitMaterial("SpawnMarker", col)
			zb.placeholderAsset = a
		}
	}
	if zb.placeholderAsset != nil {
		zb.za.AddSpriteAtWorldPos(zb.placeholderAsset, pos, rot, scale*c.placeholderScale, zb.placeholderMatStart)
		zb.spriteCount++
		zb.actPlaceholder++
	}
}

// accumSpriteCollision appends one placed sprite instance's world-space collision
// (transformed by the actor placement) to the zone's sprite-collision buffers.
func (zb *zoneBuildState) accumSpriteCollision(sprR io.ReadSeeker, sprObj *eqoa.ESFObject, sprOrder binary.ByteOrder, pos, rot [3]float32, scale float32) {
	cp, ct := collectSpriteCollision(sprR, sprObj, sprOrder, pos, rot, scale)
	if len(cp) == 0 {
		return
	}
	offs := uint32(len(zb.spriteCollPos))
	zb.spriteCollPos = append(zb.spriteCollPos, cp...)
	for _, idx := range ct {
		zb.spriteCollTris = append(zb.spriteCollTris, offs+idx)
	}
}

// placeNestedEmitters places particle emitters (0xC100) nested under a placed
// sprite whose own geometry was already emitted (e.g. a wall-torch mesh with a
// child fire). A GroupSprite (0x2C00) positions its members via a 0x2C30
// transform array, so each emitter is placed at its member's local offset lifted
// into world space; other nestings fall back to the actor position.
func (zb *zoneBuildState) placeNestedEmitters(sprObj *eqoa.ESFObject, sprR io.ReadSeeker, sprOrder binary.ByteOrder, pos, rot [3]float32, scale float32) {
	if uint16(sprObj.Header.ObjectType) == 0x2C00 {
		members := eqoa.ParseGroupMembers(sprObj, sprR, sprOrder)
		arr := eqoa.GroupSpriteArray(sprObj)
		if arr == nil {
			return
		}
		for i, child := range arr.Children {
			if uint16(child.Header.ObjectType) != 0xC100 {
				continue
			}
			wp := pos
			if i < len(members) {
				wp = actorWorldPoint(members[i].Pos, pos, rot, scale)
			}
			if placeParticleEmitter(zb.za, sprR, child, sprOrder, child.DictID, wp, zb.particleTex) {
				zb.actParticle++
			}
		}
		return
	}
	var nestScan func(o *eqoa.ESFObject)
	nestScan = func(o *eqoa.ESFObject) {
		if uint16(o.Header.ObjectType) == 0xC100 {
			if placeParticleEmitter(zb.za, sprR, o, sprOrder, o.DictID, pos, zb.particleTex) {
				zb.actParticle++
			}
			return
		}
		for _, c := range o.Children {
			nestScan(c)
		}
	}
	for _, c := range sprObj.Children {
		nestScan(c)
	}
}

// dumpActorDebug prints the per-zone actor-resolution breakdown when
// ZONE_ACTOR_DEBUG is set and any actors were seen.
func (zb *zoneBuildState) dumpActorDebug() {
	if os.Getenv("ZONE_ACTOR_DEBUG") == "" || zb.actTotal == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "[%s zone %d] actors=%d placed(local=%d lib=%d dir=%d) lights=%d particles=%d empty=%d skip=%d placeholder=%d\n",
		zb.c.sourceName, zb.zoneIdx, zb.actTotal, zb.actLocal, zb.actLib, zb.actDir, zb.actLight, zb.actParticle, zb.actEmpty, zb.actSkip, zb.actPlaceholder)
	type mc struct {
		id uint32
		n  int
	}
	var top []mc
	for id, n := range zb.unresolved {
		top = append(top, mc{id, n})
	}
	sort.Slice(top, func(i, j int) bool { return top[i].n > top[j].n })
	for i := 0; i < len(top) && i < 8; i++ {
		fmt.Fprintf(os.Stderr, "    unresolved 0x%08X x%d\n", top[i].id, top[i].n)
	}
}

// collectCollision gathers this zone's collision geometry (0x4200 CollBuffer) for
// the sidecar GLB: the zone-tree buffers (type-2 vertices anchored to the
// ZonePreTranslations pool) plus the per-instance sprite collision accumulated
// during actor placement. Returns nil when collision export is disabled.
func (zb *zoneBuildState) collectCollision(zoneObj *eqoa.ESFObject) ([][3]float32, []uint32) {
	if !collisionExport {
		return nil, nil
	}
	// Type-2 CollBuffer vertices are anchored to the zone's ZonePreTranslations
	// (0x3250) pool by a per-vertex index; pass the full unfiltered array so they
	// land on the rendered geometry.
	collBase := make([][3]float32, len(zb.allPTs))
	for i, pt := range zb.allPTs {
		collBase[i] = [3]float32{pt.East, pt.HeightRef, pt.North}
	}
	pos, tris, nColl := collectZoneCollision(zb.c.r, zoneObj, zb.c.order, collBase)
	// Append the per-instance animated-sprite collision (already world space).
	if len(zb.spriteCollPos) > 0 {
		offs := uint32(len(pos))
		pos = append(pos, zb.spriteCollPos...)
		for _, idx := range zb.spriteCollTris {
			tris = append(tris, offs+idx)
		}
	}
	if zb.c.verbose && len(tris) >= 3 {
		logf("  zone %d: collision %d verts / %d tris (%d zone CollBuffers + %d sprite-instance verts)\n",
			zb.zoneIdx, len(pos), len(tris)/3, nColl, len(zb.spriteCollPos))
	}
	return pos, tris
}

// writeZone writes the assembled zone GLB into a zones/ subdirectory (named by
// map-tile area name when available), writes the collision sidecar when there is
// collision geometry, and returns the manifest entry. ok=false on a filesystem
// error (the zone is skipped without advancing progress).
func (c *zoneConverter) writeZone(zoneIdx int, za *gltf.ZoneAssembler, spriteCount int, collPos [][3]float32, collTris []uint32) (*zoneManifestEntry, bool) {
	zonesDir := filepath.Join(c.outDir, "zones")
	if err := os.MkdirAll(zonesDir, 0755); err != nil {
		return nil, false
	}

	// Name the zone from the map tile data when available (zone_87_Qeynos.glb).
	areaName := ""
	if za.HasPos() {
		areaName = zoneTileName(c.prefix, za.MinPos, za.MaxPos)
	}
	glbName := fmt.Sprintf("zone_%d.glb", zoneIdx)
	if areaName != "" {
		glbName = fmt.Sprintf("zone_%d_%s.glb", zoneIdx, sanitizeZoneName(areaName))
	}
	outPath := filepath.Join(zonesDir, glbName)
	outF, err := os.Create(outPath)
	if err != nil {
		return nil, false
	}
	za.Builder().WriteGLB(outF)
	outF.Close()

	// Collision sidecar: a separate GLB holding just the tagged collision mesh,
	// so the visual GLB stays clean while the data is preserved.
	collName := ""
	if len(collTris) >= 3 {
		cb := gltf.NewBuilder()
		cb.AddCollisionNode("collision", collPos, collTris)
		collName = strings.TrimSuffix(glbName, ".glb") + "_collision.glb"
		if cf, cerr := os.Create(filepath.Join(zonesDir, collName)); cerr == nil {
			cb.WriteGLB(cf)
			cf.Close()
		} else {
			collName = ""
		}
	}

	entry := zoneManifestEntry{
		Index:       zoneIdx,
		GLB:         filepath.Join("zones", glbName),
		Name:        areaName,
		SpriteCount: spriteCount,
	}
	if collName != "" {
		entry.Collision = filepath.Join("zones", collName)
	}
	if za.HasPos() {
		entry.MinPos = za.MinPos
		entry.MaxPos = za.MaxPos
	}
	return &entry, true
}

// collectZoneCollision walks a Zone (0x3000) subtree, decodes every 0x4200
// CollBuffer under it, and merges them into a single world-space position array
// plus a flat triangle-list index buffer. Returns the merged positions, indices,
// and the number of CollBuffers successfully decoded.
//
// baseVerts is the zone's ZonePreTranslations (0x3250) array in (East, Height,
// North) world space. Type-2 CollBuffer vertices are stored as a small i16 delta
// plus an index k into this array (client: base = *(VIZone+0x78)[k], the same
// pool ParseZonePreTranslations fills and ParseCollBuffer reads — verified in
// ParseCollBuffer__10VIESFParse). Passing it aligns type-2 collision onto the
// rendered geometry; passing nil leaves those vertices anchored at the origin.
func collectZoneCollision(r io.ReadSeeker, zoneObj *eqoa.ESFObject, order binary.ByteOrder, baseVerts [][3]float32) ([][3]float32, []uint32, int) {
	var positions [][3]float32
	var indices []uint32
	n := 0

	var walk func(o *eqoa.ESFObject)
	walk = func(o *eqoa.ESFObject) {
		t := uint16(o.Header.ObjectType)
		// Placeable sprite subtrees (SimpleSprite/HSprite/CSprite and their
		// animated variants) are positioned per actor-instance with a world
		// transform; their collision is emitted from the actor path
		// (collectSpriteCollision) so it lands on the placed visual. Skip them
		// here — otherwise their sprite-local collision is dumped at raw
		// definition coordinates (props piling at the origin, gears floating).
		// Terrain SubSprites (0x2310) are NOT placeable: they sit directly under
		// the zone and carry zone-anchored (type-2) collision, so they stay.
		switch t {
		case 0x2000, 0x2200, 0x2400, 0x2600, 0x2700, 0x2C00:
			return
		}
		if t == 0x4200 {
			body, err := o.ReadBody(r)
			if err == nil {
				cb, err := eqoa.ParseCollBuffer(body, order, int(o.Header.ObjectVersion), baseVerts)
				if err == nil && len(cb.Positions) > 0 {
					base := uint32(len(positions))
					// CollBuffer positions are EQOA world space (x=East,
					// y=Height, z=North) — type-0/1 verts are absolute, type-2
					// verts already have their ZonePreTranslations anchor folded
					// in by ParseCollBuffer (see baseVerts). Emit directly as GLB
					// (X=East, Y=Height, Z=North), the same Y-up frame the visual
					// terrain uses (see ZoneAssembler.AddSpriteMeshes), so the
					// collision node aligns with the rendered geometry.
					for _, p := range cb.Positions {
						positions = append(positions, [3]float32{p[0], p[1], p[2]})
					}
					for _, idx := range cb.Triangles {
						indices = append(indices, base+idx)
					}
					n++
				}
			}
			return
		}
		for _, c := range o.Children {
			walk(c)
		}
	}
	walk(zoneObj)
	return positions, indices, n
}

// actorWorldPoint transforms a sprite-local point into world space by an actor's
// placement — world = R(rot)·(scale·local) + pos — the same convention used to
// place sprite meshes and collision. Used to lift a GroupSprite member's local
// offset (e.g. a torch flame position) to its world location.
func actorWorldPoint(local [3]float32, pos, rot [3]float32, scale float32) [3]float32 {
	m := gltf.EulerRotMatrix(rot)
	e, h, n := local[0]*scale, local[1]*scale, local[2]*scale
	return [3]float32{
		m[0]*e + m[1]*h + m[2]*n + pos[0],
		m[3]*e + m[4]*h + m[5]*n + pos[1],
		m[6]*e + m[7]*h + m[8]*n + pos[2],
	}
}

// placeParticleEmitter decodes a placed 0xC100 ParticleSprite and adds a tagged
// emitter marker at its world position. Returns false if the object isn't a
// decodable particle emitter (so the caller can fall back to counting it empty).
func placeParticleEmitter(za *gltf.ZoneAssembler, r io.ReadSeeker, obj *eqoa.ESFObject, order binary.ByteOrder, dictID uint32, pos [3]float32, texCache map[uint32]int) bool {
	ps, err := eqoa.ParseParticleSprite(r, obj, order)
	if err != nil || ps.Def == nil || len(ps.Def.Motifs) == 0 {
		return false
	}
	// Nested (inline) emitters have no top-level dictID; identify them by their
	// definition reference, then its texture, so they aren't all "0x00000000".
	if dictID == 0 {
		if ps.DefRef != 0 {
			dictID = ps.DefRef
		} else {
			dictID = ps.Def.TextureDictID
		}
	}
	// Embed the particle sprite texture once per unique texture and reference it
	// from extras, so a runtime has the sprite image, not just its dictID.
	texIdx := -1
	if ps.Def.Texture != nil && texCache != nil {
		if idx, ok := texCache[ps.Def.TextureDictID]; ok {
			texIdx = idx
		} else if img, ierr := ps.Def.Texture.ToImage(0); ierr == nil {
			texIdx = za.Builder().AddImageTexture(img)
			texCache[ps.Def.TextureDictID] = texIdx
		}
	}
	m := ps.Def.Motifs[0]
	clamp01 := func(f float32) float32 {
		if f < 0 {
			return 0
		}
		if f > 1 {
			return 1
		}
		return f
	}
	rgb := [3]float32{clamp01(m.Gradient[0][0]), clamp01(m.Gradient[0][1]), clamp01(m.Gradient[0][2])}
	size := m.StartSize
	if size < 0.3 {
		size = 0.3
	}
	if size > 1.5 {
		size = 1.5
	}
	fields := map[string]any{
		"effect":  "particle",
		"dict_id": fmt.Sprintf("0x%08X", dictID),
		"sprite":  ps,
	}
	if texIdx >= 0 {
		fields["texture"] = texIdx // glTF texture index of the particle sprite
	}
	extras, err := json.Marshal(fields)
	if err != nil {
		return false
	}
	za.AddParticleEmitter(fmt.Sprintf("Emitter_0x%08X", dictID), pos, rgb, size, extras)
	return true
}

// collectSpriteCollision decodes the 0x4200 CollBuffers nested under a single
// placed sprite definition and returns them transformed into world space by the
// actor's placement — world = R·(scale·v) + pos, the same transform
// AddAnimatedSpriteNode/AddSpriteAtWorldPos apply to the visual mesh. baseVerts
// is nil: a sprite's own collision is absolute in its local frame (type-0/1), not
// anchored to the zone's ZonePreTranslations pool. Indices are offset by base so
// the returned buffers can be concatenated with other collision geometry.
func collectSpriteCollision(r io.ReadSeeker, sprObj *eqoa.ESFObject, order binary.ByteOrder, pos, rot [3]float32, scale float32) ([][3]float32, []uint32) {
	m := gltf.EulerRotMatrix(rot)
	var positions [][3]float32
	var indices []uint32
	var walk func(o *eqoa.ESFObject)
	walk = func(o *eqoa.ESFObject) {
		if uint16(o.Header.ObjectType) == 0x4200 {
			body, err := o.ReadBody(r)
			if err == nil {
				cb, err := eqoa.ParseCollBuffer(body, order, int(o.Header.ObjectVersion), nil)
				if err == nil && len(cb.Positions) > 0 {
					base := uint32(len(positions))
					for _, p := range cb.Positions {
						e, h, n := p[0]*scale, p[1]*scale, p[2]*scale
						positions = append(positions, [3]float32{
							m[0]*e + m[1]*h + m[2]*n + pos[0],
							m[3]*e + m[4]*h + m[5]*n + pos[1],
							m[6]*e + m[7]*h + m[8]*n + pos[2],
						})
					}
					for _, idx := range cb.Triangles {
						indices = append(indices, base+idx)
					}
				}
			}
			return
		}
		for _, c := range o.Children {
			walk(c)
		}
	}
	walk(sprObj)
	return positions, indices
}

func init() {
	convertZoneCmd.Flags().StringVarP(&outputDir, "output", "o", "", "Output directory for assembled zone GLBs (default: current directory)")
	convertZoneCmd.Flags().BoolVar(&collisionExport, "collision", true, "export zone collision geometry (0x4200 CollBuffer) as a tagged 'collision' node (on by default; --collision=false to omit)")
	devCmd.AddCommand(convertZoneCmd)
}
