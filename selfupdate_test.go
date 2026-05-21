package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type stubReleaseUpdater struct {
	runCalls       int
	currentVersion string
	err            error
}

func (s *stubReleaseUpdater) Run(ctx context.Context, currentVersion string, stdout io.Writer) error {
	s.runCalls++
	s.currentVersion = currentVersion
	if stdout != nil {
		_, _ = io.WriteString(stdout, "stub updater invoked\n")
	}
	return s.err
}

func withReleaseUpdaterStub(t *testing.T, updater releaseUpdater) {
	t.Helper()
	previous := makeReleaseUpdater
	makeReleaseUpdater = func() releaseUpdater { return updater }
	t.Cleanup(func() {
		makeReleaseUpdater = previous
	})
}

func withSyncDirPathStub(t *testing.T, fn func(string) error) {
	t.Helper()
	previous := syncDirPath
	syncDirPath = fn
	t.Cleanup(func() {
		syncDirPath = previous
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func newTestClient(fn roundTripFunc) *http.Client {
	return &http.Client{Transport: fn}
}

func testResponse(req *http.Request, statusCode int, body []byte) *http.Response {
	return &http.Response{
		StatusCode: statusCode,
		Status:     fmt.Sprintf("%d %s", statusCode, http.StatusText(statusCode)),
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     make(http.Header),
		Request:    req,
	}
}

func TestRunVersionSubcommand(t *testing.T) {
	previous := appVersion
	appVersion = "v1.2.3"
	t.Cleanup(func() { appVersion = previous })

	code, stdoutText, stderrText := runCLI(t, []string{"version"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderrText)
	}
	if stdoutText != "v1.2.3\n" {
		t.Fatalf("stdout = %q, want version", stdoutText)
	}
	if stderrText != "" {
		t.Fatalf("stderr = %q, want empty", stderrText)
	}
}

func TestRunSelfupdateSubcommandRunsUpdater(t *testing.T) {
	stub := &stubReleaseUpdater{}
	withReleaseUpdaterStub(t, stub)

	code, stdoutText, stderrText := runCLI(t, []string{"selfupdate"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderrText)
	}
	if stub.runCalls != 1 {
		t.Fatalf("stub updater runCalls = %d, want 1", stub.runCalls)
	}
	if stub.currentVersion != appVersion {
		t.Fatalf("stub currentVersion = %q, want %q", stub.currentVersion, appVersion)
	}
	if !strings.Contains(stdoutText, "stub updater invoked") {
		t.Fatalf("stdout = %q, want updater output", stdoutText)
	}
	if stderrText != "" {
		t.Fatalf("stderr = %q, want empty", stderrText)
	}
}

func TestRunSelfupdateSubcommandReportsUpdaterError(t *testing.T) {
	stub := &stubReleaseUpdater{err: errors.New("boom")}
	withReleaseUpdaterStub(t, stub)

	code, _, stderrText := runCLI(t, []string{"selfupdate"})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderrText, "selfupdate failed: boom") {
		t.Fatalf("stderr = %q, want updater error", stderrText)
	}
}

func TestRunSelfupdateSubcommandRejectsArguments(t *testing.T) {
	stub := &stubReleaseUpdater{}
	withReleaseUpdaterStub(t, stub)

	code, stdoutText, stderrText := runCLI(t, []string{"selfupdate", "--bogus"})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if stub.runCalls != 0 {
		t.Fatalf("stub updater runCalls = %d, want 0", stub.runCalls)
	}
	if stdoutText != "" {
		t.Fatalf("stdout = %q, want empty", stdoutText)
	}
	if !strings.Contains(stderrText, "selfupdate does not accept arguments") {
		t.Fatalf("stderr = %q, want argument error", stderrText)
	}
}

func TestReleaseAssetCandidates(t *testing.T) {
	binary, checksum := releaseAssetCandidates("darwin", "arm64")
	if len(binary) != 1 || binary[0] != appName+"-darwin-arm64" {
		t.Fatalf("binary candidates = %v", binary)
	}
	if len(checksum) != 1 || checksum[0] != appName+"-darwin-arm64.sha256" {
		t.Fatalf("checksum candidates = %v", checksum)
	}

	binary, checksum = releaseAssetCandidates("windows", "amd64")
	if len(binary) != 2 || binary[0] != appName+"-windows-amd64.exe" || binary[1] != appName+"-windows-amd64" {
		t.Fatalf("windows binary candidates = %v", binary)
	}
	if len(checksum) != 2 || checksum[0] != appName+"-windows-amd64.exe.sha256" || checksum[1] != appName+"-windows-amd64.sha256" {
		t.Fatalf("windows checksum candidates = %v", checksum)
	}
}

func TestParseSHA256File(t *testing.T) {
	checksum := strings.Repeat("a", 64)
	got, err := parseSHA256File(checksum+"  "+appName+"-darwin-arm64\n", appName+"-darwin-arm64")
	if err != nil {
		t.Fatalf("parseSHA256File error: %v", err)
	}
	if got != checksum {
		t.Fatalf("checksum = %q, want %q", got, checksum)
	}
}

func TestNormalizeSemverAndCompare(t *testing.T) {
	normalized, err := normalizeSemver("1.02.3")
	if err != nil {
		t.Fatalf("normalizeSemver error: %v", err)
	}
	if normalized != "v1.2.3" {
		t.Fatalf("normalized = %q, want v1.2.3", normalized)
	}
	if compareSemver("v1.2.4", "v1.2.3") != 1 {
		t.Fatalf("expected v1.2.4 > v1.2.3")
	}
	if compareSemver("v1.2.3", "v1.2.3") != 0 {
		t.Fatalf("expected v1.2.3 == v1.2.3")
	}
	if compareSemver("v1.2.2", "v1.2.3") != -1 {
		t.Fatalf("expected v1.2.2 < v1.2.3")
	}
}

func TestGitHubReleaseUpdaterSkipsCurrentVersion(t *testing.T) {
	requests := 0
	client := newTestClient(func(req *http.Request) (*http.Response, error) {
		requests++
		if req.Header.Get("User-Agent") != appName+"/"+appVersion {
			t.Fatalf("User-Agent = %q, want %q", req.Header.Get("User-Agent"), appName+"/"+appVersion)
		}
		return testResponse(req, http.StatusOK, []byte(`{"tag_name":"v1.2.3","assets":[]}`)), nil
	})
	updater := &githubReleaseUpdater{
		client:           client,
		latestReleaseURL: "https://example.test/latest",
		executablePath: func() (string, error) {
			t.Fatal("executablePath should not be called when already current")
			return "", nil
		},
		goos:   "darwin",
		goarch: "arm64",
	}

	var stdout bytes.Buffer
	if err := updater.Run(context.Background(), "v1.2.3", &stdout); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
	if !strings.Contains(stdout.String(), appName+" v1.2.3 is already up to date") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestGitHubReleaseUpdaterReplacesExecutable(t *testing.T) {
	exePath := filepath.Join(t.TempDir(), appName)
	if err := os.WriteFile(exePath, []byte("old-binary"), 0o755); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	requestedMode := os.FileMode(0o755) | os.ModeSetuid | os.ModeSetgid
	if err := os.Chmod(exePath, requestedMode); err != nil {
		t.Fatalf("Chmod returned error: %v", err)
	}
	initialInfo, err := os.Stat(exePath)
	if err != nil {
		t.Fatalf("Stat returned error: %v", err)
	}
	expectedMode := replacementExecutableMode(initialInfo.Mode())
	resolvedExePath, err := filepath.EvalSymlinks(exePath)
	if err != nil {
		t.Fatalf("EvalSymlinks returned error: %v", err)
	}
	expectedSyncDir := filepath.Dir(resolvedExePath)
	var syncedDir string
	var syncDirCalls int
	withSyncDirPathStub(t, func(path string) error {
		syncDirCalls++
		syncedDir = path
		return nil
	})

	binaryName := appName + "-darwin-arm64"
	checksumName := binaryName + ".sha256"
	binaryBody := []byte("new-binary-content")
	sum := sha256.Sum256(binaryBody)
	checksumBody := hex.EncodeToString(sum[:]) + "  " + binaryName + "\n"

	requestedURLs := make([]string, 0, 3)
	client := newTestClient(func(req *http.Request) (*http.Response, error) {
		requestedURLs = append(requestedURLs, req.URL.String())
		switch req.URL.String() {
		case "https://example.test/latest":
			body := fmt.Sprintf(`{
  "tag_name": "v9.9.9",
  "assets": [
    {"name": %q, "browser_download_url": "https://example.test/download/%s"},
    {"name": %q, "browser_download_url": "https://example.test/download/%s"}
  ]
}`, binaryName, binaryName, checksumName, checksumName)
			return testResponse(req, http.StatusOK, []byte(body)), nil
		case "https://example.test/download/" + checksumName:
			return testResponse(req, http.StatusOK, []byte(checksumBody)), nil
		case "https://example.test/download/" + binaryName:
			return testResponse(req, http.StatusOK, binaryBody), nil
		default:
			t.Fatalf("unexpected URL %s", req.URL.String())
			return nil, nil
		}
	})
	updater := &githubReleaseUpdater{
		client:           client,
		latestReleaseURL: "https://example.test/latest",
		executablePath: func() (string, error) {
			return exePath, nil
		},
		goos:   "darwin",
		goarch: "arm64",
	}

	var stdout bytes.Buffer
	if err := updater.Run(context.Background(), "v1.0.0", &stdout); err != nil {
		t.Fatalf("Run error: %v", err)
	}

	updated, err := os.ReadFile(exePath)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if string(updated) != string(binaryBody) {
		t.Fatalf("updated executable = %q, want %q", string(updated), string(binaryBody))
	}
	updatedInfo, err := os.Stat(exePath)
	if err != nil {
		t.Fatalf("Stat returned error: %v", err)
	}
	if updatedInfo.Mode()&executableModeMask != expectedMode {
		t.Fatalf("mode = %v, want %v", updatedInfo.Mode()&executableModeMask, expectedMode)
	}
	if syncDirCalls != 1 || syncedDir != expectedSyncDir {
		t.Fatalf("sync dir calls = %d path = %q, want 1 %q", syncDirCalls, syncedDir, expectedSyncDir)
	}
	if strings.Join(requestedURLs, ",") != "https://example.test/latest,https://example.test/download/"+checksumName+",https://example.test/download/"+binaryName {
		t.Fatalf("requested URLs = %v", requestedURLs)
	}
	if !strings.Contains(stdout.String(), "Updating "+appName+" from v1.0.0 to v9.9.9") ||
		!strings.Contains(stdout.String(), "Updated "+appName+" to v9.9.9") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}
