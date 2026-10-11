package gltf

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/average-bit/eqonvert/pkg/eqoa"
)

// Clip names and pairing from the CSprite 0x2910 animation table (planClips).
// The synthetic records follow the 0x2910 v3 layout in docs/FORMATS.md.

func items(specs ...clipItem) []clipItem { return specs }

func groupNames(p clipPlan) []string {
	var out []string
	for _, g := range p.groups {
		out = append(out, g.name)
	}
	return out
}

func memberAIs(g clipGroup) []int {
	var out []int
	for _, it := range g.members {
		out = append(out, it.ai)
	}
	return out
}

// withNames installs an animation name table for one test.
func withNames(t *testing.T, names map[int]string) {
	t.Helper()
	old := animStateNames
	SetAnimationNames(names)
	t.Cleanup(func() { SetAnimationNames(old) })
}

func TestPlanClipsPairsByRecordNotByIndex(t *testing.T) {
	withNames(t, map[int]string{})
	// The two halves of a record need not be consecutive in asset.Actions.
	in := items(
		clipItem{0, 0xA1, false}, clipItem{1, 0xB1, false},
		clipItem{2, 0xA2, true}, clipItem{3, 0xB2, true},
	)
	table := []eqoa.CSpriteAnimation{
		{ActionID: 0xA1, Action2ID: 0xA2, AnimID: 0x00},
		{ActionID: 0xB1, Action2ID: 0xB2, AnimID: 0x0E},
	}
	p := planClips(in, table)
	if got, want := groupNames(p), []string{"0x00_Unknown_0xA1", "0x0E_Unknown_0xB1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("names %v, want %v", got, want)
	}
	if got := memberAIs(p.groups[1]); !reflect.DeepEqual(got, []int{1, 3}) {
		t.Errorf("anim 0x0E members %v, want [1 3]", got)
	}
	if !p.groups[1].mapped || p.groups[1].animID != 0x0E {
		t.Errorf("group %+v", p.groups[1])
	}
	if got := p.layerName[3]; got != "0x0E_Unknown_lower_0xB2" {
		t.Errorf("layer 3 = %q", got)
	}
	if got := p.layerName[1]; got != "0x0E_Unknown_upper_0xB1" {
		t.Errorf("layer 1 = %q", got)
	}
}

func TestPlanClipsNamesByAnimIDAndSkipsReuse(t *testing.T) {
	withNames(t, map[int]string{5: "SyntheticName"})
	in := items(clipItem{0, 0x10, false}, clipItem{1, 0x11, true}, clipItem{2, 0x20, false})
	// id 5 owns 0x10/0x11; id 6 names the same sets; id 7 has no Action2ID.
	table := []eqoa.CSpriteAnimation{
		{ActionID: 0x10, Action2ID: 0x11, AnimID: 5},
		{ActionID: 0x10, Action2ID: 0x11, AnimID: 6},
		{ActionID: 0x20, AnimID: 7},
	}
	p := planClips(in, table)
	if got, want := groupNames(p), []string{"0x05_SyntheticName_0x10", "0x07_Unknown_0x20"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("names %v, want %v", got, want)
	}
	if got := p.layerName[1]; got != "0x05_SyntheticName_lower_0x11" {
		t.Errorf("layer 1 = %q", got)
	}
}

func TestPlanClipsSharedSetStaysInLaterMergedClip(t *testing.T) {
	withNames(t, map[int]string{})
	in := items(clipItem{0, 0x10, false}, clipItem{1, 0x11, true}, clipItem{2, 0x12, true})
	table := []eqoa.CSpriteAnimation{
		{ActionID: 0x10, Action2ID: 0x11, AnimID: 1},
		{ActionID: 0x10, Action2ID: 0x12, AnimID: 3},
	}
	p := planClips(in, table)
	if len(p.groups) != 2 || !reflect.DeepEqual(memberAIs(p.groups[1]), []int{0, 2}) || len(p.groups[1].owned) != 1 {
		t.Fatalf("groups %+v", p.groups)
	}
	if p.groupOf[0] != 0 || p.groupOf[2] != 1 {
		t.Errorf("groupOf %v", p.groupOf)
	}
}

