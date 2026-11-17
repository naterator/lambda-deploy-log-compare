package main

import (
	"strings"
	"testing"
)

func TestCLIRejectsOperandsBeforeSideEffects(t *testing.T) {
	previous := logsClientFactory
	logsClientFactory = func(string, string) (LogsClient, error) {
		t.Fatal("invalid arguments must not initialize an AWS client")
		return nil, nil
	}
	t.Cleanup(func() { logsClientFactory = previous })
	for _, args := range [][]string{
		{"capture", "unexpected", "--function", "f", "--label", "a"},
		{"capture", "--function", "f", "unexpected", "--label", "a"},
		{"capture", "--function", "f", "--label", "a", "unexpected", "--overwrite"},
		{"capture", "--", "--function", "f", "--label", "a"},
		{"compare", "unexpected", "--a", "missing-a", "--b", "missing-b"},
		{"compare", "--a", "missing-a", "unexpected", "--b", "missing-b"},
		{"compare", "--a", "missing-a", "--b", "missing-b", "unexpected"},
		{"compare", "--a", "testdata/compare_baseline.json", "--b", "testdata/compare_new.json", "unexpected", "--fail-on-regression"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			code, out, diagnostic := runCLI(t, args)
			if code == 0 || out != "" || !strings.Contains(diagnostic, "positional arguments") {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, out, diagnostic)
			}
		})
	}
}

func TestCLIHelpSucceeds(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"--help"}, {"-h"}, {"capture", "--help"}, {"compare", "--help"}} {
		code, _, diagnostic := runCLI(t, args)
		if code != 0 || !strings.Contains(diagnostic, "Usage:") {
			t.Fatalf("%v: code=%d stderr=%q", args, code, diagnostic)
		}
	}
}
