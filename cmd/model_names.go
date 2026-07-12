package cmd

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sync"
)

// model_names.json maps sprite dictIDs (uppercase hex, no 0x prefix) to
// human-readable model names.  It is the version-controlled source of truth,
// edited directly in this repository — add or correct entries and rebuild.
// Provenance: originally recovered from the EQOAGameServer community
// database (player race/sex models from the charactermodel table — the
// server's modelid IS the sprite dictID — and NPC names majority-voted from
// retail spawn captures in the npcs table); see docs/MODEL_NAMES.md.
//
//go:embed model_names.json
var modelNamesJSON []byte

var (
	modelNames     map[string]string
	modelNamesOnce sync.Once
)

// modelName returns the human-readable name for a sprite dictID, or "" when
// unknown.
func modelName(dictID uint32) string {
	modelNamesOnce.Do(func() {
		if err := json.Unmarshal(modelNamesJSON, &modelNames); err != nil {
			modelNames = map[string]string{}
		}
	})
	return modelNames[fmt.Sprintf("%X", dictID)]
}
