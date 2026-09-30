package main

import (
	"image/color"
	"math/bits"
)

type biomeTint struct{ Grass, Foliage, Water uint32 }

// biomeFromSection samples the saved quart-resolution biome at the surface.
// Missing, malformed, and unknown biome data fall back to plains when tinting.
func biomeFromSection(s *chunkSection, x, y, z int) string {
	if s == nil || len(s.BiomePalette) == 0 {
		return ""
	}
	if len(s.BiomePalette) == 1 {
		return string(s.BiomePalette[0])
	}
	width := bits.Len(uint(len(s.BiomePalette) - 1))
	if s.BiomeData.Len() != packedLongCount(width, 64) {
		return ""
	}
	index := ((y&15)>>2)<<4 | ((z&15)>>2)<<2 | (x&15)>>2
	perLong := 64 / width
	entry := int(s.BiomeData.At(index/perLong) >> ((index % perLong) * width) & ((1 << width) - 1))
	if entry >= len(s.BiomePalette) {
		return ""
	}
	return string(s.BiomePalette[entry])
}

func tintedBlockColor(block []byte, biome string) color.RGBA {
	base, ok := blockColors[string(block)]
	if !ok {
		return color.RGBA{R: 255, B: 255, A: 255}
	}
	t, ok := biomeTints[biome]
	if !ok {
		t = biomeTints["minecraft:plains"]
	}
	var tint uint32
	switch string(block) {
	case "minecraft:grass_block", "minecraft:short_grass", "minecraft:grass", "minecraft:tall_grass", "minecraft:fern", "minecraft:large_fern", "minecraft:sugar_cane":
		tint = t.Grass
	case "minecraft:oak_leaves", "minecraft:jungle_leaves", "minecraft:acacia_leaves", "minecraft:dark_oak_leaves", "minecraft:mangrove_leaves", "minecraft:vine":
		tint = t.Foliage
	case "minecraft:water", "minecraft:bubble_column":
		tint = t.Water
	case "minecraft:spruce_leaves":
		tint = 0x619961
	case "minecraft:birch_leaves":
		tint = 0x80a755
	case "minecraft:lily_pad":
		tint = 0x208030
	default:
		return base
	}
	return color.RGBA{
		R: uint8(uint32(base.R) * (tint >> 16 & 255) / 255),
		G: uint8(uint32(base.G) * (tint >> 8 & 255) / 255),
		B: uint8(uint32(base.B) * (tint & 255) / 255), A: base.A,
	}
}
