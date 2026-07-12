// Standalone probe: print material layer TexIDs from ARENA.CSF and whether
// they're found in the surface registry built from the FRONTIERSBETA directory.
package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/average-bit/eqonvert/pkg/eqoa"
)

func main() {
	dir := "../../FRONTIERSBETA"
	arenaPath := filepath.Join(dir, "DATA2", "ARENA.CSF")

	// Build registry from all ESF/CSF in the directory.
	registry := eqoa.NewSurfaceRegistry()
	filepath.Walk(dir, func(p string, f os.FileInfo, err error) error {
		if err != nil || f.IsDir() {
			return err
		}
		ext := strings.ToUpper(filepath.Ext(p))
		if ext != ".ESF" && ext != ".CSF" {
			return nil
		}
		data, err := os.ReadFile(p)
		if err == nil {
			registry.PopulateFromESFData(data)
		}
		return nil
	})
	fmt.Printf("Registry: %d surfaces\n\n", registry.Len())

	// Parse the arena file.
	data, err := os.ReadFile(arenaPath)
	if err != nil {
		panic(err)
	}

	var esfReader io.ReadSeeker
	if len(data) >= 4 && string(data[:4]) == "CESF" {
		dr, _, err := eqoa.DecompressCSF(bytes.NewReader(data))
		if err != nil {
			panic(err)
		}
		all, _ := io.ReadAll(dr)
		esfReader = bytes.NewReader(all)
	} else {
		esfReader = bytes.NewReader(data)
	}

	_, objects, _, order, err := eqoa.ParseESF(esfReader)
	if err != nil {
		panic(err)
	}

	// First: print top-level object types to understand ARENA structure.
	fmt.Println("Top-level objects in ARENA.CSF:")
	for _, obj := range objects {
		fmt.Printf("  type=0x%04X dictID=0x%X children=%d\n",
			uint16(obj.Header.ObjectType), obj.DictID, len(obj.Children))
		for _, ch := range obj.Children {
			fmt.Printf("    child type=0x%04X dictID=0x%X children=%d\n",
				uint16(ch.Header.ObjectType), ch.DictID, len(ch.Children))
		}
	}

	found := 0
	missing := 0

	var walk func(obj *eqoa.ESFObject)
	walk = func(obj *eqoa.ESFObject) {
		// Check all object types for material arrays, not just known sprite types.
		var matArray *eqoa.ESFObject
		for _, child := range obj.Children {
			if uint16(child.Header.ObjectType) == 0x1101 {
				matArray = child
				break
			}
		}
		if matArray != nil {
			for _, mObj := range matArray.Children {
				body, _ := mObj.ReadBody(esfReader)
				m, err := eqoa.ParseMaterialBody(body, mObj.Header.ObjectVersion, order)
				if err != nil {
					continue
				}
				for li, layer := range m.Layers {
					_, ok := registry.Get(layer.TexID)
					status := "✓"
					if !ok {
						status = "✗"
						missing++
					} else {
						found++
					}
					fmt.Printf("  obj 0x%X(type=0x%04X) mat 0x%X layer %d: TexID=0x%X %s\n",
						obj.DictID, uint16(obj.Header.ObjectType), m.DictID, li, layer.TexID, status)
				}
			}
		}
		for _, child := range obj.Children {
			walk(child)
		}
	}
	for _, obj := range objects {
		walk(obj)
	}

	fmt.Printf("\nFound: %d  Missing: %d\n", found, missing)
}
