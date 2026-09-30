package main

import (
	"archive/zip"
	"encoding/json"
	"flag"
	"fmt"
	"image/color"
	"image/png"
	"log"
	"os"
	"path"
	"sort"
	"strings"
)

var preferredBlockSuffixes = []string{
	"_top",
	"_up",
	"_still",
}

type textureRef string

// UnmarshalJSON accepts both the plain string form and the object form
// ({"sprite": ...}) used by newer model files.
func (t *textureRef) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*t = textureRef(s)
		return nil
	}
	var obj struct {
		Sprite string `json:"sprite"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return err
	}
	*t = textureRef(obj.Sprite)
	return nil
}

type blockModel struct {
	Parent   string                `json:"parent"`
	Textures map[string]textureRef `json:"textures"`
}

type blockState struct {
	Variants  map[string]json.RawMessage `json:"variants"`
	Multipart []struct {
		Apply json.RawMessage `json:"apply"`
	} `json:"multipart"`
}

type modelRef struct {
	Model string `json:"model"`
}

type assets struct {
	files  map[string]*zip.File
	models map[string]map[string]string
}

func newAssets(zipFiles ...*zip.ReadCloser) *assets {
	a := &assets{
		files:  make(map[string]*zip.File),
		models: make(map[string]map[string]string),
	}
	for _, zipFile := range zipFiles {
		for _, entry := range zipFile.File {
			a.files[entry.Name] = entry
		}
	}
	return a
}

func (a *assets) readJSON(name string, v any) error {
	file, ok := a.files[name]
	if !ok {
		return os.ErrNotExist
	}
	rc, err := file.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	return json.NewDecoder(rc).Decode(v)
}

// splitResourceID splits "namespace:path" into its parts, defaulting the
// namespace to "minecraft".
func splitResourceID(id string) (string, string) {
	if ns, p, ok := strings.Cut(id, ":"); ok {
		return ns, p
	}
	return "minecraft", id
}

// blockStateNames returns the namespaced block IDs of every blockstate file,
// mapped to the file that defines them.
func (a *assets) blockStateNames() map[string]string {
	names := make(map[string]string)
	for name := range a.files {
		parts := strings.Split(name, "/")
		if len(parts) < 4 || parts[0] != "assets" || parts[2] != "blockstates" || !strings.HasSuffix(name, ".json") {
			continue
		}
		names[parts[1]+":"+strings.TrimSuffix(strings.Join(parts[3:], "/"), ".json")] = name
	}
	return names
}

func parseModelRefs(raw json.RawMessage) ([]string, error) {
	var one modelRef
	if err := json.Unmarshal(raw, &one); err == nil {
		return []string{one.Model}, nil
	}
	var many []modelRef
	if err := json.Unmarshal(raw, &many); err != nil {
		return nil, err
	}
	refs := make([]string, 0, len(many))
	for _, m := range many {
		refs = append(refs, m.Model)
	}
	return refs, nil
}

// blockStateModels returns every model referenced by any variant or multipart
// case of a blockstate file.
func (a *assets) blockStateModels(stateFile string) ([]string, error) {
	var state blockState
	if err := a.readJSON(stateFile, &state); err != nil {
		return nil, err
	}
	var raws []json.RawMessage
	for _, raw := range state.Variants {
		raws = append(raws, raw)
	}
	for _, part := range state.Multipart {
		raws = append(raws, part.Apply)
	}
	var models []string
	for _, raw := range raws {
		refs, err := parseModelRefs(raw)
		if err != nil {
			return nil, err
		}
		models = append(models, refs...)
	}
	return models, nil
}

// modelTextureVars returns the texture variables of a model merged with those
// inherited from its parents. Builtin parents without a model file end the chain.
func (a *assets) modelTextureVars(id string) map[string]string {
	ns, p := splitResourceID(id)
	key := ns + ":" + p
	if vars, ok := a.models[key]; ok {
		return vars
	}
	a.models[key] = map[string]string{}
	var model blockModel
	if err := a.readJSON(fmt.Sprintf("assets/%s/models/%s.json", ns, p), &model); err != nil {
		if !os.IsNotExist(err) {
			log.Printf("Failed to read model %s: %v", key, err)
		}
		return a.models[key]
	}
	vars := make(map[string]string)
	if model.Parent != "" {
		for k, v := range a.modelTextureVars(model.Parent) {
			vars[k] = v
		}
	}
	for k, v := range model.Textures {
		vars[k] = string(v)
	}
	a.models[key] = vars
	return vars
}

// resolveTexture follows "#variable" references until it reaches a texture ID.
func resolveTexture(vars map[string]string, value string) (string, bool) {
	for range len(vars) + 1 {
		if !strings.HasPrefix(value, "#") {
			return value, value != ""
		}
		next, ok := vars[strings.TrimPrefix(value, "#")]
		if !ok {
			return "", false
		}
		value = next
	}
	return "", false
}

// blockTextures returns the texture files used by any model of a blockstate.
func (a *assets) blockTextures(stateFile string) ([]string, error) {
	models, err := a.blockStateModels(stateFile)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	var textures []string
	for _, model := range models {
		vars := a.modelTextureVars(model)
		for _, value := range vars {
			id, ok := resolveTexture(vars, value)
			if !ok {
				continue
			}
			ns, p := splitResourceID(id)
			texturePath := fmt.Sprintf("assets/%s/textures/%s.png", ns, p)
			if _, ok := a.files[texturePath]; !ok || seen[texturePath] {
				continue
			}
			seen[texturePath] = true
			textures = append(textures, texturePath)
		}
	}
	sort.Strings(textures)
	return textures, nil
}

// pickBlockTexture chooses the texture that best represents a block seen from
// above: the first match in preferredBlockSuffixes, then a texture named after
// the block, then the shortest name.
func pickBlockTexture(blockName string, textures []string) string {
	if len(textures) == 1 {
		return textures[0]
	}
	for _, marker := range preferredBlockSuffixes {
		if best := bestTextureMatch(textures, marker); best != "" {
			return best
		}
	}
	shortest := ""
	for _, texture := range textures {
		if textureStem(texture) == blockName {
			return texture
		}
		if shortest == "" || textureNameLess(texture, shortest) {
			shortest = texture
		}
	}
	return shortest
}

func textureStem(texture string) string {
	return strings.TrimSuffix(path.Base(texture), ".png")
}

func bestTextureMatch(textures []string, marker string) string {
	best := ""
	for _, texture := range textures {
		stem := textureStem(texture)
		exact := strings.HasSuffix(stem, marker)
		if !exact && !strings.Contains(stem, marker+"_") {
			continue
		}
		if best == "" || textureMatchLess(texture, best, marker) {
			best = texture
		}
	}
	return best
}

func textureMatchLess(a, b, marker string) bool {
	aExact := strings.HasSuffix(textureStem(a), marker)
	bExact := strings.HasSuffix(textureStem(b), marker)
	if aExact != bExact {
		return aExact
	}
	return textureNameLess(a, b)
}

func textureNameLess(a, b string) bool {
	an := path.Base(a)
	bn := path.Base(b)
	if len(an) != len(bn) {
		return len(an) < len(bn)
	}
	return an < bn
}

func averageTexture(file *zip.File) (color.RGBA, error) {
	rc, err := file.Open()
	if err != nil {
		return color.RGBA{}, err
	}
	defer rc.Close()
	img, err := png.Decode(rc)
	if err != nil {
		return color.RGBA{}, err
	}
	bounds := img.Bounds()
	var totalR, totalG, totalB, count uint64
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, a := img.At(x, y).RGBA()
			if a < 0x2000 {
				continue
			}
			alphaFactor := float64(0xFFFF) / float64(a)
			totalR += uint64(float64(r) * alphaFactor)
			totalG += uint64(float64(g) * alphaFactor)
			totalB += uint64(float64(b) * alphaFactor)
			count++
		}
	}
	if count == 0 {
		return color.RGBA{A: 0}, nil
	}
	return color.RGBA{
		R: uint8((totalR / count) >> 8),
		G: uint8((totalG / count) >> 8),
		B: uint8((totalB / count) >> 8),
		A: uint8(255),
	}, nil
}

func writeGoColorFile(outPath string, colors map[string]color.RGBA) error {
	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()

	// Sort keys so git diffs stay deterministic
	keys := make([]string, 0, len(colors))
	for k := range colors {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	fmt.Fprintln(f, "// Code generated by gen_colors; DO NOT EDIT.")
	fmt.Fprintln(f, "package main")
	fmt.Fprintln(f)
	fmt.Fprintln(f, "import \"image/color\"")
	fmt.Fprintln(f)
	fmt.Fprintln(f, "var blockColors = map[string]color.RGBA{")

	for _, k := range keys {
		c := colors[k]
		fmt.Fprintf(f, "\t%q: {R: %d, G: %d, B: %d, A: %d},\n", k, c.R, c.G, c.B, c.A)
	}

	fmt.Fprintln(f, "}")
	return nil
}

func appendSpecialColors(colors map[string]color.RGBA) {
	colors["minecraft:air"] = color.RGBA{R: 0, G: 0, B: 0, A: 0}
	colors["minecraft:cave_air"] = color.RGBA{R: 0, G: 0, B: 0, A: 0}
	colors["minecraft:void_air"] = color.RGBA{R: 0, G: 0, B: 0, A: 0}
	colors[""] = color.RGBA{R: 0, G: 0, B: 0, A: 0}
}

func main() {
	var inputs inputPaths
	flag.Var(&inputs, "i", "Input JAR or directory of JARs (recursive); repeat to combine inputs, later inputs override earlier assets")
	var outPath string
	flag.StringVar(&outPath, "o", "block_colors.go", "The output file to write the colors to")
	var biomeOut string
	flag.StringVar(&biomeOut, "biomes", "", "Optional output Go file for biome tints")
	flag.Parse()
	if len(inputs) == 0 {
		flag.PrintDefaults()
		os.Exit(1)
	}
	jars, err := openInputs(inputs)
	if err != nil {
		log.Fatal(err)
	}
	defer closeJars(jars)
	a := newAssets(jars...)
	log.Printf("Loaded %d JARs", len(jars))
	if biomeOut != "" {
		if err := a.writeBiomeColors(biomeOut); err != nil {
			log.Fatal(err)
		}
	}
	colors := a.blockColors()
	err = writeGoColorFile(outPath, colors)
	if err != nil {
		log.Fatalf("Failed to write go color file: %v", err)
	}
}

func (a *assets) blockColors() map[string]color.RGBA {
	colors := make(map[string]color.RGBA)
	averages := make(map[string]color.RGBA)
	states := a.blockStateNames()
	ids := make([]string, 0, len(states))
	for id := range states {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, blockID := range ids {
		stateFile := states[blockID]
		textures, err := a.blockTextures(stateFile)
		if err != nil {
			log.Printf("Skipping blockstate %s: %v", stateFile, err)
			continue
		}
		if len(textures) == 0 {
			log.Printf("No textures found for %s", blockID)
			continue
		}
		_, name := splitResourceID(blockID)
		texture := pickBlockTexture(name, textures)
		avg, ok := averages[texture]
		if !ok {
			avg, err = averageTexture(a.files[texture])
			if err != nil {
				log.Printf("Skipping %s: texture %s: %v", blockID, texture, err)
				continue
			}
			averages[texture] = avg
		}
		colors[blockID] = avg
	}
	log.Printf("Generated colors for %d of %d blockstates (%d skipped)", len(colors), len(states), len(states)-len(colors))
	appendSpecialColors(colors)
	return colors
}
