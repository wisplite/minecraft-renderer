package main

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"log"
	"math/bits"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/Tnze/go-mc/save"
	"github.com/Tnze/go-mc/save/region"
)

var (
	TotalChunksProcessed   atomic.Int64
	RecentChunksCounter    atomic.Int64
	CurrentChunksPerSecond atomic.Int64
)

func getAllRegionFiles(path string) []string {
	files, err := os.ReadDir(path)
	if err != nil {
		return nil
	}
	regionFiles := make([]string, 0)
	for _, file := range files {
		if file.IsDir() {
			continue
		}
		regionFiles = append(regionFiles, file.Name())
	}
	return regionFiles
}

func getBlockColor(block string) color.Color {
	blockColor, ok := blockColors[block]
	if !ok {
		return color.RGBA{R: 255, G: 0, B: 255, A: 255}
	}
	return blockColor
}

func saveChunkImage(blocks []string) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for x := 0; x < 16; x++ {
		for z := 0; z < 16; z++ {
			block := blocks[x*16+z]
			color := getBlockColor(block)
			img.Set(x, z, color)
		}
	}
	return img
}

func processChunk(r *region.Region, cx int, cz int) (image.Image, error) {
	if !r.ExistSector(cx, cz) {
		return nil, errors.New("sector does not exist")
	}
	sector, err := r.ReadSector(cx, cz)
	if err != nil {
		log.Printf("Error reading sector: %v", err)
		return nil, err
	}
	var chunk save.Chunk
	if err := chunk.Load(sector); err != nil {
		log.Printf("Error loading chunk: %v", err)
		return nil, err
	}
	motionBlocking, ok := chunk.Heightmaps["MOTION_BLOCKING"]
	if !ok || len(motionBlocking) == 0 {
		log.Printf("Motion blocking heightmap not found")
		return nil, errors.New("motion blocking heightmap not found")
	}
	heights := unpackHeightmap(motionBlocking)
	// Heightmap entries are stored as (worldY - minY + 1). 0 means no block.
	minY := int(chunk.YPos) * 16
	blocks := make([]string, 16*16)
	for x := 0; x < 16; x++ {
		for z := 0; z < 16; z++ {
			stored := heights[x][z]
			if stored == 0 {
				continue
			}
			y := stored - 1 + minY
			sectionHeight := yToSection(y)
			for _, section := range chunk.Sections {
				if int(section.Y) == sectionHeight {
					block, err := getBlockFromSection(&section, x, y, z)
					if err != nil {
						log.Printf("Error getting block from section: %v", err)
						continue
					}
					blocks[x*16+z] = block.Name
				}
			}
		}
	}
	img := saveChunkImage(blocks)
	return img, nil
}

func getBlockFromSection(section *save.Section, x int, y int, z int) (save.BlockState, error) {
	if section == nil {
		return save.BlockState{}, errors.New("section is nil")
	}
	palette := section.BlockStates.Palette
	if len(palette) == 0 {
		return save.BlockState{}, errors.New("section has no block states")
	}
	// A single palette entry fills the whole section, and the packed data array is omitted.
	if len(palette) == 1 {
		return palette[0], nil
	}
	if len(section.BlockStates.Data) == 0 {
		return save.BlockState{}, errors.New("section block state data is missing")
	}

	localX := x & 15
	localZ := z & 15
	localY := y & 15
	// Section blocks are stored in YZX order.
	blockIndex := (localY << 8) | (localZ << 4) | localX
	paletteIndex, err := packedPaletteIndex(section.BlockStates.Data, len(palette), blockIndex)
	if err != nil {
		return save.BlockState{}, err
	}
	if paletteIndex < 0 || paletteIndex >= len(palette) {
		return save.BlockState{}, fmt.Errorf("palette index %d out of range for palette of length %d", paletteIndex, len(palette))
	}
	return palette[paletteIndex], nil
}

// bitsPerBlock is the width of each palette index in a section's packed long array.
// One palette entry uses 0 bits. Otherwise Minecraft stores at least 4 bits,
// rounded up to ceil(log2(paletteLen)), and values do not cross long boundaries.
func bitsPerBlock(paletteLen int) int {
	if paletteLen <= 1 {
		return 0
	}
	n := bits.Len(uint(paletteLen - 1))
	if n < 4 {
		return 4
	}
	return n
}

func packedLongCount(bitsPerValue, valueCount int) int {
	if bitsPerValue == 0 {
		return 0
	}
	valuesPerLong := 64 / bitsPerValue
	return (valueCount + valuesPerLong - 1) / valuesPerLong
}

