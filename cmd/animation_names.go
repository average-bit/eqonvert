package cmd

import (
	_ "embed"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/average-bit/eqonvert/pkg/gltf"
)

// animation_names.json maps animation pair indices (== the AnimationState
// byte IDs the EQOA server sends) to human-readable names.  It is the
// version-controlled source of truth — edit the JSON and rebuild.  Loaded
// into the glTF exporter at startup.
//
//go:embed animation_names.json
var animationNamesJSON []byte

func init() {
	var raw map[string]string
	if json.Unmarshal(animationNamesJSON, &raw) != nil {
		return
	}
	names := map[int]string{}
	for k, v := range raw {
		if strings.HasPrefix(k, "_") {
			continue // comment keys
		}
		id, err := strconv.ParseInt(strings.TrimPrefix(strings.ToLower(k), "0x"), 16, 32)
		if err == nil {
			names[int(id)] = v
		}
	}
	gltf.SetAnimationNames(names)
}
