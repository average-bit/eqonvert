package cmd

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/average-bit/eqonvert/pkg/eqoa"
	"github.com/average-bit/eqonvert/pkg/gltf"
	"github.com/spf13/cobra"
)

var outputDir string

// markSpawns places a built-in marker at every unresolved zone spawn actor;
// spawnScale multiplies its size. See cmd/spawn_marker.go and convert_zone.go.
var (
	markSpawns bool
	spawnScale float64
)

// collisionExport emits each zone's decoded 0x4200 CollBuffer geometry as a
// separate "collision" glTF node (tagged extras {"collision":true}). ON by
// default — collision is core game data (the engine's floor/wall pathing reads
// it), so preservation exports should keep it; downstream apps filter it via the
// tag. Disable with --collision=false. See convert_zone.go.
var collisionExport bool

// forceExport overrides the source-aware output-dir guard (see guardOutputDir).
var forceExport bool

// manifestName is the marker file eqonvert writes into an output directory to
// record which input it was populated from.
const manifestName = ".eqonvert-manifest.json"

// exportManifest records which input populated an output directory, so a later
// run can refuse to mix a different source into the same folder.
type exportManifest struct {
	Source     string `json:"source"`      // absolute, cleaned input path
	SourceName string `json:"source_name"` // basename, for messages
	CreatedAt  string `json:"created_at"`  // RFC3339
	Tool       string `json:"tool"`
}

// sourceIdentity returns the absolute (cleaned) path used to compare export
// sources, plus its basename for user-facing messages.
func sourceIdentity(p string) (abs, base string) {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = filepath.Clean(p)
	}
	return abs, filepath.Base(abs)
}

// readManifest loads an output dir's export manifest, if present and valid.
func readManifest(dir string) (*exportManifest, bool) {
	b, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		return nil, false
	}
	var m exportManifest
	if json.Unmarshal(b, &m) != nil {
		return nil, false
	}
	return &m, true
}

// countFiles counts regular files under dir (excluding the manifest itself),
// for the "N files" figure in guard messages.
func countFiles(dir string) int {
	n := 0
	filepath.Walk(dir, func(_ string, f os.FileInfo, err error) error {
		if err == nil && !f.IsDir() && f.Name() != manifestName {
			n++
		}
		return nil
	})
	return n
}

// guardOutputDir enforces the source-aware export protection BEFORE any work:
// it refuses to write into a directory that already holds an export from a
// different input (or a non-empty dir with no manifest, when -o is explicit),
// unless --force is set. It does not write anything — call stampOutputDir on
// success so an aborted run never falsely marks a folder.
func guardOutputDir(inputPath string) error {
	outDir, explicit := outputDir, outputDir != ""
	if outDir == "" {
		outDir = "."
	}
	srcAbs, srcBase := sourceIdentity(inputPath)

	if m, ok := readManifest(outDir); ok {
		if m.Source != srcAbs && !forceExport {
			return fmt.Errorf("output dir %q already holds an export of %q (%s, %d files); "+
				"refusing to mix %q in — choose a new -o dir, or pass --force",
				outDir, m.SourceName, m.CreatedAt, countFiles(outDir), srcBase)
		}
		return nil // same source, or forced: leave the existing manifest intact
	}
	if explicit && !forceExport {
		if n := countFiles(outDir); n > 0 {
			return fmt.Errorf("output dir %q is not empty (%d files) and has no eqonvert "+
				"manifest; use an empty dir, or pass --force to write here", outDir, n)
		}
	}
	return nil
}

// stampOutputDir writes the export manifest after a successful run. It never
// overwrites an existing manifest (preserving the folder's original identity,
// including when a different source was merged in via --force).
func stampOutputDir(inputPath string) {
	outDir := outputDir
	if outDir == "" {
		outDir = "."
	}
	if _, ok := readManifest(outDir); ok {
		return
	}
	srcAbs, srcBase := sourceIdentity(inputPath)
	m := exportManifest{srcAbs, srcBase, time.Now().Format(time.RFC3339), "eqonvert"}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(outDir, manifestName), b, 0644)
}

// progressStep, when non-nil, is called once per unit of intra-file work — each
// sprite exported and each zone assembled. Single-file conversion of a large ESF
// (e.g. a zone like TUNARIA) sets it so a spinner advances instead of the CLI
// appearing hung; directory/ISO modes leave it nil, since their per-file bar
// already moves.
var progressStep func()

