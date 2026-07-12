package gltf

import (
	"bytes"
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

func (b *Builder) WriteGLB(w io.Writer) error {
	b.Doc.Buffers = []Buffer{{ByteLength: b.binData.Len()}}
	return b.Doc.ToGLB(w, b.binData.Bytes())
}
