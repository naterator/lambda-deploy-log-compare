package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionedSnapshotAndLegacyCompatibility(t *testing.T) {
	for _, body := range []string{
		`{"function_name":"f","invocations":[{"mem_used_mb":"128 MB","max_mem_mb":"40 MB"}]}`,
		`{"schema_version":1,"tool_version":"test","function_name":"f","invocations":[{"memory_size_mb":"128 MB","max_memory_used_mb":"40 MB"}]}`,
	} {
		path := filepath.Join(t.TempDir(), "snapshot.json")
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		snap, err := loadSnapshot(path)
		if err != nil {
			t.Fatal(err)
		}
		if snap.Invocations[0].MemorySizeMB != "128 MB" || snap.Invocations[0].MaxMemoryUsedMB != "40 MB" {
			t.Fatalf("lost memory fields: %+v", snap)
		}
		encoded, err := json.Marshal(snap)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "mem_used_mb") || strings.Contains(string(encoded), "max_mem_mb") {
			t.Fatalf("emitted legacy aliases: %s", encoded)
		}
	}
	for _, version := range []int{-1, currentSchemaVersion + 1} {
		path := filepath.Join(t.TempDir(), "future.json")
		writeSnapshotFile(t, path, Snapshot{SchemaVersion: version, FunctionName: "f"})
		code, out, _ := runCLI(t, []string{"compare", "--a", path, "--b", path, "--json"})
		if code != exitInvalidInput || !strings.Contains(out, "unsupported snapshot schema_version") || !json.Valid([]byte(out)) {
			t.Fatalf("%d %s", code, out)
		}
	}
}

func TestSchemaGoldenMigration(t *testing.T) {
	want, err := os.ReadFile("testdata/schema/v1.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"testdata/schema/legacy.json", "testdata/schema/v1.json"} {
		snap, err := loadSnapshot(path)
		if err != nil {
			t.Fatal(err)
		}
		snap.SchemaVersion, snap.ToolVersion = currentSchemaVersion, "schema-test"
		if err := validateSnapshotRecords(snap); err != nil {
			t.Fatal(err)
		}
		got, err := json.MarshalIndent(snap, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(string(got)) != strings.TrimSpace(string(want)) {
			t.Fatalf("%s did not normalize to v1 golden:\n%s", path, got)
		}
	}
}

func TestStrictRecordsDiagnostics(t *testing.T) {
	valid := InvocationRecord{RequestID: "req", Timestamp: "2026-01-01T00:00:00.123Z", Duration: "10 ms"}
	for _, tt := range []struct {
		name   string
		change func(*Snapshot)
		want   string
	}{
		{"missing id", func(s *Snapshot) { s.Invocations[0].RequestID = "" }, "request_id"},
		{"timestamp", func(s *Snapshot) { s.Invocations[0].Timestamp = "bad" }, "timestamp"},
		{"duplicate", func(s *Snapshot) { s.Invocations = append(s.Invocations, s.Invocations[0]) }, "duplicates"},
		{"contradiction", func(s *Snapshot) { s.Invocations[0].ErrorLines = []string{"ERROR: failed"} }, "is_error"},
		{"numeric", func(s *Snapshot) { s.Invocations[0].Duration = "NaN" }, "duration"},
		{"units", func(s *Snapshot) { s.Invocations[0].MaxMemoryUsedMB = "40 GB" }, "max_memory_used_mb"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			snap := Snapshot{FunctionName: "f", LogGroup: "/aws/lambda/f", Invocations: []InvocationRecord{valid}}
			tt.change(&snap)
			path := filepath.Join(t.TempDir(), "snapshot.json")
			writeSnapshotFile(t, path, snap)
			code, out, _ := runCLI(t, []string{"compare", "--a", path, "--b", path, "--strict-records", "--json"})
			if code != exitInvalidInput || !strings.Contains(out, "invalid_record") || !strings.Contains(out, tt.want) || !json.Valid([]byte(out)) {
				t.Fatalf("%d %s", code, out)
			}
			if _, err := loadSnapshot(path); err != nil {
				t.Fatalf("permissive legacy read rejected: %v", err)
			}
		})
	}
}

func TestSnapshotAndReportArraysAreStable(t *testing.T) {
	for _, value := range []any{Snapshot{FunctionName: "f"}, InvocationRecord{}, buildCompareSummary("a", "b", Snapshot{}, Snapshot{}, nil, CompareOptions{})} {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "null") {
			t.Fatalf("empty arrays must be arrays: %s", data)
		}
	}
}

func TestJSONOutcomeMatchesProcessExit(t *testing.T) {
	code, out, _ := runCLI(t, []string{"compare", "--a", "testdata/compare_baseline.json", "--b", "testdata/compare_new.json", "--json", "--fail-on-regression"})
	var summary compareSummary
	if err := json.Unmarshal([]byte(out), &summary); err != nil {
		t.Fatal(err)
	}
	if code != exitRegression || summary.ExitCode != code || summary.Status != "regression" || summary.SchemaVersion != currentSchemaVersion || len(summary.Regression.ReasonCodes) == 0 {
		t.Fatalf("%d %+v", code, summary)
	}
	code, out, _ = runCLI(t, []string{"compare", "--json"})
	if code != exitInvalidInput || !json.Valid([]byte(out)) || !strings.Contains(out, "invalid_arguments") {
		t.Fatalf("%d %s", code, out)
	}
}
