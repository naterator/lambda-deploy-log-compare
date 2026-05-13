package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
)

func TestParseFunctionNames(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "trims and skips blanks",
			input: " func-a , ,func-b,   func-c  ",
			want:  []string{"func-a", "func-b", "func-c"},
		},
		{
			name:  "empty input",
			input: "   ,,  ",
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseFunctionNames(tt.input)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseFunctionNames(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestFirstDuplicate(t *testing.T) {
	tests := []struct {
		name   string
		values []string
		want   string
	}{
		{
			name:   "no duplicates",
			values: []string{"func-a", "func-b"},
			want:   "",
		},
		{
			name:   "duplicate found",
			values: []string{"func-a", "func-b", "func-a", "func-c"},
			want:   "func-a",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := firstDuplicate(tt.values); got != tt.want {
				t.Fatalf("firstDuplicate(%v) = %q, want %q", tt.values, got, tt.want)
			}
		})
	}
}

func TestValidateCaptureInputs(t *testing.T) {
	validFunctions := []string{"func-a"}

	tests := []struct {
		name      string
		functions []string
		label     string
		count     int
		offset    int
		wantErr   string
	}{
		{
			name:      "valid",
			functions: validFunctions,
			label:     "pre-deploy",
			count:     10,
			offset:    0,
		},
		{
			name:      "missing label",
			functions: validFunctions,
			count:     10,
			wantErr:   "--label is required",
		},
		{
			name:    "missing function",
			label:   "pre-deploy",
			count:   10,
			wantErr: "--function is required",
		},
		{
			name:      "non-positive count",
			functions: validFunctions,
			label:     "pre-deploy",
			count:     0,
			wantErr:   "--count must be greater than 0",
		},
		{
			name:      "negative offset",
			functions: validFunctions,
			label:     "pre-deploy",
			count:     10,
			offset:    -1,
			wantErr:   "--offset must be greater than or equal to 0",
		},
		{
			name:      "duplicate function name",
			functions: []string{"func-a", "func-a"},
			label:     "pre-deploy",
			count:     10,
			wantErr:   "--function contains duplicate name \"func-a\"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCaptureInputs(tt.functions, tt.label, tt.count, tt.offset)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateCaptureInputs returned unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateCaptureInputs returned nil, want %q", tt.wantErr)
			}
			if err.Error() != tt.wantErr {
				t.Fatalf("validateCaptureInputs error = %q, want %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestNewLogsClient_InstantiatesCloudWatchLogsClient(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config")
	credentialsPath := filepath.Join(dir, "credentials")
	if err := os.WriteFile(configPath, []byte("[default]\nregion = us-east-1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credentialsPath, []byte("[default]\naws_access_key_id = test\naws_secret_access_key = test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", configPath)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", credentialsPath)
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_PROFILE", "")

	client, err := newLogsClient("us-east-1", "")
	if err != nil {
		t.Fatalf("newLogsClient error: %v", err)
	}
	if _, ok := client.(*cloudwatchlogs.Client); !ok {
		t.Fatalf("newLogsClient returned %T, want *cloudwatchlogs.Client", client)
	}
}
