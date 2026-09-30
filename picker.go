package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var errPickerCancelled = errors.New("folder selection cancelled")

// pickFolder opens a native folder dialog starting at start.
func pickFolder(start string) (string, error) {
	if abs, err := filepath.Abs(start); err == nil {
		start = abs
	}
	var cmd *exec.Cmd
	if path, err := exec.LookPath("zenity"); err == nil {
		// zenity only opens inside the directory when the filename ends with a separator.
		cmd = exec.Command(path, "--file-selection", "--directory",
			"--title=Choose a region folder", "--filename="+start+string(filepath.Separator))
	} else if path, err := exec.LookPath("kdialog"); err == nil {
		cmd = exec.Command(path, "--getexistingdirectory", start, "--title", "Choose a region folder")
	} else {
		return "", errors.New("no folder dialog available; install zenity or kdialog")
	}
	out, err := cmd.Output()
	if err != nil {
		// Both tools exit with status 1 when the user cancels.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return "", errPickerCancelled
		}
		return "", err
	}
	return resolveRegionDir(strings.TrimRight(string(out), "\r\n")), nil
}

// resolveRegionDir accepts either a region folder or a world folder containing one.
func resolveRegionDir(dir string) string {
	if matches, _ := filepath.Glob(filepath.Join(dir, "*.mca")); len(matches) > 0 {
		return dir
	}
	region := filepath.Join(dir, "region")
	if info, err := os.Stat(region); err == nil && info.IsDir() {
		return region
	}
	return dir
}
