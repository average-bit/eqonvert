package cmd

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/average-bit/eqonvert/pkg/eqoa"
	"github.com/spf13/cobra"
)

var extractCmd = &cobra.Command{
	Use:   "extract <file>",
	Short: "Extract assets (currently surfaces) from an ESF or CSF file",
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
		outputDir := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)) + "_extracted"
		if err := os.MkdirAll(outputDir, 0755); err != nil {
			return err
		}

		for _, obj := range objects {
			extractRecursive(esfReader, obj, order, outputDir)
		}
		return nil
	},
}

func init() {
	devCmd.AddCommand(extractCmd)
}

func extractRecursive(r io.ReadSeeker, obj *eqoa.ESFObject, order binary.ByteOrder, dir string) {
	switch uint16(obj.Header.ObjectType) {
	case 0x1000: // Surface
		body, _ := obj.ReadBody(r)
		s, err := eqoa.ParseSurface(body, order)
		if err == nil {
			img, err := s.ToImage(0)
			if err == nil {
				outPath := filepath.Join(dir, fmt.Sprintf("surface_0x%X.png", s.DictID))
				outF, err := os.Create(outPath)
				if err == nil {
					png.Encode(outF, img)
					outF.Close()
					logf("Extracted surface: %s\n", outPath)
				}
			}
		}
	case 0xb000: // Adpcm Parent
		extractAdpcm(r, obj, order, dir)
	case 0xb030: // Xm Parent
		joinAndExtractAudio(r, obj, dir, "xm")
	case 0xb010, 0xb020, 0xb040, 0xb060: // Individual components
		extractAudio(r, obj, dir, "bin")
	}

	for _, child := range obj.Children {
		extractRecursive(r, child, order, dir)
	}
}

func joinAndExtractAudio(r io.ReadSeeker, obj *eqoa.ESFObject, dir string, ext string) {
	combined := new(bytes.Buffer)
	for _, child := range obj.Children {
		body, _ := child.ReadBody(r)
		combined.Write(body)
	}

	if combined.Len() > 0 {
		typeName := eqoa.GetObjectTypeName(int(uint16(obj.Header.ObjectType)))
		name := fmt.Sprintf("%s_0x%X_combined.%s", typeName, obj.DictID, ext)
		if obj.DictID == 0 {
			name = fmt.Sprintf("%s_offset_0x%X_combined.%s", typeName, obj.Offset, ext)
		}
		outPath := filepath.Join(dir, name)
		os.WriteFile(outPath, combined.Bytes(), 0644)
		logf("Extracted combined %s: %s\n", typeName, outPath)
	}
}

func extractAudio(r io.ReadSeeker, obj *eqoa.ESFObject, dir string, ext string) {
	body, _ := obj.ReadBody(r)
	if len(body) > 0 {
		typeName := eqoa.GetObjectTypeName(int(uint16(obj.Header.ObjectType)))
		name := fmt.Sprintf("%s_0x%X.%s", typeName, obj.DictID, ext)
		if obj.DictID == 0 {
			name = fmt.Sprintf("%s_offset_0x%X.%s", typeName, obj.Offset, ext)
		}
		outPath := filepath.Join(dir, name)
		err := os.WriteFile(outPath, body, 0644)
		if err == nil {
			logf("Extracted %s: %s\n", typeName, outPath)
		}
	}
}

func extractAdpcm(r io.ReadSeeker, obj *eqoa.ESFObject, order binary.ByteOrder, dir string) {
	var adpcmHeader *eqoa.AdpcmHeader
	var sampleData []byte

	for _, child := range obj.Children {
		switch uint16(child.Header.ObjectType) {
		case 0xb010: // AdpcmHeader
			body, _ := child.ReadBody(r)
			h, err := eqoa.ParseAdpcmHeader(body, order)
			if err == nil {
				adpcmHeader = h
			}
		case 0xb020: // AdpcmSampleData
			body, _ := child.ReadBody(r)
			sampleData = body
		}
	}

	if adpcmHeader != nil && len(sampleData) > 0 {
		typeName := eqoa.GetObjectTypeName(int(uint16(obj.Header.ObjectType)))
		name := fmt.Sprintf("%s_0x%X", typeName, obj.DictID)
		if obj.DictID == 0 {
			name = fmt.Sprintf("%s_offset_0x%X", typeName, obj.Offset)
		}

		vagHeader := adpcmHeader.GenerateVAGHeader(name)

		outPath := filepath.Join(dir, name+".vag")
		outF, err := os.Create(outPath)
		if err == nil {
			outF.Write(vagHeader)
			outF.Write(sampleData)
			outF.Close()
			logf("Extracted VAG: %s (SampleRate: %d, Blocks: %d)\n", outPath, adpcmHeader.SampleRate, adpcmHeader.BlockCount)
		}
	} else if len(sampleData) > 0 {
		// Fallback if header missing but data present
		joinAndExtractAudio(r, obj, dir, "vag")
	}
}
