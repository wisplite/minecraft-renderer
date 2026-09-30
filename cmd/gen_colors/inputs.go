package main

import (
	"archive/zip"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type inputPaths []string

func (p *inputPaths) String() string         { return strings.Join(*p, ", ") }
func (p *inputPaths) Set(value string) error { *p = append(*p, value); return nil }

func closeJars(jars []*zip.ReadCloser) {
	for _, jar := range jars {
		jar.Close()
	}
}

// openInputs preserves input order and visits directories in lexical order.
// Keep all archives open so models can reference assets from other mods.
func openInputs(inputs []string) ([]*zip.ReadCloser, error) {
	var paths []string
	for _, input := range inputs {
		info, err := os.Stat(input)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			paths = append(paths, input)
			continue
		}
		err = filepath.WalkDir(input, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && strings.EqualFold(filepath.Ext(p), ".jar") {
				paths = append(paths, p)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no JAR files found in inputs")
	}
	var jars []*zip.ReadCloser
	for _, p := range paths {
		jar, err := zip.OpenReader(p)
		if err != nil {
			closeJars(jars)
			return nil, fmt.Errorf("open JAR %s: %w", p, err)
		}
		jars = append(jars, jar)
	}
	return jars, nil
}