var convertCmd = &cobra.Command{
	Use:   "convert <path>",
	Short: "Convert an EQOA asset file, directory, or disc image to GLB",
	Long: `Convert EQOA assets to glTF binary (.glb).

Accepts:
  - A single .csf or .esf file
  - A directory (walks for .csf/.esf files)
  - A disc image (.iso) — scans the disc filesystem for all asset files

Audio (FLAC) and video (FMV) output use ffmpeg + openmpt123 when present; they
are optional — models convert fine without them. Install both with:
brew install ffmpeg libopenmpt`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) (err error) {
		warnMissingTools()
		path := args[0]
		info, err := os.Stat(path)
		if err != nil {
			return err
		}

		// Source-aware guard: refuse to mix a different input into a folder that
		// already holds an export (see guardOutputDir). Runs before any work.
		if err = guardOutputDir(path); err != nil {
			return err
		}
		// Stamp the manifest only on an error-free return, so an aborted or hung
		// run never falsely marks the folder's source.
		defer func() {
			if err == nil {
				stampOutputDir(path)
			}
		}()

		if info.IsDir() {
			// Pass 1: collect asset files and build cross-file surface registry.
			registry := eqoa.NewSurfaceRegistry()
			var assetFiles []string
			regSpin := newSpinner(fmt.Sprintf("Scanning %s", filepath.Base(path)))
			filepath.Walk(path, func(p string, f os.FileInfo, err error) error {
				if err != nil || f.IsDir() || !isAssetExt(p) {
					return err
				}
				if is16Ext(p) || isBGMExt(p) {
					assetFiles = append(assetFiles, p) // raw formats — no registry pass
					return nil
				}
				data, err := os.ReadFile(p)
				if err == nil {
					regSpin.Describe(fmt.Sprintf("Registry  %-20s", filepath.Base(p)))
					regSpin.Add(1)
					registry.PopulateFromESFData(data)
					assetFiles = append(assetFiles, p)
				}
				return nil
			})
			regSpin.Finish()
			total := len(assetFiles)
			logf("Surface registry: %d surfaces from %d file(s)\n", registry.Len(), total)

			// Sprite library for zone assembly: resolves ZoneActor references
			// (trees, rocks, buildings) to LODSprite meshes in companion files.
			lib := buildSpriteLibFromDir(path)
			lightLib := buildLightLibFromDir(path)
			// Cross-file resource directory (0x9000 ResourceTable union): recovers
			// streamed static props and shared creatures/items the local scan misses.
			resDir := buildResourceDirFromDir(path)

			baseOut := outputDir
			if baseOut == "" {
				baseOut = "."
			}

			// srcSubdir returns a file's directory relative to the input root, so
			// output mirrors the disc layout (e.g. BGM/, MUSIC/MUSIC0/, BGM/VO1/)
			// instead of dumping every audio/image/video file at the output root.
			srcSubdir := func(p string) string {
				rel, err := filepath.Rel(path, p)
				if err != nil {
					return ""
				}
				if d := filepath.Dir(rel); d != "." {
					return d
				}
				return ""
			}

			// Pass 2: convert with progress bar, organized to mirror source dirs.
			totalSprites := 0
			bar := newBar(total, "Converting")
			for _, p := range assetFiles {
				base := filepath.Base(p)
				bar.Describe(fmt.Sprintf("%-20s", base))
				data, err := os.ReadFile(p)
				if err != nil {
					bar.Add(1)
					continue
				}
				// Media mirrors the source layout; ESF sprites go under a
				// per-file prefix dir. Both under baseOut.
				mediaOut := filepath.Join(baseOut, srcSubdir(p))
				esfOut := filepath.Join(baseOut, strings.TrimSuffix(base, filepath.Ext(base)))
				totalSprites += convertAssetData(data, base, mediaOut, esfOut, registry, lib, lightLib, resDir)
				bar.Add(1)
			}
			bar.Finish()
			if err := writeAssetIndex(baseOut); err != nil {
				logf("index: %v\n", err)
			}
			logf("Done: %d sprite(s) from %d file(s)\n", totalSprites, total)
			return nil
		}

		if strings.ToUpper(filepath.Ext(path)) == ".ISO" {
			return convertISO(path)
		}
		// Single file: media and ESF both go flat into the output dir.
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out := outputDir
		if out == "" {
			out = "."
		}
		registry := eqoa.NewSurfaceRegistry()
		if !is16Ext(path) && !isBGMExt(path) && !isPSSExt(path) {
			registry.PopulateFromESFData(data)
		}
		// Sprite/light libraries + resource directory from sibling files so
		// ZoneActor references resolve even in single-file mode.
		lib := buildSpriteLibFromDir(filepath.Dir(path))
		lightLib := buildLightLibFromDir(filepath.Dir(path))
		resDir := buildResourceDirFromDir(filepath.Dir(path))

		// A single large ESF (a zone like TUNARIA) can take a while and has no
		// per-file bar to move. Drive a spinner that advances per sprite/zone so
		// the CLI shows it is working. Skipped in verbose mode (which already
		// streams per-sprite lines) and for the quick raw formats (.16/.bgm/.pss).
		if !verbose && !is16Ext(path) && !isBGMExt(path) && !isPSSExt(path) {
			base := filepath.Base(path)
			spin := newSpinner(fmt.Sprintf("Converting %s", base))
			n := 0
			progressStep = func() {
				n++
				spin.Describe(fmt.Sprintf("Converting %s (%d)", base, n))
				spin.Add(1)
			}
			defer func() {
				progressStep = nil
				spin.Finish()
			}()
		}
		convertAssetData(data, filepath.Base(path), out, out, registry, lib, lightLib, resDir)
		return nil
	},
}

