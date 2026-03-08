package main

import (
	"reflect"
	"testing"
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
