package gltf

import (
	"encoding/binary"
	"encoding/json"
	"io"
)

type GLTF struct {
	Asset          Asset        `json:"asset"`
	Scene          *int         `json:"scene,omitempty"`
	Scenes         []Scene      `json:"scenes"`
	Nodes          []Node       `json:"nodes"`
	Meshes         []Mesh       `json:"meshes,omitempty"`
	Accessors      []Accessor   `json:"accessors,omitempty"`
	BufferViews    []BufferView `json:"bufferViews,omitempty"`
	Buffers        []Buffer     `json:"buffers,omitempty"`
	Materials      []Material   `json:"materials,omitempty"`
	Textures       []Texture    `json:"textures,omitempty"`
	Images         []Image      `json:"images,omitempty"`
	Skins          []Skin       `json:"skins,omitempty"`
	Animations     []Animation  `json:"animations,omitempty"`
	ExtensionsUsed []string     `json:"extensionsUsed,omitempty"`
	Extensions     *Extensions  `json:"extensions,omitempty"`
}

// Extensions carries document-level glTF extensions. Currently only
// KHR_lights_punctual (zone point lights recovered from 0x2b00 defs).
type Extensions struct {
	Lights *KHRLightsPunctual `json:"KHR_lights_punctual,omitempty"`
}

type KHRLightsPunctual struct {
	Lights []PunctualLight `json:"lights"`
}

type PunctualLight struct {
	Name      string    `json:"name,omitempty"`
	Type      string    `json:"type"` // "point", "directional", "spot"
	Color     []float32 `json:"color,omitempty"`
	Intensity float32   `json:"intensity,omitempty"`
	Range     float32   `json:"range,omitempty"`
}

// NodeExtensions attaches a KHR_lights_punctual light to a node.
type NodeExtensions struct {
	Light *NodeLightRef `json:"KHR_lights_punctual,omitempty"`
}

type NodeLightRef struct {
	Light int `json:"light"`
}

type Animation struct {
	Name     string             `json:"name,omitempty"`
	Channels []AnimationChannel `json:"channels"`
	Samplers []AnimationSampler `json:"samplers"`
}

type AnimationChannel struct {
	Sampler int                    `json:"sampler"`
	Target  AnimationChannelTarget `json:"target"`
}

type AnimationChannelTarget struct {
	Node int    `json:"node"`
	Path string `json:"path"` // "translation", "rotation", "scale"
}

type AnimationSampler struct {
	Input         int    `json:"input"`         // accessor index for times (float SCALAR)
	Output        int    `json:"output"`        // accessor index for values
	Interpolation string `json:"interpolation"` // "LINEAR", "STEP", "CUBICSPLINE"
}

type Asset struct {
	Version string `json:"version"`
}

type Scene struct {
	Nodes []int `json:"nodes"`
	// Extras is the glTF-sanctioned carrier for application-specific data
	// (ignored by viewers).  Used to embed EQOA effect parameters in
	// exported spell-effect GLBs.
	Extras json.RawMessage `json:"extras,omitempty"`
}

type Node struct {
	Mesh        *int            `json:"mesh,omitempty"`
	Skin        *int            `json:"skin,omitempty"`
	Name        string          `json:"name,omitempty"`
	Children    []int           `json:"children,omitempty"`
	Translation []float32       `json:"translation,omitempty"`
	Rotation    []float32       `json:"rotation,omitempty"`
	Scale       []float32       `json:"scale,omitempty"`
	// Matrix is a column-major 4×4 transform, an alternative to TRS. Used to
	// place an animated sprite subtree at an actor's world transform without
	// re-deriving a quaternion. Must not coexist with TRS on the same node, and
	// the node must not be animation-targeted (only its joint children are).
	Matrix      []float32       `json:"matrix,omitempty"`
	Extensions  *NodeExtensions `json:"extensions,omitempty"`
	// Extras is the glTF-sanctioned carrier for application-specific data
	// (ignored by viewers). Used to tag the collision node so downstream apps
	// can filter it out of the visual scene.
	Extras json.RawMessage `json:"extras,omitempty"`
}