func isAssetExt(path string) bool {
	ext := strings.ToUpper(filepath.Ext(path))
	return ext == ".CSF" || ext == ".ESF" || ext == ".16" || ext == ".BGM" || ext == ".PSS"
}

func is16Ext(path string) bool {
	return strings.ToUpper(filepath.Ext(path)) == ".16"
}

func isPSSExt(path string) bool {
	return strings.ToUpper(filepath.Ext(path)) == ".PSS"
}

// convertAssetData routes one asset's raw bytes to the converter for its
// extension — the single dispatch shared by directory, ISO, and single-file
// modes. mediaOut is where raw-media output (.png / .flac+.vag / .mp4) is
// written; esfOut is where an ESF's per-sprite GLBs, textures and assembled
// zones go. Callers compute both per mode (flat, source-mirrored, or disc-tree).
// registry/lib/lightLib enable cross-file resolution for ESF sprites and zones.
// Returns the number of sprites exported (0 for non-ESF assets).
func convertAssetData(data []byte, name, mediaOut, esfOut string,
	registry *eqoa.SurfaceRegistry, lib SpriteLibrary, lightLib LightLibrary, resDir ResourceDirectory) int {
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	switch {
	case is16Ext(name):
		if os.MkdirAll(mediaOut, 0755) == nil {
			convert16Data(data, filepath.Join(mediaOut, stem+".png"))
		}
	case isBGMExt(name):
		if os.MkdirAll(mediaOut, 0755) == nil {
			convertBGMData(data, filepath.Join(mediaOut, stem+".flac"))
		}
	case isPSSExt(name):
		// ffmpeg needs a file path, so materialize the raw .PSS first.
		if os.MkdirAll(mediaOut, 0755) == nil {
			raw := filepath.Join(mediaOut, name)
			if os.WriteFile(raw, data, 0644) == nil {
				convertPSSFile(raw, mediaOut, verbose)
			}
		}
	default: // .esf / .csf
		n := convertESFData(data, name, registry, verbose, esfOut)
		convertZoneESFData(data, name, esfOut, verbose, lib, lightLib, resDir, registry)
		return n
	}
	return 0
}

