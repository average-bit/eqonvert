package cmd

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/average-bit/eqonvert/pkg/eqoa"
	"github.com/spf13/cobra"
)

var dumpBodyCmd = &cobra.Command{
	Use:   "dump-body <file> <object_type_hex>",
	Short: "Dump the body of all objects of a specific type to files",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		path := args[0]
		typeHex := args[1]
		var targetType uint16
		fmt.Sscanf(typeHex, "0x%x", &targetType)
		if targetType == 0 {
			fmt.Sscanf(typeHex, "%x", &targetType)
		}

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

		outputDir := fmt.Sprintf("%s_dump_0x%04X", strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), targetType)
		if err := os.MkdirAll(outputDir, 0755); err != nil {
			return err
		}

		count := 0
		dumpRecursive(esfReader, objects, targetType, order, outputDir, &count)
		logf("Dumped %d objects of type 0x%04X to %s\n", count, targetType, outputDir)
		return nil
	},
}

func init() {
	devCmd.AddCommand(dumpBodyCmd)
}

func dumpRecursive(r io.ReadSeeker, objects []*eqoa.ESFObject, targetType uint16, order binary.ByteOrder, dir string, count *int) {
	for _, obj := range objects {
		if uint16(obj.Header.ObjectType) == targetType {
			outPath := filepath.Join(dir, fmt.Sprintf("obj_%d_0x%X.bin", *count, obj.Offset))
			body, _ := obj.ReadBody(r)
			os.WriteFile(outPath, body, 0644)
			*count++
		}
		dumpRecursive(r, obj.Children, targetType, order, dir, count)
	}
}
