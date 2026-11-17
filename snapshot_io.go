package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func validateSnapshotPaths(functions []string, label, outDir string, overwrite bool) error {
	seen := make(map[string]string, len(functions))
	for _, function := range functions {
		path := filepath.Join(outDir, snapshotFileName(function, label))
		// Be portable to case-insensitive filesystems, even when planning on Linux.
		key := strings.ToLower(path)
		if previous, exists := seen[key]; exists {
			return fmt.Errorf("snapshot filename collision between %q and %q at %s", previous, function, path)
		}
		seen[key] = function
		if err := validateSnapshotDestination(path, overwrite); err != nil {
			return err
		}
	}
	return nil
}

func validateSnapshotDestination(path string, overwrite bool) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect snapshot destination: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("snapshot destination %s must be a regular file, not a symlink or directory", path)
	}
	if !overwrite {
		return fmt.Errorf("snapshot already exists at %s; use --overwrite to replace it: %w", path, os.ErrExist)
	}
	return nil
}

type snapshotTempFile interface {
	io.Writer
	Name() string
	Sync() error
	Close() error
	Chmod(os.FileMode) error
}

type snapshotFileOps struct {
	createTemp func(string, string) (snapshotTempFile, error)
	rename     func(string, string) error
	link       func(string, string) error
	remove     func(string) error
	syncDir    func(string) error
}

func defaultSnapshotFileOps() snapshotFileOps {
	return snapshotFileOps{
		createTemp: func(dir, pattern string) (snapshotTempFile, error) { return os.CreateTemp(dir, pattern) },
		rename:     os.Rename, link: os.Link, remove: os.Remove, syncDir: syncDirectory,
	}
}

func writeSnapshotOutputFile(path string, data []byte, overwrite bool) error {
	return publishSnapshotFile(path, data, overwrite, 0644, defaultSnapshotFileOps())
}

// Stage on the destination filesystem. Hard-link publication provides atomic
// create-if-absent; rename provides atomic replacement without following symlinks.
func publishSnapshotFile(path string, data []byte, overwrite bool, mode os.FileMode, ops snapshotFileOps) error {
	if err := validateSnapshotDestination(path, overwrite); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		mode &= info.Mode().Perm() // Replacement must not broaden existing permissions.
	}
	dir := filepath.Dir(path)
	file, err := ops.createTemp(dir, ".lambda-snapshot-*")
	if err != nil {
		return fmt.Errorf("create staged snapshot: %w", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
		_ = ops.remove(file.Name())
	}()
	n, err := file.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return fmt.Errorf("write staged snapshot: %w", err)
	}
	if err := file.Chmod(mode); err != nil {
		return fmt.Errorf("set snapshot permissions: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("flush staged snapshot: %w", err)
	}
	err = file.Close()
	closed = true
	if err != nil {
		return fmt.Errorf("close staged snapshot: %w", err)
	}
	if err := validateSnapshotDestination(path, overwrite); err != nil {
		return err
	}
	if overwrite {
		err = ops.rename(file.Name(), path)
	} else {
		err = ops.link(file.Name(), path)
	}
	if err != nil {
		return fmt.Errorf("publish snapshot: %w", err)
	}
	if !overwrite {
		if err := ops.remove(file.Name()); err != nil {
			return fmt.Errorf("snapshot published, but temporary-file cleanup failed: %w", err)
		}
	}
	if err := ops.syncDir(dir); err != nil {
		return fmt.Errorf("snapshot published, but directory flush failed: %w", err)
	}
	return nil
}