// convertISO opens a disc image, finds every .csf/.esf file in the ISO 9660
// directory tree, and converts each one.
func convertISO(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("opening ISO: %w", err)
	}
	defer f.Close()

	isoFilter := func(p string) bool {
		dot := strings.LastIndexByte(p, '.')
		if dot < 0 {
			return false
		}
		ext := p[dot+1:]
		return ext == "CSF" || ext == "ESF" || ext == "16" || ext == "BGM" || ext == "PSS"
	}

	scan := newSpinner("Scanning disc…")
	files, err := eqoa.ReadISOFiles(f, isoFilter)
	scan.Finish()
	if err != nil {
		return fmt.Errorf("reading ISO: %w", err)
	}
	logf("Found %d asset file(s)\n", len(files))

	// Pass 1: build cross-file surface registry from every ESF on the disc.
	registry := eqoa.NewSurfaceRegistry()
	regBar := newBar(len(files), "Building registry")
	// The same pass also builds the sprite library used by zone assembly to
	// resolve ZoneActor references (trees, rocks, buildings) to LODSprites.
	lib := SpriteLibrary{}
	lightLib := LightLibrary{}
	resDir := ResourceDirectory{}
	for _, isoFile := range files {
		isoFile := isoFile // capture for the lazy re-read closure
		shortName := isoFile.Path[strings.LastIndexByte(isoFile.Path, '/')+1:]
		regBar.Describe(fmt.Sprintf("Registry  %-20s", shortName))
		if is16Ext(shortName) || isBGMExt(shortName) {
			regBar.Add(1)
			continue // raw formats — no registry pass
		}
		data, err := isoFile.ReadAll(f)
		regBar.Add(1)
		if err != nil {
			continue
		}
		registry.PopulateFromESFData(data)
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
	regBar.Finish()
	logf("Surface registry: %d surfaces, sprite library: %d, resource directory: %d\n", registry.Len(), len(lib), len(resDir))

	// Pass 2: convert sprites with progress bar.
	// Output mirrors the disc directory tree: DATA/CHAR/, DATA2/ARENA/, etc.
	baseOut := outputDir
	if baseOut == "" {
		baseOut = "."
	}
	total := len(files)
	totalSprites := 0
	bar := newBar(total, "Converting")
	for _, isoFile := range files {
		shortName := isoFile.Path[strings.LastIndexByte(isoFile.Path, '/')+1:]
		bar.Describe(fmt.Sprintf("%-20s", shortName))

		// Output mirrors the disc tree: media under DATA/, ESF sprites under
		// DATA/<prefix>/.
		discDir := strings.Trim(isoFile.Path[:strings.LastIndexByte(isoFile.Path, '/')], "/")
		mediaOut := filepath.Join(baseOut, discDir)
		esfOut := filepath.Join(mediaOut, strings.TrimSuffix(shortName, filepath.Ext(shortName)))

		data, err := isoFile.ReadAll(f)
		if err != nil {
			bar.Add(1)
			continue
		}
		totalSprites += convertAssetData(data, shortName, mediaOut, esfOut, registry, lib, lightLib, resDir)
		bar.Add(1)
	}
	bar.Finish()
	if err := writeAssetIndex(baseOut); err != nil {
		logf("index: %v\n", err)
	}
	logf("Done: %d sprite(s) from %d file(s)\n", totalSprites, total)
	return nil
}

// convertESFData decompresses (if CESF) and converts raw ESF/CSF bytes.
// outDir is the directory to write GLBs and PNGs into (caller computes this).
// verbose=true prints each sprite as it is exported (single-file mode).
// verbose=false suppresses per-sprite output; the caller prints a summary line.
// Returns the number of sprites exported.
func convertESFData(data []byte, sourceName string, registry *eqoa.SurfaceRegistry, verbose bool, outDir string) int {
	var esfReader io.ReadSeeker

	if len(data) >= 4 && string(data[:4]) == eqoa.MagicCESF {
		dr, _, err := eqoa.DecompressCSF(bytes.NewReader(data))
		if err != nil {
			logf("  Error decompressing: %v\n", err)
			return 0
		}
		all, err := io.ReadAll(dr)
		if err != nil {
			logf("  Error reading decompressed data: %v\n", err)
			return 0
		}
		esfReader = bytes.NewReader(all)
	} else {
		esfReader = bytes.NewReader(data)
	}

	_, objects, _, order, err := eqoa.ParseESF(esfReader)
	if err != nil {
		logf("  Error parsing ESF: %v\n", err)
		return 0
	}

	// Derive a clean prefix from the source filename (e.g. "CHAR" from "CHAR.ESF").
	prefix := sourceName
	if dot := strings.LastIndexByte(prefix, '.'); dot >= 0 {
		prefix = prefix[:dot]
	}

	if err := os.MkdirAll(outDir, 0755); err == nil {
		writeSurfacePNGs(esfReader, objects, order, prefix, outDir, verbose)
		writeAudioWAVs(esfReader, objects, order, prefix, outDir, verbose)
		writeMusicXM(esfReader, objects, order, prefix, outDir, verbose)
		writeParticleFX(esfReader, objects, order, prefix, outDir, verbose)
		writeFonts(esfReader, objects, order, prefix, outDir, verbose)
	}

	sprites := 0
	processed := make(map[int64]bool)
	for _, obj := range objects {
		processObject(esfReader, obj, order, prefix, processed, registry, nil, verbose, &sprites, outDir)
	}
	return sprites

}

