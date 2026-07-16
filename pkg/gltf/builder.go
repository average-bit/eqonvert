package gltf

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
)

type Builder struct {
	Doc     *GLTF
	binData *bytes.Buffer
}

func NewBuilder() *Builder {
	defaultScene := 0
	return &Builder{
		Doc: &GLTF{
			Asset:  Asset{Version: "2.0"},
			Scene:  &defaultScene,
			Scenes: []Scene{{Nodes: []int{}}},
		},
		binData: new(bytes.Buffer),
	}
}

func (b *Builder) AddBufferView(data []byte, target int) int {
	// Align to 4 bytes
	for b.binData.Len()%4 != 0 {
		b.binData.WriteByte(0)
	}

	offset := b.binData.Len()
	b.binData.Write(data)

	bvIdx := len(b.Doc.BufferViews)
	b.Doc.BufferViews = append(b.Doc.BufferViews, BufferView{
		Buffer:     0,
		ByteOffset: offset,
		ByteLength: len(data),
		Target:     target,
	})
	return bvIdx
}

func (b *Builder) AddAccessor(bvIdx int, byteOffset int, compType int, count int, attrType string, normalized bool) int {
	accIdx := len(b.Doc.Accessors)
	b.Doc.Accessors = append(b.Doc.Accessors, Accessor{
		BufferView:    bvIdx,
		ByteOffset:    byteOffset,
		ComponentType: compType,
		Count:         count,
		Type:          attrType,
		Normalized:    normalized,
	})
	return accIdx
}

func (b *Builder) AddNode(node Node) int {
	nodeIdx := len(b.Doc.Nodes)
	b.Doc.Nodes = append(b.Doc.Nodes, node)
	return nodeIdx
}

func (b *Builder) AddMesh(mesh Mesh) int {
	meshIdx := len(b.Doc.Meshes)
	b.Doc.Meshes = append(b.Doc.Meshes, mesh)
	return meshIdx
}

func (b *Builder) AddSkin(skin Skin) int {
	skinIdx := len(b.Doc.Skins)
	b.Doc.Skins = append(b.Doc.Skins, skin)
	return skinIdx
}

func (b *Builder) AddSceneNode(nodeIdx int) {
	b.Doc.Scenes[0].Nodes = append(b.Doc.Scenes[0].Nodes, nodeIdx)
}

// AddPointLight registers a KHR_lights_punctual point light and returns its
// index (referenced from a node's extensions). Registers the extension on
// first use.
func (b *Builder) AddPointLight(name string, color [3]float32, intensity, rng float32) int {
	if b.Doc.Extensions == nil || b.Doc.Extensions.Lights == nil {
		b.Doc.Extensions = &Extensions{Lights: &KHRLightsPunctual{}}
		b.Doc.ExtensionsUsed = append(b.Doc.ExtensionsUsed, "KHR_lights_punctual")
	}
	idx := len(b.Doc.Extensions.Lights.Lights)
	b.Doc.Extensions.Lights.Lights = append(b.Doc.Extensions.Lights.Lights, PunctualLight{
		Name:      name,
		Type:      "point",
		Color:     []float32{color[0], color[1], color[2]},
		Intensity: intensity,
		Range:     rng,
	})
	return idx
}

// AddLightNode creates a scene node at pos referencing point light lightIdx.
func (b *Builder) AddLightNode(name string, pos [3]float32, lightIdx int) int {
	nodeIdx := b.AddNode(Node{
		Name:        name,
		Translation: []float32{pos[0], pos[1], pos[2]},
		Extensions:  &NodeExtensions{Light: &NodeLightRef{Light: lightIdx}},
	})
	b.AddSceneNode(nodeIdx)
	return nodeIdx
}

// AddCollisionNode appends a triangle-mesh node built from world-space positions
// and a flat triangle-list index buffer, tagged with extras {"collision": true}
// so downstream apps can filter it out of the visual scene. It returns the node
// index, or -1 if there is nothing to emit. The node is added to the scene root.
func (b *Builder) AddCollisionNode(name string, positions [][3]float32, indices []uint32) int {
	if len(positions) == 0 || len(indices) < 3 {
		return -1
	}

	// Positions -> f32x3 buffer view + accessor (with min/max, required by spec
	// for POSITION accessors).
	posBuf := new(bytes.Buffer)
	minv := [3]float32{positions[0][0], positions[0][1], positions[0][2]}
	maxv := minv
	for _, p := range positions {
		for k := 0; k < 3; k++ {
			if p[k] < minv[k] {
				minv[k] = p[k]
			}
			if p[k] > maxv[k] {
				maxv[k] = p[k]
			}
			binary.Write(posBuf, binary.LittleEndian, p[k])
		}
	}
	posBv := b.AddBufferView(posBuf.Bytes(), 34962) // ARRAY_BUFFER
	posAcc := b.AddAccessor(posBv, 0, 5126, len(positions), "VEC3", false)
	b.Doc.Accessors[posAcc].Min = []float32{minv[0], minv[1], minv[2]}
	b.Doc.Accessors[posAcc].Max = []float32{maxv[0], maxv[1], maxv[2]}

	// Indices -> u32 buffer view + accessor.
	idxBuf := new(bytes.Buffer)
	for _, idx := range indices {
		binary.Write(idxBuf, binary.LittleEndian, idx)
	}
	idxBv := b.AddBufferView(idxBuf.Bytes(), 34963) // ELEMENT_ARRAY_BUFFER
	idxAcc := b.AddAccessor(idxBv, 0, 5125, len(indices), "SCALAR", false)

	mesh := Mesh{
		Name: name,
		Primitives: []Primitive{{
			Attributes: map[string]int{"POSITION": posAcc},
			Indices:    &idxAcc,
		}},
	}
	meshIdx := b.AddMesh(mesh)

	nodeIdx := b.AddNode(Node{
		Name:   name,
		Mesh:   &meshIdx,
		Extras: json.RawMessage(`{"collision":true}`),
	})
	b.AddSceneNode(nodeIdx)
	return nodeIdx
}

func (b *Builder) WriteGLB(w io.Writer) error {
	b.Doc.Buffers = []Buffer{{ByteLength: b.binData.Len()}}
	return b.Doc.ToGLB(w, b.binData.Bytes())
}
