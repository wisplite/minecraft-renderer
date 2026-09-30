package main

import (
	_ "embed"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

func doesTileExist(tiles []RegionTile, rx, rz int) bool {
	for _, tile := range tiles {
		if tile.Pos.X == float32(rx*512) && tile.Pos.Y == float32(rz*512) {
			return true
		}
	}
	return false
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

		select {
		case tile := <-session.tiles:
			coords := strings.Split(tile, ".")
			if len(coords) != 4 || coords[0] != "r" || coords[3] != "png" {
				break
			}
			rx, xErr := strconv.Atoi(coords[1])
			rz, zErr := strconv.Atoi(coords[2])
			if xErr != nil || zErr != nil || doesTileExist(tiles, rx, rz) {
				break
			}
			loaded, err := loadTile(filepath.Join(session.cacheDir, tile), rx, rz)
			if err != nil {
				// A depth map may still be generating; the next scan retries it.
				log.Printf("Loading %s: %v", tile, err)
				break
			}
			tiles = append(tiles, loaded)
		default:
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
			if camera.Zoom < 0.05 {
				camera.Zoom = 0.05
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
	tiles     chan string
	done      chan struct{}
}

func startWorldSession(regionDir string) *worldSession {
	s := &worldSession{
		regionDir: regionDir,
		cacheDir:  regionCacheDir(regionDir),
		tiles:     make(chan string),
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
		for {
			select {
			case <-s.done:
				return
			case <-ticker.C:
			}
			for _, file := range getAllImageFiles(s.cacheDir) {
				select {
				case s.tiles <- file:
				case <-s.done:
					return
				}
			}
		}
	}()
	return s
}

func (s *worldSession) stop() {
	close(s.done)
}

func loadTile(path string, rx, rz int) (RegionTile, error) {
	depthPath := filepath.Join(filepath.Dir(path), "depth", fmt.Sprintf("r.%d.%d_depth.png", rx, rz))
	packed, err := readDepthMap(depthPath, 512, 512)
	if err != nil {
		return RegionTile{}, err
	}
	tex := rl.LoadTexture(path)
	if tex.ID == 0 {
		return RegionTile{}, fmt.Errorf("load color texture %s", path)
	}
	if tex.Width != 512 || tex.Height != 512 {
		rl.UnloadTexture(tex)
		return RegionTile{}, fmt.Errorf("color tile must be 512x512")
	}
	depthImage := rl.NewImageFromImage(packed)
	depth := rl.LoadTextureFromImage(depthImage)
	rl.UnloadImage(depthImage)
	if depth.ID == 0 {
		rl.UnloadTexture(tex)
		return RegionTile{}, fmt.Errorf("upload depth texture %s", depthPath)
	}
	// Point sampling preserves both the block colors and packed height bytes.
	rl.SetTextureFilter(tex, rl.FilterPoint)
	rl.SetTextureFilter(depth, rl.FilterPoint)
	rl.SetTextureWrap(depth, rl.WrapClamp)
	return RegionTile{
		Texture: tex,
		Depth:   depth,
		Pos:     rl.NewVector2(float32(rx*512), float32(rz*512)),
	}, nil
}
