package main

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"

	"github.com/Tnze/go-mc/nbt"
	"github.com/Tnze/go-mc/save"
	"github.com/Tnze/go-mc/save/region"
)

type refChunk struct {
	Heightmaps map[string][]uint64 `nbt:"Heightmaps"`
	Sections   []refSection        `nbt:"sections"`
	YPos       int                 `nbt:"yPos"`
}

type refSection struct {
	Y           int                                    `nbt:"Y"`
	BlockStates save.PaletteContainer[save.BlockState] `nbt:"block_states"`
}

func inflateSector(t testing.TB, sector []byte) []byte {
	t.Helper()
	zr, err := zlib.NewReader(bytes.NewReader(sector[1:]))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func compareChunk(t *testing.T, want *refChunk, got *chunkData) {
	t.Helper()
	if got.YPos != want.YPos {
		t.Fatalf("yPos = %d, want %d", got.YPos, want.YPos)
	}
	wantMB := want.Heightmaps["MOTION_BLOCKING"]
	if got.MotionBlocking.Len() != len(wantMB) {
		t.Fatalf("MOTION_BLOCKING has %d longs, want %d", got.MotionBlocking.Len(), len(wantMB))
	}
	for i, v := range wantMB {
		if got.MotionBlocking.At(i) != v {
			t.Fatalf("MOTION_BLOCKING[%d] = %d, want %d", i, got.MotionBlocking.At(i), v)
		}
	}
	if len(got.Sections) != len(want.Sections) {
		t.Fatalf("got %d sections, want %d", len(got.Sections), len(want.Sections))
	}
	for i, ws := range want.Sections {
		gs := got.Sections[i]
		if gs.Y != ws.Y {
			t.Fatalf("section %d: Y = %d, want %d", i, gs.Y, ws.Y)
		}
		if len(gs.Palette) != len(ws.BlockStates.Palette) {
			t.Fatalf("section %d: palette has %d entries, want %d", i, len(gs.Palette), len(ws.BlockStates.Palette))
		}
		for j, b := range ws.BlockStates.Palette {
			if string(gs.Palette[j]) != b.Name {
				t.Fatalf("section %d: palette[%d] = %q, want %q", i, j, gs.Palette[j], b.Name)
			}
		}
		if gs.Data.Len() != len(ws.BlockStates.Data) {
			t.Fatalf("section %d: data has %d longs, want %d", i, gs.Data.Len(), len(ws.BlockStates.Data))
		}
		for j, v := range ws.BlockStates.Data {
			if gs.Data.At(j) != v {
				t.Fatalf("section %d: data[%d] = %d, want %d", i, j, gs.Data.At(j), v)
			}
		}
	}
}

func TestChunkDecoderMatchesGoMC(t *testing.T) {
	files, _ := filepath.Glob("test/region/*.mca")
	if len(files) == 0 {
		t.Skip("no region files in test/region")
	}
	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			t.Parallel()
			if st, err := os.Stat(file); err != nil || st.Size() == 0 {
				t.Skip("empty region file")
			}
			r, err := region.Open(file)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			var dec chunkDecoder
			for cz := 0; cz < 32; cz++ {
				for cx := 0; cx < 32; cx++ {
					if !r.ExistSector(cx, cz) {
						continue
					}
					sector, err := r.ReadSector(cx, cz)
					if err != nil {
						continue
					}
					var want refChunk
					_, wantErr := nbt.NewDecoder(bytes.NewReader(inflateSector(t, sector))).Decode(&want)
					got, err := dec.decode(sector)
					if (err != nil) != (wantErr != nil) {
						t.Fatalf("chunk %d,%d: err = %v, go-mc err = %v", cx, cz, err, wantErr)
					}
					if err == nil {
						compareChunk(t, &want, got)
					}
				}
			}
		})
	}
}

func firstSector(t testing.TB) []byte {
	t.Helper()
	files, _ := filepath.Glob("test/region/*.mca")
	for _, file := range files {
		r, err := region.Open(file)
		if err != nil {
			continue
		}
		defer r.Close()
		for i := 0; i < 32*32; i++ {
			if !r.ExistSector(i%32, i/32) {
				continue
			}
			if sector, err := r.ReadSector(i%32, i/32); err == nil && len(sector) > 0 && sector[0] == 2 {
				return sector
			}
		}
	}
	t.Skip("no zlib chunk found in test/region")
	return nil
}

func TestChunkDecoderCompressionTypes(t *testing.T) {
	zlibSector := firstSector(t)
	raw := inflateSector(t, zlibSector)

	var gz bytes.Buffer
	gz.WriteByte(1)
	gw := gzip.NewWriter(&gz)
	gw.Write(raw)
	gw.Close()

	var want refChunk
	if _, err := nbt.NewDecoder(bytes.NewReader(raw)).Decode(&want); err != nil {
		t.Fatal(err)
	}

	var dec chunkDecoder
	for name, sector := range map[string][]byte{
		"zlib":         zlibSector,
		"gzip":         gz.Bytes(),
		"uncompressed": append([]byte{3}, raw...),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := dec.decode(sector)
			if err != nil {
				t.Fatal(err)
			}
			compareChunk(t, &want, got)
		})
	}
}

func BenchmarkProcessRegion(b *testing.B) {
	const file = "test/region/r.3.2.mca"
	if _, err := os.Stat(file); err != nil {
		b.Skip(err)
	}
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := processRegion(file); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkChunkDecode(b *testing.B) {
	sector := firstSector(b)
	var dec chunkDecoder
	b.ReportAllocs()
	for b.Loop() {
		if _, err := dec.decode(sector); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkChunkDecodeGoMC(b *testing.B) {
	sector := firstSector(b)
	b.ReportAllocs()
	for b.Loop() {
		zr, err := zlib.NewReader(bytes.NewReader(sector[1:]))
		if err != nil {
			b.Fatal(err)
		}
		var c refChunk
		if _, err := nbt.NewDecoder(zr).Decode(&c); err != nil {
			b.Fatal(err)
		}
	}
}

func FuzzParseChunk(f *testing.F) {
	type seedBlock struct {
		Name       string
		Properties map[string]string
	}
	type seedSection struct {
		Y           int8 `nbt:"Y"`
		BlockStates struct {
			Palette []seedBlock `nbt:"palette"`
			Data    []int64     `nbt:"data"`
		} `nbt:"block_states"`
	}
	seed := struct {
		Heightmaps map[string][]int64 `nbt:"Heightmaps"`
		Sections   []seedSection      `nbt:"sections"`
		YPos       int32              `nbt:"yPos"`
	}{
		Heightmaps: map[string][]int64{"MOTION_BLOCKING": {1, 2, 3}},
		Sections:   make([]seedSection, 1),
		YPos:       -4,
	}
	seed.Sections[0].BlockStates.Palette = []seedBlock{
		{Name: "minecraft:stone"},
		{Name: "minecraft:water", Properties: map[string]string{"level": "0"}},
	}
	seed.Sections[0].BlockStates.Data = []int64{0x1111, 0x2222}
	b, err := nbt.Marshal(seed)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(b)
	f.Add([]byte{tagCompound, 0, 0, tagEnd})
	f.Fuzz(func(t *testing.T, data []byte) {
		var c chunkData
		parseChunk(&nbtReader{buf: data}, &c)
	})
}
