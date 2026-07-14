package cmd

import (
	"bytes"
	"database/sql"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/average-bit/eqonvert/pkg/eqoa"
	"github.com/spf13/cobra"
	_ "modernc.org/sqlite"
)

var (
	worldOut    string
	worldServer string
)

var worldCmd = &cobra.Command{
	Use:   "world <path>",
	Short: "Build a navigable SQLite world database of zones, placements and models",
	Long: `Extract every zone's actor placements (full 7-DOF) into a single SQLite
database, organised region → zone → placement → model, with a zone-adjacency
graph. Accepts a directory, a disc image (.iso), or a single .esf/.csf file.

Browse it with any SQLite tool (e.g. Datasette). See docs/DICTIDS.md.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		src := args[0]
		out := worldOut
		if out == "" {
			out = "world.db"
		}

		regions, err := collectRegions(src)
		if err != nil {
			return err
		}
		if len(regions) == 0 {
			return fmt.Errorf("no zone data found in %s", src)
		}

		db, err := openWorldDB(out)
		if err != nil {
			return err
		}
		defer db.Close()

		if err := writeWorld(db, regions); err != nil {
			return err
		}
		if worldServer != "" {
			n, err := importServer(db, worldServer)
			if err != nil {
				return fmt.Errorf("server import: %w", err)
			}
			logf("Imported %d spawn(s) from %s\n", n, worldServer)
		}
		logf("Wrote %s: %d region(s)\n", out, len(regions))
		return nil
	},
}

func init() {
	worldCmd.Flags().StringVarP(&worldOut, "output", "o", "world.db", "output SQLite database path")
	worldCmd.Flags().StringVar(&worldServer, "server", "", "EQOAGameServer SQL dump to import spawns/names from (EQOA_Master.sql)")
	rootCmd.AddCommand(worldCmd)
}

// --- extracted model ---------------------------------------------------------

type placementRow struct {
	dictID           uint32
	x, y, z          float32
	yaw, pitch, roll float32
	scale            float32
	r, g, b, a       uint8
}

type zoneRow struct {
	index      int
	placements []placementRow
}

type regionRow struct {
	name       string
	sourceFile string
	zones      []zoneRow
}

// --- source walking ----------------------------------------------------------

func collectRegions(src string) ([]regionRow, error) {
	info, err := os.Stat(src)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		var regions []regionRow
		filepath.Walk(src, func(p string, f os.FileInfo, err error) error {
			if err != nil || f.IsDir() || !isZoneSourceExt(p) {
				return nil
			}
			if data, e := os.ReadFile(p); e == nil {
				if r, ok := regionFromData(data, regionNameFromPath(p)); ok {
					regions = append(regions, r)
				}
			}
			return nil
		})
		return regions, nil
	}
	if strings.EqualFold(filepath.Ext(src), ".iso") {
		return collectRegionsFromISO(src)
	}
	// single file
	data, err := os.ReadFile(src)
	if err != nil {
		return nil, err
	}
	if r, ok := regionFromData(data, regionNameFromPath(src)); ok {
		return []regionRow{r}, nil
	}
	return nil, nil
}

func collectRegionsFromISO(path string) ([]regionRow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	files, err := eqoa.ReadISOFiles(f, func(p string) bool { return isZoneSourceExt(p) })
	if err != nil {
		return nil, err
	}
	var regions []regionRow
	for _, isoFile := range files {
		data, err := isoFile.ReadAll(f)
		if err != nil {
			continue
		}
		if r, ok := regionFromData(data, regionNameFromPath(isoFile.Path)); ok {
			regions = append(regions, r)
		}
	}
	return regions, nil
}

func isZoneSourceExt(p string) bool {
	ext := strings.ToUpper(filepath.Ext(p))
	return ext == ".ESF" || ext == ".CSF"
}

func regionNameFromPath(p string) string {
	base := filepath.Base(p)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// regionFromData parses one ESF/CSF and extracts its zones + placements. Returns
// ok=false when the file contains no Zone (0x3000) objects.
func regionFromData(data []byte, name string) (regionRow, bool) {
	var r io.ReadSeeker
	if len(data) >= 4 && string(data[:4]) == eqoa.MagicCESF {
		dr, _, err := eqoa.DecompressCSF(bytes.NewReader(data))
		if err != nil {
			return regionRow{}, false
		}
		all, _ := io.ReadAll(dr)
		r = bytes.NewReader(all)
	} else {
		r = bytes.NewReader(data)
	}
	_, objects, _, order, err := eqoa.ParseESF(r)
	if err != nil {
		return regionRow{}, false
	}

	reg := regionRow{name: name, sourceFile: name}
	f32 := func(b []byte) float32 { return math.Float32frombits(order.Uint32(b)) }

	var collectZones func(o *eqoa.ESFObject)
	collectZones = func(o *eqoa.ESFObject) {
		if uint16(o.Header.ObjectType) == 0x3000 {
			z := zoneRow{index: len(reg.zones)}
			var walkActors func(x *eqoa.ESFObject)
			walkActors = func(x *eqoa.ESFObject) {
				if uint16(x.Header.ObjectType) == 0x6000 {
					if body, e := x.ReadBody(r); e == nil && len(body) >= 36 {
						z.placements = append(z.placements, placementRow{
							dictID: order.Uint32(body[0:4]),
							x:      f32(body[4:8]), y: f32(body[8:12]), z: f32(body[12:16]),
							yaw: f32(body[16:20]), pitch: f32(body[20:24]), roll: f32(body[24:28]),
							scale: f32(body[28:32]),
							r:     body[32], g: body[33], b: body[34], a: body[35],
						})
					}
				}
				for _, c := range x.Children {
					walkActors(c)
				}
			}
			walkActors(o)
			reg.zones = append(reg.zones, z)
			return
		}
		for _, c := range o.Children {
			collectZones(c)
		}
	}
	for _, o := range objects {
		collectZones(o)
	}
	if len(reg.zones) == 0 {
		return regionRow{}, false
	}
	return reg, true
}

// --- database ----------------------------------------------------------------

const worldSchema = `
PRAGMA journal_mode = OFF;
PRAGMA synchronous = OFF;
CREATE TABLE regions (
  region_id INTEGER PRIMARY KEY,
  name TEXT, source_file TEXT,
  zone_count INTEGER, placement_count INTEGER,
  min_x REAL,min_y REAL,min_z REAL, max_x REAL,max_y REAL,max_z REAL
);
CREATE TABLE zones (
  zone_id INTEGER PRIMARY KEY,
  region_id INTEGER REFERENCES regions(region_id),
  zone_index INTEGER, placement_count INTEGER,
  center_x REAL,center_y REAL,center_z REAL,
  min_x REAL,min_y REAL,min_z REAL, max_x REAL,max_y REAL,max_z REAL,
  grid_x INTEGER, grid_z INTEGER, border_mask INTEGER
);
CREATE TABLE models (
  model_dictid INTEGER PRIMARY KEY,
  hex TEXT, name TEXT, kind TEXT, placement_count INTEGER
);
CREATE TABLE placements (
  placement_id INTEGER PRIMARY KEY,
  zone_id INTEGER REFERENCES zones(zone_id),
  model_dictid INTEGER REFERENCES models(model_dictid),
  x REAL,y REAL,z REAL, yaw REAL,pitch REAL,roll REAL, scale REAL,
  r INTEGER,g INTEGER,b INTEGER,a INTEGER
);
CREATE TABLE zone_links (
  zone_id INTEGER REFERENCES zones(zone_id),
  neighbor_id INTEGER REFERENCES zones(zone_id),
  cardinal TEXT, bearing_deg REAL, gap REAL,
  PRIMARY KEY (zone_id, neighbor_id)
);
CREATE TABLE spawns (
  spawn_id INTEGER PRIMARY KEY,
  npc_name TEXT, world INTEGER, zone INTEGER,
  model_dictid INTEGER REFERENCES models(model_dictid),
  x REAL,y REAL,z REAL, facing INTEGER,
  size REAL, hp INTEGER, npc_level INTEGER, npc_type INTEGER, race TEXT
);
CREATE INDEX ix_place_zone ON placements(zone_id);
CREATE INDEX ix_place_model ON placements(model_dictid);
CREATE INDEX ix_spawn_model ON spawns(model_dictid);
CREATE INDEX ix_spawn_loc ON spawns(world,zone);
CREATE VIEW v_unknown_models AS
  SELECT m.model_dictid, m.hex, m.placement_count
  FROM models m WHERE m.name IS NULL ORDER BY m.placement_count DESC;
`

func openWorldDB(path string) (*sql.DB, error) {
	os.Remove(path)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(worldSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	return db, nil
}

func writeWorld(db *sql.DB, regions []regionRow) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	modelCount := map[uint32]int{}
	zoneID := 0
	placeID := 0

	for ri, reg := range regions {
		var rmin, rmax [3]float32
		haveR := false
		regPlace := 0
		for _, z := range reg.zones {
			zoneID++
			var zmin, zmax [3]float32
			haveZ := false
			for _, p := range z.placements {
				placeID++
				modelCount[p.dictID]++
				if _, err := tx.Exec(`INSERT INTO placements VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
					placeID, zoneID, int64(p.dictID), p.x, p.y, p.z, p.yaw, p.pitch, p.roll, p.scale, p.r, p.g, p.b, p.a); err != nil {
					return err
				}
				pt := [3]float32{p.x, p.y, p.z}
				accum(&zmin, &zmax, &haveZ, pt)
				accum(&rmin, &rmax, &haveR, pt)
			}
			cx, cy, cz := center(zmin, zmax)
			if _, err := tx.Exec(`INSERT INTO zones(zone_id,region_id,zone_index,placement_count,center_x,center_y,center_z,min_x,min_y,min_z,max_x,max_y,max_z) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				zoneID, ri+1, z.index, len(z.placements), cx, cy, cz, zmin[0], zmin[1], zmin[2], zmax[0], zmax[1], zmax[2]); err != nil {
				return err
			}
			regPlace += len(z.placements)
		}
		if _, err := tx.Exec(`INSERT INTO regions VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			ri+1, reg.name, reg.sourceFile, len(reg.zones), regPlace, rmin[0], rmin[1], rmin[2], rmax[0], rmax[1], rmax[2]); err != nil {
			return err
		}
	}

	for id, n := range modelCount {
		name := modelName(id)
		var namePtr any
		kind := "unknown"
		if name != "" {
			namePtr = name
			kind = "known"
		} else if id>>16 == 0xDEAD {
			kind = "sentinel"
		}
		if _, err := tx.Exec(`INSERT INTO models VALUES(?,?,?,?,?)`,
			int64(id), fmt.Sprintf("0x%08X", id), namePtr, kind, n); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	return computeZoneLinks(db)
}