func TestPlanClipsUnmapped(t *testing.T) {
	withNames(t, map[int]string{0: "SyntheticName"})
	// With a table, a set no record names stands alone and gets no name.
	p := planClips(items(clipItem{0, 0x10, false}, clipItem{1, 0x99, true}),
		[]eqoa.CSpriteAnimation{{ActionID: 0x10, AnimID: 0}})
	if got, want := groupNames(p), []string{"0x00_SyntheticName_0x10", "Unmapped_action1_0x99"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("names %v, want %v", got, want)
	}
	if p.groups[1].mapped || p.layerName[1] != "Unmapped_action1_lower_0x99" {
		t.Errorf("unmapped group %+v, layer %q", p.groups[1], p.layerName[1])
	}
	// Without a table the consecutive pairing still groups the sets.
	q := planClips(items(clipItem{0, 0x10, false}, clipItem{1, 0x11, true}, clipItem{2, 0x12, false}), nil)
	if got, want := groupNames(q), []string{"Unmapped_pair0_0x10", "Unmapped_pair1_0x12"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("names %v, want %v", got, want)
	}
	if !reflect.DeepEqual(memberAIs(q.groups[0]), []int{0, 1}) || q.layerName[0] != "Unmapped_action0_upper_0x10" {
		t.Errorf("group %+v, layer %q", q.groups[0], q.layerName[0])
	}
}

// TestExportClipsFollowAnimTable exports an asset whose upper and lower halves
// are not consecutive in Actions; the 0x2910 table pairs them.
func TestExportClipsFollowAnimTable(t *testing.T) {
	withNames(t, map[int]string{})
	joints := minimalJoints(3)
	joints[1].ParentIndex = 0
	joints[2].ParentIndex = 0
	asset := &eqoa.Asset{
		ID:        0x10,
		Hierarchy: &eqoa.HSpriteHierarchy{Joints: joints},
		BoneMap:   map[int32]int32{0: 0, 1: 1, 2: 2},
		Actions: []*eqoa.ActionSet{
			actionTargeting(0xA1, 1),    // upper of 0x0E
			actionTargeting(0xB1, 2),    // upper of 0x01
			actionTargeting(0xA2, 0),    // lower of 0x0E (root)
			actionTargeting(0xB2, 0, 2), // lower of 0x01 (root)
		},
		AnimTable: []eqoa.CSpriteAnimation{
			{ActionID: 0xB1, Action2ID: 0xB2, AnimID: 0x01},
			{ActionID: 0xA1, Action2ID: 0xA2, AnimID: 0x0E},
		},
	}
	var got []string
	for _, a := range exportAnims(t, asset) {
		got = append(got, a.Name)
	}
	want := []string{
		"0x01_Unknown_0xB1", "0x0E_Unknown_0xA1",
		"0x0E_Unknown_upper_0xA1", "0x01_Unknown_upper_0xB1", "0x0E_Unknown_lower_0xA2", "0x01_Unknown_lower_0xB2",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("animations\n got %v\nwant %v", got, want)
	}
}

// ---- disc-gated ----

