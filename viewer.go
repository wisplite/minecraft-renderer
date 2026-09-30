package main

import (
	_ "embed"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	rl "github.com/gen2brain/raylib-go/raylib"
)

//go:embed shaders/hillshade.fs
var hillshadeFragment string

type RegionTile struct {
	Depth   rl.Texture2D
	Texture rl.Texture2D
	Pos     rl.Vector2 // World space (rx*512, rz*512)
}

const tileSize = 512

// tileUploadBudget caps how long each frame spends uploading textures.
const tileUploadBudget = 16 * time.Millisecond

// decodedTile holds a tile's pixels, decoded off the render thread and ready
// for GPU upload.
type decodedTile struct {
	rx, rz int
	color  []byte // Non-premultiplied RGBA
	depth  *image.RGBA
}

func parseTileName(name string) (rx, rz int, ok bool) {
	coords := strings.Split(name, ".")
	if len(coords) != 4 || coords[0] != "r" || coords[3] != "png" {
		return 0, 0, false
	}
	rx, xErr := strconv.Atoi(coords[1])
	rz, zErr := strconv.Atoi(coords[2])
	return rx, rz, xErr == nil && zErr == nil
}

func main() {
	rl.SetConfigFlags(rl.FlagWindowResizable | rl.FlagVsyncHint)
	rl.InitWindow(1280, 720, "Minecraft World Viewer")
	if !rl.IsWindowReady() {
		log.Print("Unable to initialize viewer window")
		return
	}
	defer rl.CloseWindow()
	rl.SetTargetFPS(60)

	shader := rl.LoadShaderFromMemory("", hillshadeFragment)
	defer rl.UnloadShader(shader)
	depthLocation := rl.GetShaderLocation(shader, "depthMap")
	if depthLocation < 0 {
		log.Print("Hillshade shader unavailable; displaying color tiles")
	}
	hillshading := depthLocation >= 0

	regionDir := "test/region"
	if len(os.Args) > 1 {
		regionDir = resolveRegionDir(os.Args[1])
	}
	session := startWorldSession(regionDir)
	defer func() { session.stop() }()

	// Set up 2D camera
	newCamera := func() rl.Camera2D {
		return rl.Camera2D{
			Target: rl.NewVector2(0, 0),
			Offset: rl.NewVector2(float32(rl.GetScreenWidth())/2, float32(rl.GetScreenHeight())/2),
			Zoom:   1.0,
		}
	}
	camera := newCamera()

	var tiles []RegionTile
	unloadTiles := func() {
		for _, t := range tiles {
			rl.UnloadTexture(t.Texture)
			rl.UnloadTexture(t.Depth)
		}
		tiles = nil
	}
	defer func() { unloadTiles() }()

	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				CurrentChunksPerSecond.Store(RecentChunksCounter.Swap(0))
			}
		}
	}()

	type pickResult struct {
		path string
		err  error
	}
	pickerResults := make(chan pickResult, 1)
	pickerOpen := false

	for !rl.WindowShouldClose() {
		select {
		case result := <-pickerResults:
			pickerOpen = false
			if result.err != nil {
				if result.err != errPickerCancelled {
					log.Printf("Folder picker: %v", result.err)
				}
				break
			}
			session.stop()
			unloadTiles()
			TotalChunksProcessed.Store(0)
			camera = newCamera()
			session = startWorldSession(result.path)
		default:
		}
		if rl.IsKeyPressed(rl.KeyO) && !pickerOpen {
			pickerOpen = true
			start := session.regionDir
			go func() {
				path, err := pickFolder(start)
				pickerResults <- pickResult{path, err}
			}()
		}

		uploadStart := time.Now()
	upload:
		for time.Since(uploadStart) < tileUploadBudget {
			select {
			case decoded := <-session.tiles:
				tile, err := uploadTile(decoded)
				if err != nil {
					log.Printf("Uploading r.%d.%d: %v", decoded.rx, decoded.rz, err)
					continue
				}
				tiles = append(tiles, tile)
			default:
				break upload
			}
		}
		if rl.IsKeyPressed(rl.KeyH) && depthLocation >= 0 {
			hillshading = !hillshading
		}
		// --- Controls: Pan (Right Mouse Drag) ---
		if rl.IsMouseButtonDown(rl.MouseRightButton) {
			delta := rl.GetMouseDelta()
			// Divide by zoom so drag speed feels 1:1 at any zoom level
			delta = rl.Vector2Scale(delta, -1.0/camera.Zoom)
			camera.Target = rl.Vector2Add(camera.Target, delta)
		}

		// --- Controls: Zoom (Mouse Wheel anchored to cursor) ---
		wheel := rl.GetMouseWheelMove()
		if wheel != 0 {
			mouseWorldPos := rl.GetScreenToWorld2D(rl.GetMousePosition(), camera)
			camera.Offset = rl.GetMousePosition()
			camera.Target = mouseWorldPos

			camera.Zoom += wheel * 0.125 * camera.Zoom
			if camera.Zoom < 0.01 {
				camera.Zoom = 0.01
			} else if camera.Zoom > 16.0 {
				camera.Zoom = 16.0
			}
		}

		// --- Render ---
		rl.BeginDrawing()
		rl.ClearBackground(rl.NewColor(20, 20, 20, 255))

		rl.BeginMode2D(camera)

		// Draw world origin marker
		rl.DrawCircle(0, 0, 4/camera.Zoom, rl.Red)

		// Draw all loaded tiles
		for _, tile := range tiles {
			if hillshading {
				rl.BeginShaderMode(shader)
				rl.SetShaderValueTexture(shader, depthLocation, tile.Depth)
			}
			rl.DrawTextureRec(
				tile.Texture,
				rl.NewRectangle(0, 0, float32(tile.Texture.Width), float32(tile.Texture.Height)),
				tile.Pos,
				rl.White,
			)
			if hillshading {
				// EndShaderMode flushes this tile before its depth sampler changes.
				rl.EndShaderMode()
			}
			// Optional: draw region border
			//rl.DrawRectangleLines(int32(tile.Pos.X), int32(tile.Pos.Y), 512, 512, rl.Fade(rl.White, 0.2))
		}

		rl.EndMode2D()

		// Overlay stats
		rl.DrawText(fmt.Sprintf("Zoom: %.2fx | Cam: (%.0f, %.0f)", camera.Zoom, camera.Target.X, camera.Target.Y), 10, 10, 20, rl.RayWhite)
		rl.DrawText(fmt.Sprintf("Chunks/s: %d | Total chunks: %d", CurrentChunksPerSecond.Load(), TotalChunksProcessed.Load()), 10, 35, 20, rl.RayWhite)
		rl.DrawFPS(10, 60)
		rl.DrawText(fmt.Sprintf("[H] Hillshading: %t", hillshading), 10, 85, 20, rl.RayWhite)
		{
			// Extract the last two folders from the path
			regionParts := strings.Split(filepath.Clean(session.regionDir), string(os.PathSeparator))
			displayPath := session.regionDir
			if len(regionParts) >= 2 {
				displayPath = filepath.Join(regionParts[len(regionParts)-2], regionParts[len(regionParts)-1])
			} else if len(regionParts) == 1 {
				displayPath = regionParts[0]
			}
			rl.DrawText(fmt.Sprintf("[O] Open region folder: %s", displayPath), 10, 110, 20, rl.RayWhite)
		}

		rl.EndDrawing()
	}
}

