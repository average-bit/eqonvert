package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/average-bit/eqonvert/pkg/eqoa"
	"github.com/spf13/cobra"
)

var inspectJSON bool

var inspectCmd = &cobra.Command{
	Use:   "inspect <path>",
	Short: "Inspect a CSF or ESF file or directory",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path := args[0]
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return inspectOne(path)
		}
		failed := 0
		if err := filepath.Walk(path, func(p string, f os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if !f.IsDir() {
				ext := strings.ToUpper(filepath.Ext(p))
				if ext == ".CSF" || ext == ".ESF" {
					if !inspectJSON {
						fmt.Printf("\n--- Inspecting: %s ---\n", p)
					}
					if e := inspectOne(p); e != nil {
						logf("  error: %v\n", e)
						failed++
					}
				}
			}
			return nil
		}); err != nil {
			return fmt.Errorf("walking %s: %w", path, err)
		}
		if failed > 0 {
			return fmt.Errorf("%d file(s) failed to inspect", failed)
		}
		return nil
	},
}

func init() {
	inspectCmd.Flags().BoolVar(&inspectJSON, "json", false, "emit the object tree as JSON")
	rootCmd.AddCommand(inspectCmd)
}

// objNode is the JSON shape of one ESF object (with --json).
type objNode struct {
	Type     string    `json:"type"`
	TypeHex  string    `json:"typeHex"`
	DictID   string    `json:"dictId"`
	Version  int16     `json:"version"`
	Size     int32     `json:"size"`
	Offset   int64     `json:"offset"`
	Zlib     bool      `json:"zlib,omitempty"`
	Children []objNode `json:"children,omitempty"`
}

// inspectOne parses a CSF/ESF file and prints its object tree — as JSON when
// --json is set, otherwise as an indented text tree. Data goes to stdout;
// errors are returned.
func inspectOne(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()

	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		return fmt.Errorf("reading magic: %w", err)
	}
	f.Seek(0, io.SeekStart)

	var esfReader io.ReadSeeker
	format := "ESF"
	magicStr := string(magic[:])
	switch {
	case magicStr == eqoa.MagicCESF:
		format = "CSF"
		r, _, err := eqoa.DecompressCSF(f)
		if err != nil {
			return fmt.Errorf("decompressing CSF: %w", err)
		}
		esfReader = r.(io.ReadSeeker)
	case magicStr == eqoa.MagicOBJF || (magic[0] == 'F' && magic[1] == 'J' && magic[2] == 'B' && magic[3] == 'O'):
		esfReader = f
	default:
		return fmt.Errorf("unknown file magic: %s (%X)", magicStr, magic)
	}

	header, objects, _, order, err := eqoa.ParseESF(esfReader)
	if err != nil {
		return fmt.Errorf("parsing ESF: %w", err)
	}

	if inspectJSON {
		nodes := make([]objNode, len(objects))
		for i, o := range objects {
			nodes[i] = toObjNode(o)
		}
		out := map[string]any{"file": path, "format": format, "objects": nodes}
		if header != nil {
			out["objectCount"] = header.NumberOfObjects
			out["byteOrder"] = fmt.Sprintf("%v", order)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	fmt.Printf("Type: %s\n", format)
	if header != nil {
		fmt.Printf("ESF Header: Objects=%d, Type=%d, Order=%v\n", header.NumberOfObjects, header.FileType, order)
	}
	for _, obj := range objects {
		printObject(obj, 0)
	}
	return nil
}

func toObjNode(obj *eqoa.ESFObject) objNode {
	n := objNode{
		Type:    eqoa.GetObjectTypeName(int(uint16(obj.Header.ObjectType))),
		TypeHex: fmt.Sprintf("0x%04X", uint16(obj.Header.ObjectType)),
		DictID:  fmt.Sprintf("0x%08X", obj.DictID),
		Version: obj.Header.ObjectVersion,
		Size:    obj.Header.ObjectSize,
		Offset:  obj.Offset,
		Zlib:    obj.IsZlib,
	}
	for _, c := range obj.Children {
		n.Children = append(n.Children, toObjNode(c))
	}
	return n
}

func printObject(obj *eqoa.ESFObject, depth int) {
	indent := strings.Repeat("  ", depth)
	typeName := eqoa.GetObjectTypeName(int(uint16(obj.Header.ObjectType)))
	zlibNote := ""
	if obj.IsZlib {
		zlibNote = " [ZLIB]"
	}
	fmt.Printf("%sObject: %s (0x%04X) DictID=0x%08X Ver=%d Size=%d SubObjs=%d @ 0x%X%s\n",
		indent, typeName, uint16(obj.Header.ObjectType), obj.DictID, obj.Header.ObjectVersion, obj.Header.ObjectSize, obj.Header.NumberOfSubObjects, obj.Offset, zlibNote)

	for _, child := range obj.Children {
		printObject(child, depth+1)
	}
}
