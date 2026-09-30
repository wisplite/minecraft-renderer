package main

import (
	_ "embed"
	"fmt"
	"log"
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

	go func() {
		err := processAllRegions("test/region")
		if err != nil {
			log.Printf("Error processing regions: %v", err)
		}
	}()

	// Set up 2D camera
	camera := rl.Camera2D{
		Target: rl.NewVector2(0, 0),
		Offset: rl.NewVector2(1280/2, 720/2),
		Zoom:   1.0,
	}

	var tiles []RegionTile

	defer func() {
		for _, t := range tiles {
			rl.UnloadTexture(t.Texture)
			rl.UnloadTexture(t.Depth)
		}
	}()

	tileChannel := make(chan string)
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
			}
			rate := RecentChunksCounter.Swap(0)
			CurrentChunksPerSecond.Store(rate)
			imageFiles := getAllImageFiles(imageCacheDir)
			for _, file := range imageFiles {
				select {
				case tileChannel <- file:
				case <-done:
					return
				}
			}
		}
	}()

	for !rl.WindowShouldClose() {
		select {
		case tile := <-tileChannel:
			coords := strings.Split(tile, ".")
			if len(coords) != 4 || coords[0] != "r" || coords[3] != "png" {
				break
			}
			rx, xErr := strconv.Atoi(coords[1])
			rz, zErr := strconv.Atoi(coords[2])
			if xErr != nil || zErr != nil || doesTileExist(tiles, rx, rz) {
				break
			}
			loaded, err := loadTile(filepath.Join(imageCacheDir, tile), rx, rz)
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

		rl.EndDrawing()
	}
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