// worldSession renders one region folder and feeds its cached tile names to the viewer.
type worldSession struct {
	regionDir string
	cacheDir  string
	tiles     chan decodedTile
	done      chan struct{}
}

func startWorldSession(regionDir string) *worldSession {
	s := &worldSession{
		regionDir: regionDir,
		cacheDir:  regionCacheDir(regionDir),
		tiles:     make(chan decodedTile, runtime.NumCPU()),
		done:      make(chan struct{}),
	}
	go func() {
		if err := processAllRegions(s.regionDir, s.cacheDir, s.done); err != nil {
			log.Printf("Error processing regions: %v", err)
		}
	}()
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		sent := make(map[string]bool)
		for {
			s.decodeNewTiles(sent)
			select {
			case <-s.done:
				return
			case <-ticker.C:
			}
		}
	}()
	return s
}

func (s *worldSession) stop() {
	close(s.done)
}

// decodeNewTiles decodes every cached tile not yet in sent, in parallel, and
// hands them to the viewer. Tiles that fail to decode are retried next scan.
func (s *worldSession) decodeNewTiles(sent map[string]bool) {
	type job struct {
		name   string
		rx, rz int
	}
	var pending []job
	for _, name := range getAllImageFiles(s.cacheDir) {
		if sent[name] {
			continue
		}
		if rx, rz, ok := parseTileName(name); ok {
			pending = append(pending, job{name, rx, rz})
		}
	}
	if len(pending) == 0 {
		return
	}

	jobs := make(chan job)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range min(runtime.NumCPU(), len(pending)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				tile, err := decodeTile(s.cacheDir, j.rx, j.rz)
				if err != nil {
					log.Printf("Loading %s: %v", j.name, err)
					continue
				}
				select {
				case s.tiles <- tile:
					mu.Lock()
					sent[j.name] = true
					mu.Unlock()
				case <-s.done:
					return
				}
			}
		}()
	}
