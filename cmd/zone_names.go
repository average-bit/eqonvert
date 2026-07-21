package cmd

import (
	_ "embed"
	"encoding/json"
	"regexp"
	"strings"
	"sync"
)

// zone_tile_names.json carries the EQOA world map tile names (2000-unit
// tiles), extracted from the eqoa.live/map community project.  Each world has
// a zone-name list and a tile grid mapping (row, col) → name index.  Only
// world 0 (Tunaria) has name data.
//
//go:embed zone_tile_names.json
var zoneTileNamesJSON []byte

type tileWorld struct {
	Index     int      `json:"index"`
	Label     string   `json:"label"`
	ZoneNames []string `json:"zone_names"`
	TileGrid  [][]*int `json:"tile_grid"`
}

type tileNames struct {
	Worlds []tileWorld `json:"worlds"`
}

var (
	tileData     *tileNames
	tileDataOnce sync.Once
)

// zoneFileWorld maps a zone ESF file prefix to its map-world index, or -1
// when the world has no tile-name data.
func zoneFileWorld(prefix string) int {
	switch {
	case strings.EqualFold(prefix, "TUNARIA"):
		return 0
	case strings.EqualFold(prefix, "RATHE"):
		return 1
	case strings.EqualFold(prefix, "ODUS"):
		return 2
	}
	return -1
}

// zoneTileName resolves a zone's bounds to its in-game area name via the map
// tile grid.  The bounds CENTER is used — zone geometry bleeds a few units
// past tile edges, so flooring min_pos lands in the neighboring tile.
// Returns "" when unnamed/unknown.
func zoneTileName(prefix string, minPos, maxPos [3]float32) string {
	w := zoneFileWorld(prefix)
	if w < 0 {
		return ""
	}
	tileDataOnce.Do(func() {
		var td tileNames
		if json.Unmarshal(zoneTileNamesJSON, &td) == nil {
			tileData = &td
		}
	})
	if tileData == nil || w >= len(tileData.Worlds) {
		return ""
	}
	world := tileData.Worlds[w]

	// Zone bounds are in GLB world space (East=X, Height=Y, North=Z). The map
	// tile grid is a 2D East×North layout, so column = East and row = North —
	// the North axis is index [2] (Y is height/up after the Y-up orientation).
	cx := float64(minPos[0]+maxPos[0]) / 2
	cy := float64(minPos[2]+maxPos[2]) / 2
	col, row := int(cx/2000), int(cy/2000)
	if row < 0 || row >= len(world.TileGrid) || col < 0 || col >= len(world.TileGrid[row]) {
		return ""
	}
	idx := world.TileGrid[row][col]
	if idx == nil || *idx < 0 || *idx >= len(world.ZoneNames) {
		return ""
	}
	name := world.ZoneNames[*idx]
	if name == "Empty" {
		return ""
	}
	return name
}

var zoneNameSanitize = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// sanitizeZoneName converts an area name to a filename-safe token.
func sanitizeZoneName(name string) string {
	s := strings.ReplaceAll(name, "'", "")
	s = zoneNameSanitize.ReplaceAllString(s, "_")
	return strings.Trim(s, "_")
}
