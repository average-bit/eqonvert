package gltf

import (
	"image"
	"image/color"
	"testing"
)

func TestAddImageTexture(t *testing.T) {
	b := NewBuilder()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(0, 0, color.RGBA{255, 128, 0, 255})

	ti := b.AddImageTexture(img)
	if ti != 0 {
		t.Fatalf("first texture index = %d, want 0", ti)
	}
	if len(b.Doc.Images) != 1 || len(b.Doc.Textures) != 1 {
		t.Fatalf("images=%d textures=%d, want 1/1", len(b.Doc.Images), len(b.Doc.Textures))
	}
	if b.Doc.Textures[ti].Source != 0 {
		t.Errorf("texture source = %d, want 0", b.Doc.Textures[ti].Source)
	}
	if b.Doc.Images[0].MimeType != "image/png" || b.Doc.Images[0].BufferView < 0 {
		t.Errorf("image = %+v", b.Doc.Images[0])
	}
	// A second image gets its own index.
	if ti2 := b.AddImageTexture(img); ti2 != 1 {
		t.Errorf("second texture index = %d, want 1", ti2)
	}
}
