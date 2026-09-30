package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
)

// Pack Gray16's big-endian height bytes into RG so an RGBA8 GPU texture
// retains block-level precision. Blue marks valid terrain; zero is empty.
func readDepthMap(path string, width, height int) (*image.RGBA, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("decode depth map: %w", err)
	}
	depth, ok := img.(*image.Gray16)
	if !ok {
		return nil, fmt.Errorf("depth map must be a 16-bit grayscale PNG")
	}
	if depth.Bounds().Dx() != width || depth.Bounds().Dy() != height {
		return nil, fmt.Errorf("depth dimensions %v do not match color texture %dx%d", depth.Bounds().Size(), width, height)
	}
	packed := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			h := depth.Gray16At(x, y).Y
			valid := uint8(0)
			if h != 0 {
				valid = 255
			}
			packed.SetRGBA(x, y, color.RGBA{R: uint8(h >> 8), G: uint8(h), B: valid, A: 255})
		}
	}
	return packed, nil
}
