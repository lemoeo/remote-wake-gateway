//go:build !windows

package storage

import (
	"os"
	"path/filepath"
)

func protectDirectory(path string) error { return nil }
func protectFile(f *os.File) error       { return f.Chmod(0600) }

func replaceFile(source, dest string) error {
	if err := os.Rename(source, dest); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(dest))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
