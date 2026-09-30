package main

import (
	"image"
	"image/color"
	"path/filepath"
	"testing"
)

func TestReadDepthMapPreservesHeights(t *testing.T) {
	values := []uint16{0, 1, 32704, 32767, 32768, 32769, 33023, 33024, 65535}
	depth := image.NewGray16(image.Rect(0, 0, len(values), 1))
	for x, v := range values {
		depth.SetGray16(x, 0, color.Gray16{Y: v})
	}
	path := filepath.Join(t.TempDir(), "depth.png")
	if err := saveImage(depth, path); err != nil {
		t.Fatal(err)
	}
	packed, err := readDepthMap(path, len(values), 1)
	if err != nil {
		t.Fatal(err)
	}
	for x, want := range values {
		p := packed.RGBAAt(x, 0)
		if got := uint16(p.R)<<8 | uint16(p.G); got != want {
			t.Errorf("pixel %d: got %d want %d", x, got, want)
		}
		if (p.B != 0) != (want != 0) || p.A != 255 {
			t.Errorf("pixel %d: invalid validity/alpha %v", x, p)
		}
	}
	if _, err := readDepthMap(path, 512, 512); err == nil {
		t.Fatal("accepted mismatched dimensions")
	}
}

func TestReadDepthMapRejectsEightBitPNG(t *testing.T) {
	path := filepath.Join(t.TempDir(), "depth.png")
	if err := saveImage(image.NewGray(image.Rect(0, 0, 1, 1)), path); err != nil {
		t.Fatal(err)
	}
	if _, err := readDepthMap(path, 1, 1); err == nil {
		t.Fatal("accepted eight-bit height data")
	}
}

func TestStitchDepthImageKeepsEmptyChunkSlots(t *testing.T) {
	chunk := image.NewGray16(image.Rect(0, 0, 16, 16))
	chunk.SetGray16(0, 0, color.Gray16{Y: 32832})
	depth := stitchDepthImage([]image.Image{nil, chunk}).(*image.Gray16)
	if got := depth.Gray16At(16, 0).Y; got != 32832 {
		t.Fatalf("height at second chunk: %d", got)
	}
	if got := depth.Gray16At(0, 0).Y; got != 0 {
		t.Fatalf("empty chunk height: %d", got)
	}
}
