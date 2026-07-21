package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

var indexCmd = &cobra.Command{
	Use:   "index <output_dir>",
	Short: "Generate INDEX.md — a navigable catalog of an extraction output directory",
	Long: `Walks a convert output directory and writes INDEX.md at its root:
asset counts by type, a per-zone table (name, sprite count, tile, file link)
for every zone manifest found, and an alphabetical table of all named
character models.  Runs automatically at the end of directory and ISO
conversions; use this command to regenerate it manually.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return writeAssetIndex(args[0])
	},
}

var charNameRe = regexp.MustCompile(`^CHAR_([A-Za-z].*)_0x[0-9A-Fa-f]+\.glb$`)

// writeAssetIndex generates INDEX.md at the root of an output directory.
func writeAssetIndex(base string) error {
	counts := map[string]int{}
	var chars [][2]string // name, relative path
	var manifests []string

	err := filepath.Walk(base, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		fn := info.Name()
		counts[strings.ToLower(filepath.Ext(fn))]++
		if m := charNameRe.FindStringSubmatch(fn); m != nil {
			rel, _ := filepath.Rel(base, p)
			chars = append(chars, [2]string{m[1], rel})
		}
		if strings.HasSuffix(fn, "_zones.json") {
			manifests = append(manifests, p)
		}
		return nil
	})
	if err != nil {
		return err
	}

	var b strings.Builder
	b.WriteString("# Asset index\n\n| type | count |\n|---|---|\n")
	for _, ext := range []string{".glb", ".png", ".flac", ".xm"} {
		fmt.Fprintf(&b, "| %s | %d |\n", ext, counts[ext])
	}

	b.WriteString("\n## Zones\n")
	sort.Strings(manifests)
	for _, mp := range manifests {
		data, err := os.ReadFile(mp)
		if err != nil {
			continue
		}
		var man zoneManifest
		if json.Unmarshal(data, &man) != nil {
			continue
		}
		relDir, _ := filepath.Rel(base, filepath.Dir(mp))
		prefix := strings.TrimSuffix(filepath.Base(mp), "_zones.json")
		named := 0
		for _, z := range man.Zones {
			if z.Name != "" {
				named++
			}
		}
		fmt.Fprintf(&b, "\n### %s (%s) — %d zones, %d named\n", prefix, relDir, len(man.Zones), named)
		b.WriteString("| zone | name | sprites | tile | file |\n|---|---|---|---|---|\n")
		for _, z := range man.Zones {
			col := int(((z.MinPos[0] + z.MaxPos[0]) / 2) / 2000)
			row := int(((z.MinPos[1] + z.MaxPos[1]) / 2) / 2000)
			fmt.Fprintf(&b, "| %d | %s | %d | (%d,%d) | [%s](%s) |\n",
				z.Index, z.Name, z.SpriteCount, col, row,
				filepath.Base(z.GLB), filepath.ToSlash(filepath.Join(relDir, z.GLB)))
		}
	}

	fmt.Fprintf(&b, "\n## Named characters (%d)\n| model | file |\n|---|---|\n", len(chars))
	sort.Slice(chars, func(i, j int) bool {
		return strings.ToLower(chars[i][0]) < strings.ToLower(chars[j][0])
	})
	for _, c := range chars {
		fmt.Fprintf(&b, "| %s | [%s](%s) |\n", c[0], filepath.Base(c[1]), filepath.ToSlash(c[1]))
	}

	out := filepath.Join(base, "INDEX.md")
	if err := os.WriteFile(out, []byte(b.String()), 0644); err != nil {
		return err
	}
	logf("index → %s\n", out)
	return nil
}

func init() {
	devCmd.AddCommand(indexCmd)
}