// writeSurfacePNGs walks the object tree and writes every Surface (0x1000)
// as PREFIX_0xDICTID.png alongside the GLBs — both material-palette textures
// and standalone surfaces (UI sprite sheets, item icons, face textures, which
// live outside any palette and would otherwise be lost).
//
// Fullscreen UI backdrops are stored SPLIT across consecutive 512×512
// surfaces (the GS has no 1024-wide texture and 4MB VRAM): adjacent
// standalone slices in file order compose one 1024×448 hi-res screen, with
// continuous border art across the seam.  Runs of consecutive standalone
// 512×512 surfaces are therefore additionally stitched into
// PREFIX_screenN.png composites (individual slices are still written;
// palette/model textures are never stitched).
func writeSurfacePNGs(r io.ReadSeeker, objects []*eqoa.ESFObject, order binary.ByteOrder, prefix string, outDir string, verbose bool) {
	written := make(map[uint32]bool)
	count := 0

	// Standalone surfaces in file order, for screen-composite detection.
	type slice struct {
		img  image.Image
		wide bool // 512×512
	}
	var standalone []slice

	var walk func(obj *eqoa.ESFObject, inPalette bool)
	walk = func(obj *eqoa.ESFObject, inPalette bool) {
		t := uint16(obj.Header.ObjectType)
		if t == 0x1001 {
			inPalette = true
		}
		if t == 0x1000 {
			body, err := obj.ReadBody(r)
			if err == nil {
				if s, err := eqoa.ParseSurface(body, order); err == nil && !written[s.DictID] {
					written[s.DictID] = true
					if img, err := s.ToImage(0); err == nil {
						pngName := fmt.Sprintf("%s_0x%X.png", prefix, s.DictID)
						if mn := modelName(s.DictID); mn != "" {
							// Named surfaces: item icons etc. from the manifest.
							pngName = fmt.Sprintf("%s_%s_0x%X.png", prefix, mn, s.DictID)
						}
						pngPath := filepath.Join(outDir, pngName)
						if f, err := os.Create(pngPath); err == nil {
							png.Encode(f, img)
							f.Close()
							count++
						}
						if !inPalette {
							b := img.Bounds()
							standalone = append(standalone, slice{img, b.Dx() == 512 && b.Dy() == 512})
						}
					}
				}
			}
		}
		for _, child := range obj.Children {
			walk(child, inPalette)
		}
	}
	for _, obj := range objects {
		walk(obj, false)
	}

	// Stitch consecutive 512×512 standalone runs into hi-res screens.
	screens := 0
	for i := 0; i < len(standalone); {
		if !standalone[i].wide {
			i++
			continue
		}
		j := i
		for j < len(standalone) && standalone[j].wide {
			j++
		}
		if runLen := j - i; runLen >= 2 && runLen <= 4 {
			const screenH = 448 // visible rows; 448..511 is padding
			comp := image.NewNRGBA(image.Rect(0, 0, runLen*512, screenH))
			for k := 0; k < runLen; k++ {
				draw.Draw(comp, image.Rect(k*512, 0, (k+1)*512, screenH),
					standalone[i+k].img, image.Point{}, draw.Src)
			}
			pngPath := filepath.Join(outDir, fmt.Sprintf("%s_screen%d.png", prefix, screens))
			if f, err := os.Create(pngPath); err == nil {
				png.Encode(f, comp)
				f.Close()
				screens++
			}
		}
		i = j
	}

	if verbose && count > 0 {
		logf("  → %d texture(s) written as PNG\n", count)
	}
	if verbose && screens > 0 {
		logf("  → %d fullscreen composite(s) stitched\n", screens)
	}
}

