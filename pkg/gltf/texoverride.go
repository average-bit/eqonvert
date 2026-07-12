package gltf

import (
	"bytes"
	"embed"
	"encoding/json"
	"image"
	_ "image/png"
	"strconv"
	"strings"
)

//go:embed texoverride/*
var texOverrideFS embed.FS

// texOverrides maps a source surface DictID (as referenced by a material's
// TexID) to a replacement image. STOPGAP: some textures are resolved by the
// engine through a runtime resource table we don't yet follow, so the raw
// surface a material points at is wrong (e.g. gate-mural 0x2C68F15C shown on
// dock pillars that should be dark wood). Overrides let us substitute the
// correct-looking texture until that indirection is reverse-engineered.
// Add entries in texoverride/overrides.json ("0xHEX": "file.png").
var texOverrides = loadTexOverrides()

func loadTexOverrides() map[uint32]image.Image {
	out := map[uint32]image.Image{}
	data, err := texOverrideFS.ReadFile("texoverride/overrides.json")
	if err != nil {
		return out
	}
	var m map[string]string
	if json.Unmarshal(data, &m) != nil {
		return out
	}
	for hexID, png := range m {
		id, err := strconv.ParseUint(strings.TrimPrefix(strings.ToLower(hexID), "0x"), 16, 32)
		if err != nil || id == 0 {
			continue
		}
		b, err := texOverrideFS.ReadFile("texoverride/" + png)
		if err != nil {
			continue
		}
		img, _, err := image.Decode(bytes.NewReader(b))
		if err != nil {
			continue
		}
		out[uint32(id)] = img
	}
	return out
}
