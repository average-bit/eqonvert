package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/average-bit/eqonvert/pkg/eqoa"
	"github.com/spf13/cobra"
)

var decompressCmd = &cobra.Command{
	Use:   "decompress <path> [output_dir]",
	Short: "Decompress a CSF file or directory of CSF files",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path := args[0]
		outputDir := "."
		if len(args) > 1 {
			outputDir = args[1]
		}

		info, err := os.Stat(path)
		if err != nil {
			return err
		}

		esfName := func(p string) string {
			return filepath.Join(outputDir, strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))+".ESF")
		}

		if !info.IsDir() {
			return decompressFile(path, esfName(path))
		}

		failed := 0
		if err := filepath.Walk(path, func(p string, f os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if !f.IsDir() && strings.ToUpper(filepath.Ext(p)) == ".CSF" {
				logf("Decompressing %s -> %s\n", p, esfName(p))
				if e := decompressFile(p, esfName(p)); e != nil {
					logf("  error: %v\n", e)
					failed++
				}
			}
			return nil
		}); err != nil {
			return fmt.Errorf("walking %s: %w", path, err)
		}
		if failed > 0 {
			return fmt.Errorf("%d file(s) failed to decompress", failed)
		}
		return nil
	},
}

func init() {
	devCmd.AddCommand(decompressCmd)
}

// decompressFile decompresses one CSF into an ESF at outPath.
func decompressFile(inPath, outPath string) error {
	f, err := os.Open(inPath)
	if err != nil {
		return fmt.Errorf("opening %s: %w", inPath, err)
	}
	defer f.Close()

	r, _, err := eqoa.DecompressCSF(f)
	if err != nil {
		return fmt.Errorf("decompressing %s: %w", inPath, err)
	}

	out, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("creating %s: %w", outPath, err)
	}
	defer out.Close()

	written, err := io.Copy(out, r)
	if err != nil {
		return fmt.Errorf("writing %s: %w", outPath, err)
	}
	logf("  → %d bytes to %s\n", written, outPath)
	return nil
}