func accum(mn, mx *[3]float32, have *bool, p [3]float32) {
	if !*have {
		*mn, *mx, *have = p, p, true
		return
	}
	for k := 0; k < 3; k++ {
		if p[k] < mn[k] {
			mn[k] = p[k]
		}
		if p[k] > mx[k] {
			mx[k] = p[k]
		}
	}
}

func center(mn, mx [3]float32) (float32, float32, float32) {
	return (mn[0] + mx[0]) / 2, (mn[1] + mx[1]) / 2, (mn[2] + mx[2]) / 2
}

// computeZoneLinks derives, for the non-empty zones of each region: a grid
// coordinate (zones tile a regular lattice), an adjacency graph from AABB
// abutment (cardinal + exact bearing 0=N/90=E + gap), and a border mask (which
// N/E/S/W neighbours are missing = edge of world). Empty zones (no placements ⇒
// no position) are skipped — they can't participate in spatial adjacency.
func computeZoneLinks(db *sql.DB) error {
	type zb struct {
		id, region             int
		cx, cz                 float64
		minx, minz, maxx, maxz float64
	}
	rows, err := db.Query(`SELECT zone_id,region_id,center_x,center_z,min_x,min_z,max_x,max_z FROM zones WHERE placement_count > 0`)
	if err != nil {
		return err
	}
	var zs []zb
	var spans []float64
	for rows.Next() {
		var z zb
		rows.Scan(&z.id, &z.region, &z.cx, &z.cz, &z.minx, &z.minz, &z.maxx, &z.maxz)
		zs = append(zs, z)
		spans = append(spans, z.maxx-z.minx, z.maxz-z.minz)
	}
	rows.Close()
	if len(zs) == 0 {
		return nil
	}

	// Cell size = median zone span (≈ lattice pitch for a gridded region).
	cell := median(spans)
	if cell <= 0 {
		cell = 1
	}

	tx, _ := db.Begin()
	defer tx.Rollback()

	// grid coords + occupancy set (per region).
	type cellKey struct{ region, gx, gz int }
	occupied := map[cellKey]bool{}
	gridOf := map[int][2]int{}
	for _, z := range zs {
		gx := int(math.Round(z.cx / cell))
		gz := int(math.Round(z.cz / cell))
		gridOf[z.id] = [2]int{gx, gz}
		occupied[cellKey{z.region, gx, gz}] = true
		tx.Exec(`UPDATE zones SET grid_x=?,grid_z=? WHERE zone_id=?`, gx, gz, z.id)
	}

	// adjacency: AABB abut/overlap (overlap one axis, gap<=tol on the other).
	tol := cell * 0.1
	for i := range zs {
		for j := range zs {
			if i == j || zs[i].region != zs[j].region {
				continue
			}
			gapx := axisGap(zs[i].minx, zs[i].maxx, zs[j].minx, zs[j].maxx)
			gapz := axisGap(zs[i].minz, zs[i].maxz, zs[j].minz, zs[j].maxz)
			if (gapx <= 0 && gapz <= tol) || (gapz <= 0 && gapx <= tol) {
				dE := zs[j].cx - zs[i].cx
				dN := zs[j].cz - zs[i].cz
				bearing := math.Mod(math.Atan2(dE, dN)*180/math.Pi+360, 360)
				gap := math.Max(gapx, gapz)
				if gap < 0 {
					gap = 0
				}
				tx.Exec(`INSERT OR IGNORE INTO zone_links VALUES(?,?,?,?,?)`,
					zs[i].id, zs[j].id, cardinal8(bearing), bearing, gap)
			}
		}
	}

	// border_mask from grid occupancy: bit set where the adjacent cell is empty.
	// N=+z(1), E=+x(2), S=-z(4), W=-x(8).
	for _, z := range zs {
		g := gridOf[z.id]
		mask := 0
		if !occupied[cellKey{z.region, g[0], g[1] + 1}] {
			mask |= 1
		}
		if !occupied[cellKey{z.region, g[0] + 1, g[1]}] {
			mask |= 2
		}
		if !occupied[cellKey{z.region, g[0], g[1] - 1}] {
			mask |= 4
		}
		if !occupied[cellKey{z.region, g[0] - 1, g[1]}] {
			mask |= 8
		}
		tx.Exec(`UPDATE zones SET border_mask=? WHERE zone_id=?`, mask, z.id)
	}
	return tx.Commit()
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	c := append([]float64(nil), v...)
	for i := range c {
		for j := i + 1; j < len(c); j++ {
			if c[j] < c[i] {
				c[i], c[j] = c[j], c[i]
			}
		}
	}
	return c[len(c)/2]
}

