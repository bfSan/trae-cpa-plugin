//go:build !unix

package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func readAccountCredential(dir, name string) ([]byte, error) {
	if !filepath.IsAbs(dir) || name == "" || filepath.Base(name) != name {
		return nil, fmt.Errorf("invalid credential path")
	}
	path := filepath.Join(dir, name)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("physical credential unavailable")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("physical credential unavailable")
	}
	return raw, nil
}

func writeAccountCredential(dir, name string, raw []byte) error {
	if !filepath.IsAbs(dir) || name == "" || filepath.Base(name) != name {
		return fmt.Errorf("invalid credential path")
	}
	return atomicWriteJSON(filepath.Join(dir, name), raw)
}
