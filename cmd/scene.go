package cmd

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/average-bit/eqonvert/pkg/eqoa"
	"github.com/spf13/cobra"
)

type ZoneScene struct {
	Name   string           `json:"name"`
	Actors []eqoa.ZoneActor `json:"actors"`
}

var sceneCmd = &cobra.Command{
	Use:   "scene <file>",
	Short: "Extract ZoneActors to a scene JSON file",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path := args[0]
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()

		var magic [4]byte
		if _, err := io.ReadFull(f, magic[:]); err != nil {
			return fmt.Errorf("reading magic: %w", err)
		}
		f.Seek(0, io.SeekStart)

		var esfReader io.ReadSeeker
		if string(magic[:]) == eqoa.MagicCESF {
			r, _, err := eqoa.DecompressCSF(f)
			if err != nil {
				return fmt.Errorf("decompressing: %w", err)
			}
			esfReader = r.(io.ReadSeeker)
		} else {
			esfReader = f
		}

		_, objects, _, order, err := eqoa.ParseESF(esfReader)
		if err != nil {
			return fmt.Errorf("parsing ESF: %w", err)
		}

		scene := &ZoneScene{Name: filepath.Base(path)}
		collectActors(esfReader, objects, order, &scene.Actors)

		if len(scene.Actors) == 0 {
			logf("No ZoneActors found.\n")
			return nil
		}
		outPath := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)) + "_scene.json"
		data, err := json.MarshalIndent(scene, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(outPath, data, 0644); err != nil {
			return err
		}
		logf("Extracted %d actors to %s\n", len(scene.Actors), outPath)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(sceneCmd)
}

func collectActors(r io.ReadSeeker, objects []*eqoa.ESFObject, order binary.ByteOrder, actors *[]eqoa.ZoneActor) {
	for _, obj := range objects {
		if obj.Header.ObjectType == 0x6000 { // ZoneActor
			body, err := obj.ReadBody(r)
			if err == nil {
				a, err := eqoa.ParseZoneActor(body, order)
				if err == nil {
					*actors = append(*actors, *a)
				}
			}
		}
		collectActors(r, obj.Children, order, actors)
	}
}