type Skin struct {
	InverseBindMatrices *int   `json:"inverseBindMatrices,omitempty"`
	Joints              []int  `json:"joints"`
	Skeleton            *int   `json:"skeleton,omitempty"`
	Name                string `json:"name,omitempty"`
}

type Mesh struct {
	Primitives []Primitive `json:"primitives"`
	Name       string      `json:"name,omitempty"`
}

type Primitive struct {
	Attributes map[string]int `json:"attributes"`
	Indices    *int           `json:"indices,omitempty"`
	Material   *int           `json:"material,omitempty"`
}

type Accessor struct {
	BufferView    int       `json:"bufferView"`
	ByteOffset    int       `json:"byteOffset"`
	ComponentType int       `json:"componentType"`
	Count         int       `json:"count"`
	Type          string    `json:"type"`
	Normalized    bool      `json:"normalized,omitempty"`
	Max           []float32 `json:"max,omitempty"`
	Min           []float32 `json:"min,omitempty"`
}

type BufferView struct {
	Buffer     int `json:"buffer"`
	ByteOffset int `json:"byteOffset"`
	ByteLength int `json:"byteLength"`
	Target     int `json:"target,omitempty"`
}

type Buffer struct {
	ByteLength int `json:"byteLength"`
}

type Material struct {
	Name                 string       `json:"name,omitempty"`
	PBRMetallicRoughness *PBR         `json:"pbrMetallicRoughness,omitempty"`
	EmissiveTexture      *TextureInfo `json:"emissiveTexture,omitempty"`
	OcclusionTexture     *TextureInfo `json:"occlusionTexture,omitempty"`
	EmissiveFactor       []float32    `json:"emissiveFactor,omitempty"`
	AlphaMode            string       `json:"alphaMode,omitempty"`
	AlphaCutoff          *float32     `json:"alphaCutoff,omitempty"`
	DoubleSided          bool         `json:"doubleSided,omitempty"`
}

type PBR struct {
	BaseColorFactor  []float32    `json:"baseColorFactor,omitempty"`
	BaseColorTexture *TextureInfo `json:"baseColorTexture,omitempty"`
	MetallicFactor   float32      `json:"metallicFactor"`
	RoughnessFactor  float32      `json:"roughnessFactor"`
}

type TextureInfo struct {
	Index int `json:"index"`
}

type Texture struct {
	Source int `json:"source"`
}

type Image struct {
	BufferView int    `json:"bufferView"`
	MimeType   string `json:"mimeType"`
}

func (g *GLTF) ToGLB(w io.Writer, binData []byte) error {
	jsonBytes, err := json.Marshal(g)
	if err != nil {
		return err
	}

	// Align JSON to 4 bytes
	for len(jsonBytes)%4 != 0 {
		jsonBytes = append(jsonBytes, ' ')
	}

	// Align BIN to 4 bytes
	for len(binData)%4 != 0 {
		binData = append(binData, 0x00)
	}

	totalLength := 12 + 8 + len(jsonBytes) + 8 + len(binData)

	// Header
	binary.Write(w, binary.LittleEndian, [4]byte{'g', 'l', 'T', 'F'})
	binary.Write(w, binary.LittleEndian, uint32(2))
	binary.Write(w, binary.LittleEndian, uint32(totalLength))

	// JSON Chunk
	binary.Write(w, binary.LittleEndian, uint32(len(jsonBytes)))
	binary.Write(w, binary.LittleEndian, [4]byte{'J', 'S', 'O', 'N'})
	w.Write(jsonBytes)

	// BIN Chunk
	binary.Write(w, binary.LittleEndian, uint32(len(binData)))
	binary.Write(w, binary.LittleEndian, [4]byte{'B', 'I', 'N', 0x00})
	w.Write(binData)

	return nil
}