// axisGap returns the separation between two 1-D intervals (<=0 means overlap).
func axisGap(aMin, aMax, bMin, bMax float64) float64 {
	if aMax < bMin {
		return bMin - aMax
	}
	if bMax < aMin {
		return aMin - bMax
	}
	return -1
}

func cardinal8(deg float64) string {
	dirs := []string{"N", "NE", "E", "SE", "S", "SW", "W", "NW"}
	return dirs[int((deg+22.5)/45)%8]
}

// --- server-side import (EQOAGameServer SQL dump) ----------------------------

// npcs column indices (from the CREATE TABLE order).
const (
	npcName    = 1
	npcZone    = 2
	npcX       = 5
	npcY       = 6
	npcZ       = 7
	npcFacing  = 8
	npcWorld   = 9
	npcHP      = 11
	npcModelID = 13
	npcSize    = 14
	npcRace    = 33
	npcLevel   = 35
	npcType    = 36
)

// importServer parses the EQOAGameServer SQL dump and loads the `npcs` spawn
// table into `spawns`, naming creature models from npc_name. The server stores a
// DictID as a signed int32, so model_dictid = modelid & 0xFFFFFFFF. Returns the
// number of spawns imported.
func importServer(db *sql.DB, path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	rows := parseInsertRows(string(data), "npcs")

	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	nameOf := map[uint32]string{}
	n := 0
	for _, f := range rows {
		if len(f) <= npcType {
			continue
		}
		mid, _ := strconv.ParseInt(f[npcModelID], 10, 64)
		dict := uint32(mid) // signed int32 → DictID (low 32 bits)
		n++
		if _, err := tx.Exec(`INSERT INTO spawns VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			n, f[npcName], atoiOrNil(f[npcWorld]), atoiOrNil(f[npcZone]), int64(dict),
			atofOrNil(f[npcX]), atofOrNil(f[npcY]), atofOrNil(f[npcZ]), atoiOrNil(f[npcFacing]),
			atofOrNil(f[npcSize]), atoiOrNil(f[npcHP]), atoiOrNil(f[npcLevel]), atoiOrNil(f[npcType]), f[npcRace]); err != nil {
			return 0, err
		}
		if f[npcName] != "" {
			if _, ok := nameOf[dict]; !ok {
				nameOf[dict] = f[npcName]
			}
		}
	}

	// Add/name creature models: ensure a models row exists, and name it (a client
	// prop that's also a creature keeps its existing name if already known).
	for dict, name := range nameOf {
		tx.Exec(`INSERT OR IGNORE INTO models(model_dictid,hex,name,kind,placement_count) VALUES(?,?,?,'creature',0)`,
			int64(dict), fmt.Sprintf("0x%08X", dict), name)
		tx.Exec(`UPDATE models SET name=COALESCE(name,?), kind=CASE WHEN kind='unknown' THEN 'creature' ELSE kind END WHERE model_dictid=?`,
			name, int64(dict))
	}
	return n, tx.Commit()
}

func atoiOrNil(s string) any {
	if s == "" {
		return nil
	}
	if v, err := strconv.ParseInt(s, 10, 64); err == nil {
		return v
	}
	return nil
}

func atofOrNil(s string) any {
	if s == "" {
		return nil
	}
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		return v
	}
	return nil
}

// parseInsertRows extracts every value tuple from all `INSERT INTO <table> VALUES
// (...),(...);` statements in a MySQL dump. Quoted strings are unquoted; NULL
// becomes "". Tolerant of \' and ” escapes and multi-row / multi-statement inserts.
func parseInsertRows(sql, table string) [][]string {
	var out [][]string
	marker := "INSERT INTO `" + table + "`"
	pos := 0
	for {
		s := strings.Index(sql[pos:], marker)
		if s < 0 {
			break
		}
		s += pos
		v := strings.Index(sql[s:], "VALUES")
		if v < 0 {
			break
		}
		i := s + v + len("VALUES")
		for i < len(sql) {
			for i < len(sql) && sql[i] != '(' && sql[i] != ';' {
				i++
			}
			if i >= len(sql) || sql[i] == ';' {
				break
			}
			i++ // past '('
			var fields []string
			var cur strings.Builder
			inStr := false
			for i < len(sql) {
				ch := sql[i]
				if inStr {
					if ch == '\\' && i+1 < len(sql) {
						cur.WriteByte(sql[i+1])
						i += 2
						continue
					}
					if ch == '\'' {
						if i+1 < len(sql) && sql[i+1] == '\'' {
							cur.WriteByte('\'')
							i += 2
							continue
						}
						inStr = false
						i++
						continue
					}
					cur.WriteByte(ch)
					i++
					continue
				}
				if ch == '\'' {
					inStr = true
					i++
					continue
				}
				if ch == ',' {
					fields = append(fields, normNull(cur.String()))
					cur.Reset()
					i++
					continue
				}
				if ch == ')' {
					fields = append(fields, normNull(cur.String()))
					i++
					break
				}
				cur.WriteByte(ch)
				i++
			}
			out = append(out, fields)
		}
		pos = i
	}
	return out
}

func normNull(s string) string {
	s = strings.TrimSpace(s)
	if s == "NULL" {
		return ""
	}
	return s
}