feed:
	for _, j := range pending {
		select {
		case jobs <- j:
		case <-s.done:
			break feed
		}
	}
	close(jobs)
	wg.Wait()
}

func decodeTile(cacheDir string, rx, rz int) (decodedTile, error) {
	depthPath := filepath.Join(cacheDir, "depth", fmt.Sprintf("r.%d.%d_depth.png", rx, rz))
	depth, err := readDepthMap(depthPath, tileSize, tileSize)
	if err != nil {
		return decodedTile{}, err
	}
	colorPath := filepath.Join(cacheDir, fmt.Sprintf("r.%d.%d.png", rx, rz))
	f, err := os.Open(colorPath)
	if err != nil {
		return decodedTile{}, err
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return decodedTile{}, fmt.Errorf("decode color tile: %w", err)
	}
	if img.Bounds().Dx() != tileSize || img.Bounds().Dy() != tileSize {
		return decodedTile{}, fmt.Errorf("color tile must be %dx%d", tileSize, tileSize)
	}
	return decodedTile{rx: rx, rz: rz, color: nrgbaPixels(img), depth: depth}, nil
}

// nrgbaPixels returns img as tightly packed, non-premultiplied RGBA bytes.
func nrgbaPixels(img image.Image) []byte {
	b := img.Bounds()
	switch m := img.(type) {
	case *image.NRGBA:
		if m.Stride == b.Dx()*4 {
			return m.Pix
		}
	case *image.RGBA:
		if m.Stride == b.Dx()*4 && m.Opaque() {
			return m.Pix
		}
	}
	dst := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), img, b.Min, draw.Src)
	return dst.Pix
}

// uploadTile creates the GPU textures for a decoded tile. It must run on the
// render thread.
func uploadTile(t decodedTile) (RegionTile, error) {
	tex := rl.LoadTextureFromImage(rl.NewImage(t.color, tileSize, tileSize, 1, rl.UncompressedR8g8b8a8))
	if tex.ID == 0 {
		return RegionTile{}, fmt.Errorf("upload color texture")
	}
	depth := rl.LoadTextureFromImage(rl.NewImage(t.depth.Pix, tileSize, tileSize, 1, rl.UncompressedR8g8b8a8))
	if depth.ID == 0 {
		rl.UnloadTexture(tex)
		return RegionTile{}, fmt.Errorf("upload depth texture")
	}
	rx, rz := t.rx, t.rz
	// Point sampling preserves both the block colors and packed height bytes.
	rl.SetTextureFilter(tex, rl.FilterPoint)
	rl.SetTextureFilter(depth, rl.FilterPoint)
	rl.SetTextureWrap(depth, rl.WrapClamp)
	return RegionTile{
		Texture: tex,
		Depth:   depth,
		Pos:     rl.NewVector2(float32(rx*tileSize), float32(rz*tileSize)),
	}, nil
}
