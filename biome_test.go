package main

import (
	"encoding/binary"
	"fmt"
	"image/color"
	"testing"

	"github.com/Tnze/go-mc/nbt"
	"github.com/Tnze/go-mc/save"
	"github.com/Tnze/go-mc/save/region"
)

func TestBiomePacking(t *testing.T) {
	for _, size := range []int{1, 2, 3, 5, 9, 17, 33, 64} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			s := chunkSection{BiomePalette: make([][]byte, size)}
			for i := range size {
				s.BiomePalette[i] = []byte(fmt.Sprint(i))
			}
			width := 0
			for 1<<width < size {
				width++
			}
			if width > 0 {
				s.BiomeData = make(longArray, packedLongCount(width, 64)*8)
				for i := 0; i < 64; i++ {
					word := i / (64 / width)
					v := s.BiomeData.At(word) | uint64(i%size)<<((i%(64/width))*width)
					binary.BigEndian.PutUint64(s.BiomeData[word*8:], v)
				}
			}
			for y := 0; y < 16; y++ {
				for z := 0; z < 16; z++ {
					for x := 0; x < 16; x++ {
						want := fmt.Sprint(((y/4)*16 + (z/4)*4 + x/4) % size)
						if got := biomeFromSection(&s, x, y-64, z); got != want {
							t.Fatalf("%d,%d,%d: %s != %s", x, y, z, got, want)
						}
					}
				}
			}
			if size > 1 {
				s.BiomeData = nil
				if biomeFromSection(&s, 0, 0, 0) != "" {
					t.Fatal("missing data did not fall back")
				}
			}
		})
	}
	s := &chunkSection{BiomePalette: [][]byte{[]byte("a"), []byte("b"), []byte("c")}, BiomeData: make(longArray, 16)}
	binary.BigEndian.PutUint64(s.BiomeData, 3)
	if biomeFromSection(s, 0, 0, 0) != "" {
		t.Fatal("invalid index did not fall back")
	}
}

func TestTintColors(t *testing.T) {
	for _, block := range []string{"grass_block", "oak_leaves", "water"} {
		id := []byte("minecraft:" + block)
		plains := tintedBlockColor(id, "minecraft:plains")
		swamp := tintedBlockColor(id, "minecraft:swamp")
		if plains == swamp || plains == blockColors[string(id)] {
			t.Fatalf("%s not biome tinted", block)
		}
		if tintedBlockColor(id, "mod:unknown") != plains {
			t.Fatal("unknown biome fallback differs")
		}
	}
	for _, block := range []string{"stone", "cherry_leaves", "azalea_leaves"} {
		id := []byte("minecraft:" + block)
		if tintedBlockColor(id, "minecraft:swamp") != blockColors[string(id)] {
			t.Fatalf("unexpected tint for %s", block)
		}
	}
	for _, block := range []string{"birch_leaves", "spruce_leaves"} {
		id := []byte("minecraft:" + block)
		if tintedBlockColor(id, "minecraft:plains") != tintedBlockColor(id, "minecraft:swamp") {
			t.Fatal("fixed leaf tint varies")
		}
	}
	// Plains water multiplier is #3f76e4; the base water texture averages #b1b1b1.
	if got := tintedBlockColor([]byte("minecraft:water"), "minecraft:plains"); got != (color.RGBA{43, 81, 158, 255}) {
		t.Fatalf("water = %v", got)
	}
}

func TestBiomeDecoderReuse(t *testing.T) {
	var dec chunkDecoder
	for _, palette := range [][]string{{"minecraft:swamp", "minecraft:plains"}, {"minecraft:desert"}, nil} {
		var c refChunk
		c.Sections = []refSection{{Y: -4, Biomes: save.PaletteContainer[string]{Palette: palette}}}
		if len(palette) > 1 {
			c.Sections[0].Biomes.Data = []uint64{0xaaaaaaaaaaaaaaaa}
		}
		raw, err := marshalBiomeChunk(c)
		if err != nil {
			t.Fatal(err)
		}
		got, err := dec.decode(append([]byte{3}, raw...))
		if err != nil {
			t.Fatal(err)
		}
		compareChunk(t, &c, got)
	}
}

func TestRenderBiomeAtSurface(t *testing.T) {
	// Different surface heights must select different section biomes.
	var c refChunk
	c.YPos = -4
	heights := make([]uint64, 37)
	for i := 0; i < 256; i++ {
		h := uint64(1)
		if i%16 >= 8 {
			h = 17
		}
		heights[i/7] |= h << ((i % 7) * 9)
	}
	c.Heightmaps = map[string][]uint64{"MOTION_BLOCKING": heights}
	for i, b := range []string{"minecraft:plains", "minecraft:swamp"} {
		c.Sections = append(c.Sections, refSection{Y: -4 + i, BlockStates: save.PaletteContainer[save.BlockState]{Palette: []save.BlockState{{Name: "minecraft:water"}}}, Biomes: save.PaletteContainer[string]{Palette: []string{b}}})
	}
	raw, err := marshalBiomeChunk(c)
	if err != nil {
		t.Fatal(err)
	}
	r, err := region.Create(t.TempDir() + "/r.0.0.mca")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := r.WriteSector(0, 0, append([]byte{3}, raw...)); err != nil {
		t.Fatal(err)
	}
	var dec chunkDecoder
	img, _, err := processChunk(&dec, r, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for x, b := range map[int]string{0: "minecraft:plains", 15: "minecraft:swamp"} {
		if img.At(x, 0) != tintedBlockColor([]byte("minecraft:water"), b) {
			t.Fatalf("wrong surface tint at x=%d", x)
		}
	}
}

// NBT decoding accepts int destinations, but encoding needs explicit widths.
func marshalBiomeChunk(c refChunk) ([]byte, error) {
	type block struct{ Name string }
	type section struct {
		Y           int8                          `nbt:"Y"`
		BlockStates save.PaletteContainer[block]  `nbt:"block_states"`
		Biomes      save.PaletteContainer[string] `nbt:"biomes"`
	}
	wire := struct {
		Heightmaps map[string][]uint64 `nbt:"Heightmaps"`
		Sections   []section           `nbt:"sections"`
		YPos       int32               `nbt:"yPos"`
	}{Heightmaps: c.Heightmaps, YPos: int32(c.YPos)}
	for _, s := range c.Sections {
		sec := section{Y: int8(s.Y), Biomes: s.Biomes}
		sec.BlockStates.Data = s.BlockStates.Data
		for _, b := range s.BlockStates.Palette {
			sec.BlockStates.Palette = append(sec.BlockStates.Palette, block{b.Name})
		}
		wire.Sections = append(wire.Sections, sec)
	}
	return nbt.Marshal(wire)
}
