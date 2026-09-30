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
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/Tnze/go-mc/save/region"
)

// A separate cache prevents pre-tint images from being reused.
const imageCacheDir = "images"

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

func getBlockColor(block []byte) color.Color {
	blockColor, ok := blockColors[string(block)]
	if !ok {
		return color.RGBA{R: 255, G: 0, B: 255, A: 255}
	}
	return blockColor
}

func saveChunkImage(blocks [][]byte) image.Image {
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

func processChunk(dec *chunkDecoder, r *region.Region, cx int, cz int) (image.Image, image.Image, error) {
	if !r.ExistSector(cx, cz) {
		return nil, nil, errors.New("sector does not exist")
	}
	sector, err := r.ReadSector(cx, cz)
	if err != nil {
		log.Printf("Error reading sector: %v", err)
		return nil, nil, err
	}
	chunk, err := dec.decode(sector)
	if err != nil {
		log.Printf("Error decoding chunk: %v", err)
		return nil, nil, err
	}
	if chunk.MotionBlocking.Len() == 0 {
		log.Printf("Motion blocking heightmap not found")
		return nil, nil, errors.New("motion blocking heightmap not found")
	}
	heights := unpackHeightmap(chunk.MotionBlocking)
	// Heightmap entries are stored as (worldY - minY + 1). 0 means no block.
	minY := chunk.YPos * 16
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	depthImg := image.NewGray16(image.Rect(0, 0, 16, 16))
	for x := 0; x < 16; x++ {
		for z := 0; z < 16; z++ {
			stored := heights[x][z]
			if stored == 0 {
				continue
			}
			y := stored - 1 + minY
			sectionHeight := yToSection(y)
			for i := range chunk.Sections {
				section := &chunk.Sections[i]
				if section.Y == sectionHeight {
					block, err := getBlockFromSection(section, x, y, z)
					if err != nil {
						log.Printf("Error getting block from section: %v", err)
						continue
					}
					img.SetRGBA(x, z, tintedBlockColor(block, biomeFromSection(section, x, y, z)))
					depthImg.SetGray16(x, z, color.Gray16{Y: uint16(y + 32768)})
					break
				}
			}
		}
	}
	return img, depthImg, nil
}

// getBlockFromSection returns the name of the block at the given position.
func getBlockFromSection(section *chunkSection, x int, y int, z int) ([]byte, error) {
	if section == nil {
		return nil, errors.New("section is nil")
	}
	palette := section.Palette
	if len(palette) == 0 {
		return nil, errors.New("section has no block states")
	}
	// A single palette entry fills the whole section, and the packed data array is omitted.
	if len(palette) == 1 {
		return palette[0], nil
	}
	if section.Data.Len() == 0 {
		return nil, errors.New("section block state data is missing")
	}

	localX := x & 15
	localZ := z & 15
	localY := y & 15
	// Section blocks are stored in YZX order.
	blockIndex := (localY << 8) | (localZ << 4) | localX
	paletteIndex, err := packedPaletteIndex(section.Data, len(palette), blockIndex)
	if err != nil {
		return nil, err
	}
	if paletteIndex < 0 || paletteIndex >= len(palette) {
		return nil, fmt.Errorf("palette index %d out of range for palette of length %d", paletteIndex, len(palette))
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

func packedPaletteIndex(data longArray, paletteLen, index int) (int, error) {
	const blocksPerSection = 16 * 16 * 16
	if index < 0 || index >= blocksPerSection {
		return 0, fmt.Errorf("block index %d out of range", index)
	}
	bitsPerValue := bitsPerBlock(paletteLen)
	if bitsPerValue == 0 {
		return 0, nil
	}
	if data.Len() != packedLongCount(bitsPerValue, blocksPerSection) {
		return 0, fmt.Errorf("packed block data has %d longs, want %d for a %d-entry palette", data.Len(), packedLongCount(bitsPerValue, blocksPerSection), paletteLen)
	}
	valuesPerLong := 64 / bitsPerValue
	longIndex := index / valuesPerLong
	offset := (index % valuesPerLong) * bitsPerValue
	mask := uint64(1<<bitsPerValue) - 1
	return int((data.At(longIndex) >> uint(offset)) & mask), nil
}

// unpackHeightmap unpacks 256 9-bit Y values from a 1.18+ heightmap.
// Indexing: heights[x][z] corresponds to chunk-relative coordinates (0..15).
func unpackHeightmap(data longArray) [16][16]int {
	var heights [16][16]int
	if data.Len() == 0 {
		return heights
	}

	const (
		bitsPerVal = 9
		mask       = (1 << bitsPerVal) - 1 // 0x1FF (511)
		perUint64  = 64 / bitsPerVal       // 7 values per word
	)

	entryIdx := 0
	for w := 0; w < data.Len(); w++ {
		word := data.At(w)
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

func stitchDepthImage(images []image.Image) image.Image {
	img := image.NewGray16(image.Rect(0, 0, 16*32, 16*32))
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
	imgFile, err := os.CreateTemp(filepath.Dir(filename), ".tile-*.png")
	if err != nil {
		log.Printf("Error creating image file: %v", err)
		return err
	}
	defer os.Remove(imgFile.Name())
	if err := png.Encode(imgFile, img); err != nil {
		imgFile.Close()
		return err
	}
	if err := imgFile.Close(); err != nil {
		return err
	}
	return os.Rename(imgFile.Name(), filename)
}

func processRegion(regionFile string) (image.Image, image.Image, error) {
	r, err := region.Open(regionFile)
	if err != nil {
		return nil, nil, err
	}
	defer r.Close()
	var dec chunkDecoder
	images := make([]image.Image, 0)
	depthImages := make([]image.Image, 0)
	for cz := 0; cz < 32; cz++ {
		for cx := 0; cx < 32; cx++ {
			if !r.ExistSector(cx, cz) {
				arrayOfAir := make([][]byte, 16*16)
				images = append(images, saveChunkImage(arrayOfAir))
				depthImages = append(depthImages, nil)
				continue
			}
			img, depthImg, err := processChunk(&dec, r, cx, cz)
			if err != nil {
				log.Printf("Error processing chunk: %v", err)
				arrayOfAir := make([][]byte, 16*16)
				images = append(images, saveChunkImage(arrayOfAir))
				depthImages = append(depthImages, nil)
				continue
			}
			TotalChunksProcessed.Add(1)
			RecentChunksCounter.Add(1)
			images = append(images, img)
			depthImages = append(depthImages, depthImg)
		}
	}
	img := stitchRegionImage(images)
	depthImg := stitchDepthImage(depthImages)
	return img, depthImg, nil
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
	img, depthImg, err := processRegion(fmt.Sprintf("%s/%s", path, regionFile))
	if err != nil {
		log.Printf("Error processing region %s: %v", regionFile, err)
		return err
	}
	if err := saveImage(depthImg, fmt.Sprintf("%s/depth/%s_depth.png", imageCacheDir, parsedRegionName)); err != nil {
		return err
	}
	return saveImage(img, fmt.Sprintf("%s/%s.png", imageCacheDir, parsedRegionName))
}

func processAllRegions(path string) error {
	if err := os.MkdirAll(filepath.Join(imageCacheDir, "depth"), 0755); err != nil {
		return err
	}
	regionFiles := getAllRegionFiles(path)
	sem := make(chan struct{}, runtime.NumCPU())
	wg := sync.WaitGroup{}
	for _, regionFile := range regionFiles {
		sem <- struct{}{}
		wg.Add(1)
		go func(regionFile string) {
			defer wg.Done()
			defer func() { <-sem }()
			parsedRegionName := parseRegionName(regionFile)
			_, colorErr := os.Stat(filepath.Join(imageCacheDir, parsedRegionName+".png"))
			_, depthErr := os.Stat(filepath.Join(imageCacheDir, "depth", parsedRegionName+"_depth.png"))
			if colorErr == nil && depthErr == nil {
				return
			}
			if err := processAndSaveRegion(path, regionFile, parsedRegionName); err != nil {
				log.Printf("Error processing region %s: %v", regionFile, err)
			}
		}(regionFile)
	}
	wg.Wait()
	return nil
}
