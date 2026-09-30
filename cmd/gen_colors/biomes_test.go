package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBiomeColorsFromClientJar(t *testing.T) {
	jar, err := zip.OpenReader("../../minecraft-1.21.1-client.jar")
	if os.IsNotExist(err) {
		t.Skip("Minecraft 1.21.1 client jar not available")
	}
	if err != nil {
		t.Fatal(err)
	}
	defer jar.Close()
	out := filepath.Join(t.TempDir(), "biomes.go")
	a := newAssets(jar)
	if err := a.writeBiomeColors(out); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"minecraft:plains": {Grass: 0x91bd59, Foliage: 0x77ab2f, Water: 0x3f76e4}`,
		`"minecraft:swamp": {Grass: 0x6a7039, Foliage: 0x6a7039, Water: 0x617b64}`,
		`"minecraft:dark_forest": {Grass: 0x507a32, Foliage: 0x59ae30, Water: 0x3f76e4}`,
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("missing expected biome: %s", want)
		}
	}
	if err := a.writeBiomeColors(out); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(second) != string(data) {
		t.Fatal("generation is not deterministic")
	}
}