// writeAudioWAVs walks the object tree, decodes every Adpcm (0xB000) object
// (PS2 VAG ADPCM), and writes PREFIX_0xDICTID.flac — lossless, encoded with
// the pure-Go FLAC encoder.  Sample rate comes from the AdpcmHeader (0xB010)
// child; the stream is the AdpcmSampleData (0xB020) child.
func writeAudioWAVs(r io.ReadSeeker, objects []*eqoa.ESFObject, order binary.ByteOrder, prefix string, outDir string, verbose bool) {
	count := 0

	var walk func(obj *eqoa.ESFObject)
	walk = func(obj *eqoa.ESFObject) {
		if uint16(obj.Header.ObjectType) == 0xB000 {
			var hdr *eqoa.AdpcmHeader
			var sample []byte
			for _, child := range obj.Children {
				switch uint16(child.Header.ObjectType) {
				case 0xB010:
					if body, err := child.ReadBody(r); err == nil {
						hdr, _ = eqoa.ParseAdpcmHeader(body, order)
					}
				case 0xB020:
					sample, _ = child.ReadRaw(r)
				}
			}
			if hdr != nil && len(sample) > 0 {
				pcm := eqoa.DecodeADPCM(sample)
				if len(pcm) > 0 {
					flacPath := filepath.Join(outDir, fmt.Sprintf("%s_0x%X.flac", prefix, hdr.DictID))
					if err := writeFLAC(flacPath, pcm, hdr.SampleRate); err == nil {
						count++
					} else if verbose {
						logf("  FLAC 0x%X: %v\n", hdr.DictID, err)
					}
					// Original ADPCM → .vag (real sample rate; preserves loops).
					vagPath := filepath.Join(outDir, fmt.Sprintf("%s_0x%X.vag", prefix, hdr.DictID))
					vag := append(eqoa.VAGHeader(hdr.SampleRate, uint32(len(sample)), filepath.Base(vagPath)), sample...)
					os.WriteFile(vagPath, vag, 0644)
				}
			}
		}
		for _, child := range obj.Children {
			walk(child)
		}
	}
	for _, obj := range objects {
		walk(obj)
	}

	if verbose && count > 0 {
		logf("  → %d sound(s) written as FLAC\n", count)
	}
}

// writeMusicXM walks the object tree, rebuilds every Xm module (0xB030) into
// a playable FastTracker II file, and writes PREFIX_0xDICTID.xm.  The .xm is
// the lossless master for tracker music — notes plus instruments.
func writeMusicXM(r io.ReadSeeker, objects []*eqoa.ESFObject, order binary.ByteOrder, prefix string, outDir string, verbose bool) {
	count := 0

	var walk func(obj *eqoa.ESFObject)
	walk = func(obj *eqoa.ESFObject) {
		if uint16(obj.Header.ObjectType) == 0xB030 {
			var hb, sd []byte
			for _, child := range obj.Children {
				switch uint16(child.Header.ObjectType) {
				case 0xB040:
					hb, _ = child.ReadRaw(r)
				case 0xB060:
					sd, _ = child.ReadRaw(r)
				}
			}
			if len(hb) > 0 {
				dictID := order.Uint32(hb[0:4])
				name := fmt.Sprintf("%s_%d", prefix, count)
				xm, err := eqoa.RebuildXM(hb, sd, order, name)
				if err == nil {
					path := filepath.Join(outDir, fmt.Sprintf("%s_%d_0x%X.xm", prefix, count, dictID))
					if os.WriteFile(path, xm, 0644) == nil {
						count++
						// Render a playable FLAC alongside the .xm source
						// (openmpt123 → WAV → ffmpeg → FLAC).
						if err := renderXMToFLAC(path); err != nil && verbose {
							logf("  Xm→FLAC 0x%X: %v\n", dictID, err)
						}
					}
				} else if verbose {
					logf("  Xm 0x%X: %v\n", dictID, err)
				}
			}
		}
		for _, child := range obj.Children {
			walk(child)
		}
	}
	for _, obj := range objects {
		walk(obj)
	}

	if verbose && count > 0 {
		logf("  → %d music module(s) written as XM\n", count)
	}
}

