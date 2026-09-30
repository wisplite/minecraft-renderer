package main

import "image/color"

var blockColors = map[string]color.Color{
	"minecraft:air":         color.RGBA{R: 0, G: 0, B: 0, A: 0},
	"minecraft:stone":       color.RGBA{R: 128, G: 128, B: 128, A: 255},
	"minecraft:dirt":        color.RGBA{R: 102, G: 51, B: 0, A: 255},
	"minecraft:grass_block": color.RGBA{R: 0, G: 255, B: 0, A: 255},
	"minecraft:water":       color.RGBA{R: 0, G: 0, B: 255, A: 255},
}