func packedPaletteIndex(data []uint64, paletteLen, index int) (int, error) {
	const blocksPerSection = 16 * 16 * 16
	if index < 0 || index >= blocksPerSection {
		return 0, fmt.Errorf("block index %d out of range", index)
	}
	bitsPerValue := bitsPerBlock(paletteLen)
	if bitsPerValue == 0 {
		return 0, nil
	}
	if len(data) != packedLongCount(bitsPerValue, blocksPerSection) {
		return 0, fmt.Errorf("packed block data has %d longs, want %d for a %d-entry palette", len(data), packedLongCount(bitsPerValue, blocksPerSection), paletteLen)
	}
	valuesPerLong := 64 / bitsPerValue
	longIndex := index / valuesPerLong
	offset := (index % valuesPerLong) * bitsPerValue
	mask := uint64(1<<bitsPerValue) - 1
	return int((data[longIndex] >> uint(offset)) & mask), nil
}

// unpackHeightmap unpacks 256 9-bit Y values from a 1.18+ heightmap.
// Indexing: heights[x][z] corresponds to chunk-relative coordinates (0..15).
func unpackHeightmap(data []uint64) [16][16]int {
	var heights [16][16]int
	if len(data) == 0 {
		return heights
	}

	const (
		bitsPerVal = 9
		mask       = (1 << bitsPerVal) - 1 // 0x1FF (511)
		perUint64  = 64 / bitsPerVal       // 7 values per word
	)

	entryIdx := 0
	for _, word := range data {
		for i := 0; i < perUint64 && entryIdx < 256; i++ {
			rawY := int((word >> (i * bitsPerVal)) & mask)

			// Values are stored offsets, not world Y. 0 means the column is empty.
			// world Y = rawY - 1 + minY, where minY is chunk.YPos * 16.
			x := entryIdx % 16
			z := entryIdx / 16
			heights[x][z] = rawY

			entryIdx++
		}
	}

	return heights
}

// yToSection converts a world Y coordinate into a chunk section index.
// Sections are 16 blocks tall, matching save.Section.Y.
// The shift is a floor divide, so Y=-1 is section -1 and Y=-64 is section -4.
func yToSection(y int) int {
	return y >> 4
}

// parseRegionName parses the region file path and returns the region name
// r.0.0.mca -> r.0.0
func parseRegionName(regionFile string) string {
	parts := strings.Split(regionFile, "/")
	regionName := parts[len(parts)-1]
	regionName = strings.TrimSuffix(regionName, ".mca")
	return regionName
}

func stitchRegionImage(images []image.Image) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 16*32, 16*32))
	for i, chunkImg := range images {
		if chunkImg == nil || i >= 32*32 {
			continue
		}
		cx := i % 32
		cz := i / 32
		draw.Draw(img, image.Rect(cx*16, cz*16, (cx+1)*16, (cz+1)*16), chunkImg, chunkImg.Bounds().Min, draw.Src)
	}
	return img
}

func saveImage(img image.Image, filename string) error {
	imgFile, err := os.Create(filename)
	if err != nil {
		log.Printf("Error creating image file: %v", err)
		return err
	}
	defer imgFile.Close()
	png.Encode(imgFile, img)
	return nil
}

func processRegion(regionFile string) (image.Image, error) {
	r, err := region.Open(regionFile)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	images := make([]image.Image, 0)
	for cz := 0; cz < 32; cz++ {
		for cx := 0; cx < 32; cx++ {
			if !r.ExistSector(cx, cz) {
				arrayOfAir := make([]string, 16*16)
				images = append(images, saveChunkImage(arrayOfAir))
				continue
			}
			img, err := processChunk(r, cx, cz)
			if err != nil {
				log.Printf("Error processing chunk: %v", err)
				arrayOfAir := make([]string, 16*16)
				images = append(images, saveChunkImage(arrayOfAir))
				continue
			}
			TotalChunksProcessed.Add(1)
			RecentChunksCounter.Add(1)
			images = append(images, img)
		}
	}
	img := stitchRegionImage(images)
	return img, nil
}

func getAllImageFiles(path string) []string {
	files, err := os.ReadDir(path)
	if err != nil {
		return nil
	}
	imageFiles := make([]string, 0)
	for _, file := range files {
		if file.IsDir() {
			continue
		}
		imageFiles = append(imageFiles, file.Name())
	}
	return imageFiles
}

func processAndSaveRegion(path string, regionFile string, parsedRegionName string) error {
	img, err := processRegion(fmt.Sprintf("%s/%s", path, regionFile))
	if err != nil {
		log.Printf("Error processing region %s: %v", regionFile, err)
		return err
	}
	saveImage(img, fmt.Sprintf("images/%s.png", parsedRegionName))
	return nil
}

func processAllRegions(path string) error {
	os.MkdirAll("images", 0755)
	regionFiles := getAllRegionFiles(path)
	imageFiles := getAllImageFiles("images")
	sem := make(chan struct{}, runtime.NumCPU())
	for _, regionFile := range regionFiles {
		sem <- struct{}{}
		go func(regionFile string) {
			defer func() { <-sem }()
			parsedRegionName := parseRegionName(regionFile)
			if slices.Contains(imageFiles, fmt.Sprintf("%s.png", parsedRegionName)) {
				return
			}
			if err := processAndSaveRegion(path, regionFile, parsedRegionName); err != nil {
				log.Printf("Error processing region %s: %v", regionFile, err)
			}
		}(regionFile)
	}
	return nil
}