// discAssetFiles returns every CSF/ESF under EQOA_DATA (a directory or an
// .iso) as name -> decompressed ESF bytes, one at a time through fn.
func discAssetFiles(t *testing.T, dir string, fn func(name string, esf []byte)) {
	t.Helper()
	isESF := func(p string) bool {
		u := strings.ToUpper(p)
		return strings.HasSuffix(u, ".CSF") || strings.HasSuffix(u, ".ESF")
	}
	decode := func(name string, data []byte) {
		if len(data) >= 4 && string(data[:4]) == eqoa.MagicCESF {
			dr, _, err := eqoa.DecompressCSF(bytes.NewReader(data))
			if err != nil {
				t.Errorf("%s: %v", name, err)
				return
			}
			if data, err = io.ReadAll(dr); err != nil {
				t.Errorf("%s: %v", name, err)
				return
			}
		}
		fn(name, data)
	}
	if st, err := os.Stat(dir); err == nil && !st.IsDir() && strings.EqualFold(filepath.Ext(dir), ".iso") {
		f, err := os.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		files, err := eqoa.ReadISOFiles(f, isESF)
		if err != nil {
			t.Fatal(err)
		}
		for i := range files {
			data, err := files[i].ReadAll(f)
			if err != nil {
				t.Fatal(err)
			}
			decode(files[i].Path, data)
		}
		return
	}
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !isESF(p) {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		decode(p, data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

var clipNameRE = regexp.MustCompile(`^0x([0-9A-F]+)_.*_0x([0-9A-F]+)$`)

// TestDiscClipNamesMatchAnimTable checks, on real disc data, that every named
// clip of every CSprite with a 0x2910 table names an AnimID whose record has
// the clip's ActionSet as its ActionID or Action2ID. Runs only when EQOA_DATA
// points at an extracted disc directory or an .iso.
func TestDiscClipNamesMatchAnimTable(t *testing.T) {
	dir := os.Getenv("EQOA_DATA")
	if dir == "" {
		t.Skip("EQOA_DATA is not set (no disc data available)")
	}
	var sprites, named, unmapped int
	var bad []string
	discAssetFiles(t, dir, func(name string, esf []byte) {
		r := bytes.NewReader(esf)
		_, objects, _, order, err := eqoa.ParseESF(r)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			return
		}
		var visit func(o *eqoa.ESFObject)
		visit = func(o *eqoa.ESFObject) {
			if uint16(o.Header.ObjectType) == 0x2700 {
				asset, err := eqoa.LoadAsset(r, o, order)
				if err == nil && len(asset.AnimTable) > 0 && asset.Hierarchy != nil && len(asset.Actions) > 0 {
					sprites++
					// Meshes and materials do not affect clip names; skip them.
					a := *asset
					a.Meshes, a.MatPalObj = nil, nil
					b := NewBuilder()
					if _, err := ExportAssetToBuilder(b, r, &a, order, nil, true); err != nil {
						t.Errorf("%s 0x%X: %v", name, asset.ID, err)
					}
					for _, anim := range b.Doc.Animations {
						if strings.HasPrefix(anim.Name, "Unmapped_") {
							unmapped++
							continue
						}
						m := clipNameRE.FindStringSubmatch(anim.Name)
						if m == nil {
							bad = append(bad, name+": "+anim.Name)
							continue
						}
						named++
						animID, _ := strconv.ParseUint(m[1], 16, 32)
						dictID, _ := strconv.ParseUint(m[2], 16, 32)
						ok := false
						for _, rec := range asset.AnimTable {
							if uint64(rec.AnimID) == animID && (uint64(rec.ActionID) == dictID || uint64(rec.Action2ID) == dictID) {
								ok = true
								break
							}
						}
						if !ok {
							bad = append(bad, name+": "+anim.Name)
						}
					}
				}
			}
			for _, c := range o.Children {
				visit(c)
			}
		}
		for _, o := range objects {
			visit(o)
		}
	})
	t.Logf("%d CSprites with a 0x2910 table, %d named clips, %d unmapped clips", sprites, named, unmapped)
	if sprites == 0 {
		t.Fatal("no CSprite with a 0x2910 v3 table found under EQOA_DATA")
	}
	if len(bad) > 0 {
		if len(bad) > 20 {
			bad = bad[:20]
		}
		t.Errorf("clips whose name does not match the 0x2910 table:\n%s", strings.Join(bad, "\n"))
	}
}
