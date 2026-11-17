package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type failingSnapshotFile struct {
	*os.File
	failure string
}

func (f *failingSnapshotFile) Write(data []byte) (int, error) {
	if f.failure == "write" {
		return 0, errors.New("injected write failure")
	}
	if f.failure == "short write" {
		return len(data) / 2, nil
	}
	return f.File.Write(data)
}
func (f *failingSnapshotFile) Close() error {
	err := f.File.Close()
	if f.failure == "close" {
		return errors.New("injected close failure")
	}
	return err
}
func (f *failingSnapshotFile) Sync() error {
	if f.failure == "sync" {
		return errors.New("injected sync failure")
	}
	return f.File.Sync()
}
func (f *failingSnapshotFile) Chmod(mode os.FileMode) error {
	if f.failure == "chmod" {
		return errors.New("injected chmod failure")
	}
	return f.File.Chmod(mode)
}

func TestSnapshotPrepublicationFailuresPreserveOldFile(t *testing.T) {
	for _, failure := range []string{"write", "short write", "close", "sync", "chmod", "rename"} {
		for _, overwrite := range []bool{false, true} {
			t.Run(failure+"/"+map[bool]string{false: "create", true: "replace"}[overwrite], func(t *testing.T) {
				dir := t.TempDir()
				path := filepath.Join(dir, "snapshot.json")
				if overwrite {
					if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				ops := defaultSnapshotFileOps()
				ops.createTemp = func(dir, pattern string) (snapshotTempFile, error) {
					f, err := os.CreateTemp(dir, pattern)
					return &failingSnapshotFile{File: f, failure: failure}, err
				}
				if failure == "rename" {
					ops.rename = func(string, string) error { return errors.New("injected publish failure") }
					ops.link = ops.rename
				}
				if err := publishSnapshotFile(path, []byte("new"), overwrite, 0644, ops); err == nil {
					t.Fatal("expected injected failure")
				}
				data, err := os.ReadFile(path)
				if overwrite && (err != nil || string(data) != "old") {
					t.Fatalf("old snapshot lost: %q %v", data, err)
				}
				if !overwrite && !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("unexpected destination: %q %v", data, err)
				}
				entries, err := os.ReadDir(dir)
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if entry.Name() != "snapshot.json" {
						t.Fatalf("temporary file leaked: %s", entry.Name())
					}
				}
			})
		}
	}
}

func TestSnapshotExclusivePublicationUnderContention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.json")
	const writers = 12
	var wg sync.WaitGroup
	errorsCh := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errorsCh <- writeSnapshotOutputFile(path, []byte("complete"), false) }()
	}
	wg.Wait()
	close(errorsCh)
	successes := 0
	for err := range errorsCh {
		if err == nil {
			successes++
		} else if !errors.Is(err, os.ErrExist) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("got %d successful writers", successes)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "complete" {
		t.Fatalf("%q %v", data, err)
	}
}

func TestSnapshotSymlinkAndPermissionPolicy(t *testing.T) {
	dir := t.TempDir()
	target, link := filepath.Join(dir, "target.json"), filepath.Join(dir, "link.json")
	if err := os.WriteFile(target, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := writeSnapshotOutputFile(link, []byte("new"), true); err == nil {
		t.Fatal("symlink accepted")
	}
	data, _ := os.ReadFile(target)
	if string(data) != "old" {
		t.Fatal("symlink target modified")
	}
	if err := writeSnapshotOutputFile(target, []byte("new"), true); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("permissions broadened: %v %v", info, err)
	}
}

func TestSnapshotCollisionsRejectedBeforeAWS(t *testing.T) {
	previous := logsClientFactory
	logsClientFactory = func(string, string) (LogsClient, error) { t.Fatal("AWS must not be initialized"); return nil, nil }
	t.Cleanup(func() { logsClientFactory = previous })
	for _, functions := range []string{"orders,orders-", "Orders,orders"} {
		for _, overwrite := range []bool{false, true} {
			args := []string{"capture", "--function", functions, "--label", "a", "--out", t.TempDir()}
			if overwrite {
				args = append(args, "--overwrite")
			}
			code, _, diagnostic := runCLI(t, args)
			if code == 0 || !strings.Contains(diagnostic, "collision") {
				t.Fatalf("%d %s", code, diagnostic)
			}
		}
	}
}

func TestSnapshotPostpublicationFailureIsExplicit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.json")
	ops := defaultSnapshotFileOps()
	ops.syncDir = func(string) error { return errors.New("injected directory sync failure") }
	err := publishSnapshotFile(path, []byte("complete"), false, 0600, ops)
	if err == nil || !strings.Contains(err.Error(), "snapshot published") {
		t.Fatalf("%v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "complete" {
		t.Fatal("published file missing")
	}
}