// processObject walks the ESF object tree, exporting every sprite it finds.
// ambientPal is the nearest enclosing 0x1110 MaterialPalette above this
// object — inherited by sprites that have no palette of their own (e.g.
// 0x2310 zone terrain sprites and 0x2320 sub-sprites inside a 0x2700).
func processObject(r io.ReadSeeker, obj *eqoa.ESFObject, order binary.ByteOrder, prefix string, processed map[int64]bool, registry *eqoa.SurfaceRegistry, ambientPal *eqoa.ESFObject, verbose bool, sprites *int, outDir string) {
	if processed[obj.Offset] {
		return
	}
	if eqoa.IsSprite(uint16(obj.Header.ObjectType)) {
		processed[obj.Offset] = true
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					if verbose {
						logf("  Warning: sprite 0x%X panicked during export: %v — skipping\n", obj.DictID, rec)
					}
				}
			}()
			asset, err := eqoa.LoadAsset(r, obj, order)
			if err == nil && len(asset.Meshes) > 0 {
				// Sprites with no embedded material palette inherit from the
				// nearest ancestor that has one (zone terrain, sub-sprites, etc.)
				if asset.MatPalObj == nil && ambientPal != nil {
					asset.MatPalObj = ambientPal
				}
				if verbose {
					logf("  sprite 0x%X (%s)\n", asset.ID, eqoa.GetObjectTypeName(int(obj.Header.ObjectType)))
					if asset.HierarchyErr != nil {
						logf("    Warning: skeleton dropped: %v\n", asset.HierarchyErr)
					}
				}
				generateGLB(r, asset, order, prefix, registry, verbose, outDir)
				*sprites++
				if progressStep != nil {
					progressStep()
				}
			}
		}()
	}

	// Update the ambient palette for this subtree: if any direct child is a
	// 0x1110 MaterialPalette, it supersedes the inherited one.
	newAmbient := ambientPal
	for _, child := range obj.Children {
		if uint16(child.Header.ObjectType) == 0x1110 {
			newAmbient = child
			break
		}
	}

	for _, child := range obj.Children {
		processObject(r, child, order, prefix, processed, registry, newAmbient, verbose, sprites, outDir)
	}
}

func generateGLB(r io.ReadSeeker, asset *eqoa.Asset, order binary.ByteOrder, prefix string, registry *eqoa.SurfaceRegistry, verbose bool, outDir string) {
	b := gltf.NewBuilder()
	// Character/item sprites may have sheer cloth (translucency gradients) that
	// must BLEND; zone/environment sprites keep foliage cutouts on MASK to avoid
	// colored halos, so only opt character content into the gradient→BLEND upgrade.
	blendGradients := strings.HasPrefix(prefix, "CHAR") || prefix == "ITEM"
	rootIdx, err := gltf.ExportAssetToBuilder(b, r, asset, order, registry, blendGradients)
	if err != nil {
		if verbose {
			logf("    Error exporting: %v\n", err)
		}
		return
	}
	b.AddSceneNode(rootIdx)

	if err := os.MkdirAll(outDir, 0755); err != nil {
		if verbose {
			logf("    Error creating output dir: %v\n", err)
		}
		return
	}

	// Tag GLBs that carry skeletal animation so animated vs. static meshes are
	// distinguishable at a glance (and greppable) in the export.
	animTag := ""
	if len(b.Doc.Animations) > 0 {
		animTag = "_animated"
	}
	name := fmt.Sprintf("%s%s_0x%X.glb", prefix, animTag, asset.ID)
	if mn := modelName(asset.ID); mn != "" {
		name = fmt.Sprintf("%s_%s%s_0x%X.glb", prefix, mn, animTag, asset.ID)
	}
	outPath := filepath.Join(outDir, name)
	outF, err := os.Create(outPath)
	if err != nil {
		if verbose {
			logf("    Error creating file: %v\n", err)
		}
		return
	}
	b.WriteGLB(outF)
	outF.Close()
	if verbose {
		logf("    → %s\n", outPath)
	}
}

func init() {
	convertCmd.Flags().StringVarP(&outputDir, "output", "o", "", "Output directory for GLB files (default: current directory)")
	convertCmd.Flags().BoolVar(&forceExport, "force", false, "write into a non-empty or different-source output dir (overrides the export guard)")
	convertCmd.Flags().BoolVar(&collisionExport, "collision", true, "export zone collision geometry (0x4200 CollBuffer) as a tagged 'collision' node (on by default; --collision=false to omit)")
	convertCmd.Flags().BoolVar(&markSpawns, "mark-spawns", false, "place a built-in marker at unresolved spawn actors in assembled zones")
	convertCmd.Flags().Float64Var(&spawnScale, "spawn-scale", 1.0, "size multiplier for spawn markers (world units; markers are small vs a zone)")
	rootCmd.AddCommand(convertCmd)
}
