package main

import (
	"encoding/json"
	"fmt"
)

const currentSchemaVersion = 1

func nonNil[T any](values []T) []T {
	if values == nil {
		return []T{}
	}
	return values
}

// Legacy snapshots remain unversioned unless explicitly migrated by a writer.
func (s Snapshot) MarshalJSON() ([]byte, error) {
	type wireSnapshot Snapshot
	s.Invocations = nonNil(s.Invocations)
	if s.Capture != nil {
		metadata := *s.Capture
		metadata.Diagnostics, metadata.FailedStreams = nonNil(metadata.Diagnostics), nonNil(metadata.FailedStreams)
		s.Capture = &metadata
	}
	return json.Marshal(wireSnapshot(s))
}

func validateSnapshotSchema(snap Snapshot) error {
	if snap.SchemaVersion < 0 || snap.SchemaVersion > currentSchemaVersion {
		return fmt.Errorf("unsupported snapshot schema_version %d (supported: legacy/0 and %d)", snap.SchemaVersion, currentSchemaVersion)
	}
	return nil
}

func validateSnapshotRecords(snap Snapshot) error {
	if err := validateSnapshotSchema(snap); err != nil {
		return err
	}
	if snap.CapturedAt != "" {
		if _, ok := parseInvocationTimestamp(snap.CapturedAt); !ok {
			return fmt.Errorf("invalid captured_at timestamp %q", snap.CapturedAt)
		}
	} else if snap.SchemaVersion > 0 {
		return fmt.Errorf("versioned snapshot missing captured_at")
	}
	seen := map[string]bool{}
	if snap.Capture != nil {
		if snap.Capture.SelectedCount != len(snap.Invocations) || snap.Capture.RequestedCount <= 0 || snap.Capture.Offset < 0 {
			return fmt.Errorf("capture metadata counts are inconsistent")
		}
		if snap.Capture.Complete && !snapshotIsComplete(snap) {
			return fmt.Errorf("capture claims completeness without sufficient evidence")
		}
	}
	for i, inv := range snap.Invocations {
		prefix := fmt.Sprintf("invocation %d", i+1)
		if inv.RequestID == "" {
			return fmt.Errorf("%s missing request_id", prefix)
		}
		if seen[inv.RequestID] {
			return fmt.Errorf("%s duplicates request_id %q", prefix, inv.RequestID)
		}
		seen[inv.RequestID] = true
		if _, ok := parseInvocationTimestamp(inv.Timestamp); !ok {
			return fmt.Errorf("%s has invalid timestamp %q", prefix, inv.Timestamp)
		}
		if !inv.IsError && len(inv.ErrorLines) > 0 {
			return fmt.Errorf("%s has error_lines but is_error is false", prefix)
		}
		if inv.Completion != nil && inv.Completion.Status != newInvocationCompletion(inv.Completion.StartSeen, inv.Completion.ReportSeen).Status {
			return fmt.Errorf("%s has inconsistent completion metadata", prefix)
		}
		for _, metric := range []struct {
			name, value string
			parse       func(string) (float64, bool)
		}{
			{"duration", inv.Duration, parseDurationMs}, {"billed_ms", inv.BilledMs, parseDurationMs},
			{"memory_size_mb", inv.MemorySizeMB, parseMemMB}, {"max_memory_used_mb", inv.MaxMemoryUsedMB, parseMemMB},
		} {
			if metric.value == "" {
				continue
			}
			if _, valid := metric.parse(metric.value); !valid {
				return fmt.Errorf("%s has invalid %s %q", prefix, metric.name, metric.value)
			}
		}
	}
	return nil
}
