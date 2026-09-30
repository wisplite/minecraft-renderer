package main

import (
	"archive/zip"
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeTestJar(t *testing.T, name string, files map[string][]byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	for name, data := range files {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func testPNG(t *testing.T, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for y := range 2 {
		for x := range 2 {
			img.SetRGBA(x, y, c)
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestDirectoryAssets(t *testing.T) {
	dir := t.TempDir()
	red, green := color.RGBA{R: 255, A: 255}, color.RGBA{G: 255, A: 255}
	writeTestJar(t, filepath.Join(dir, "a.jar"), map[string][]byte{
		"assets/mod/blockstates/nested/block.json": []byte(`{"variants":{"": [{"model":"mod:block/child"}]}}`),
		"assets/mod/models/block/child.json":       []byte(`{"parent":"library:block/base","textures":{"surface":"other:block/shared"}}`),
		"assets/mod/blockstates/multipart.json":    []byte(`{"multipart":[{"apply":{"model":"mod:block/child"}}]}`),
		"assets/mod/blockstates/broken.json":       []byte(`{`),
		"assets/mod/blockstates/bad_texture.json":  []byte(`{"variants":{"":{"model":"mod:block/bad"}}}`),
		"assets/mod/models/block/bad.json":         []byte(`{"textures":{"all":"mod:block/bad"}}`),
		"assets/mod/textures/block/bad.png":        []byte("not a PNG"),
		"assets/other/textures/block/shared.png":   testPNG(t, red),
	})
	writeTestJar(t, filepath.Join(dir, "nested", "b.JAR"), map[string][]byte{
		"assets/library/models/block/base.json":  []byte(`{"textures":{"all":"#surface"}}`),
		"assets/other/textures/block/shared.png": testPNG(t, green),
	})
	if err := os.WriteFile(filepath.Join(dir, "ignored.txt"), []byte("not a JAR"), 0644); err != nil {
		t.Fatal(err)
	}
	jars, err := openInputs([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	defer closeJars(jars)
	if len(jars) != 2 {
		t.Fatalf("loaded %d jars, want 2", len(jars))
	}
	a := newAssets(jars...)
	vars := a.modelTextureVars("mod:block/child")
	if got, ok := resolveTexture(vars, vars["all"]); !ok || got != "other:block/shared" {
		t.Fatalf("inherited texture = %q, %v", got, ok)
	}
	colors := a.blockColors()
	for _, id := range []string{"mod:nested/block", "mod:multipart"} {
		if colors[id] != green {
			t.Errorf("%s = %v, want %v", id, colors[id], green)
		}
	}
	for _, id := range []string{"mod:broken", "mod:bad_texture"} {
		if _, ok := colors[id]; ok {
			t.Errorf("included invalid block %s", id)
		}
	}
	if !reflect.DeepEqual(colors, a.blockColors()) {
		t.Fatal("generation not deterministic")
	}
	// A repeated explicit input has higher priority than the directory.
	override, err := openInputs([]string{dir, filepath.Join(dir, "a.jar")})
	if err != nil {
		t.Fatal(err)
	}
	defer closeJars(override)
	if got := newAssets(override...).blockColors()["mod:nested/block"]; got != red {
		t.Fatalf("override color = %v", got)
	}
}

func TestInputErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := openInputs([]string{dir}); err == nil {
		t.Fatal("empty directory accepted")
	}
	if _, err := openInputs([]string{filepath.Join(dir, "missing.jar")}); err == nil {
		t.Fatal("missing input accepted")
	}
	bad := filepath.Join(dir, "bad.jar")
	if err := os.WriteFile(bad, []byte("bad zip"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := openInputs([]string{dir}); err == nil {
		t.Fatal("invalid JAR accepted")
	}
}
