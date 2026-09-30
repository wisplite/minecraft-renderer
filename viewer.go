package main

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	rl "github.com/gen2brain/raylib-go/raylib"
)

type RegionTile struct {
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
	defer rl.CloseWindow()
	rl.SetTargetFPS(60)

	go func() {
		err := processAllRegions("test/region")
		if err != nil {
			log.Fatalf("Error processing regions: %v", err)
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
		}
	}()

	tileChannel := make(chan string)
	defer close(tileChannel)

	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			rate := RecentChunksCounter.Swap(0)
			CurrentChunksPerSecond.Store(rate)
			imageFiles := getAllImageFiles("images")
			for _, file := range imageFiles {
				tileChannel <- file
			}
		}
	}()

	for !rl.WindowShouldClose() {
		select {
		case tile := <-tileChannel:
			coords := strings.Split(tile, ".")
			rx, err := strconv.Atoi(coords[1])
			if err != nil {
				log.Fatalf("Error parsing rx: %v", err)
			}
			rz, err := strconv.Atoi(coords[2])
			if err != nil {
				log.Fatalf("Error parsing rz: %v", err)
			}
			if doesTileExist(tiles, rx, rz) {
				continue
			}
			tiles = append(tiles, loadTile(fmt.Sprintf("images/%s", tile), rx, rz))
		default:
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
			rl.DrawTextureRec(
				tile.Texture,
				rl.NewRectangle(0, 0, float32(tile.Texture.Width), float32(tile.Texture.Height)),
				tile.Pos,
				rl.White,
			)
			// Optional: draw region border
			rl.DrawRectangleLines(int32(tile.Pos.X), int32(tile.Pos.Y), 512, 512, rl.Fade(rl.White, 0.2))
		}

		rl.EndMode2D()

		// Overlay stats
		rl.DrawText(fmt.Sprintf("Zoom: %.2fx | Cam: (%.0f, %.0f)", camera.Zoom, camera.Target.X, camera.Target.Y), 10, 10, 20, rl.RayWhite)
		rl.DrawText(fmt.Sprintf("Chunks/s: %d | Total chunks: %d", CurrentChunksPerSecond.Load(), TotalChunksProcessed.Load()), 10, 35, 20, rl.RayWhite)
		rl.DrawFPS(10, 60)

		rl.EndDrawing()
	}
}

func loadTile(path string, rx, rz int) RegionTile {
	tex := rl.LoadTexture(path)
	// Point filtering keeps Minecraft pixels crisp when zooming in
	rl.SetTextureFilter(tex, rl.FilterPoint)

	return RegionTile{
		Texture: tex,
		Pos:     rl.NewVector2(float32(rx*512), float32(rz*512)),
	}
}
