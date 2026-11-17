package main

import (
	"encoding/json"
	"errors"
	"fmt"
)

const (
	exitOK               = 0
	exitOperational      = 1
	exitInvalidInput     = 2
	exitCaptureFailed    = 3
	exitInsufficientData = 4
	exitRegression       = 5
)

type commandError struct {
	Exit     int
	Code     string
	Cause    error
	Reported bool
}

func (e *commandError) Error() string { return e.Cause.Error() }
func (e *commandError) Unwrap() error { return e.Cause }

func inputError(code string, err error) error {
	return &commandError{Exit: exitInvalidInput, Code: code, Cause: err}
}

func statusForExit(exit int) string {
	switch exit {
	case exitOK:
		return "passed"
	case exitInvalidInput:
		return "invalid_input"
	case exitCaptureFailed:
		return "capture_failed"
	case exitInsufficientData:
		return "insufficient_data"
	case exitRegression:
		return "regression"
	default:
		return "operational_failure"
	}
}

func reportCommandError(err error, machine bool) int {
	var typed *commandError
	if !errors.As(err, &typed) {
		typed = &commandError{Exit: exitOperational, Code: "operation_failed", Cause: err}
	}
	if machine && !typed.Reported {
		envelope := struct {
			SchemaVersion int    `json:"schema_version"`
			ToolVersion   string `json:"tool_version"`
			Status        string `json:"status"`
			ExitCode      int    `json:"exit_code"`
			Code          string `json:"code"`
			Message       string `json:"message"`
		}{currentSchemaVersion, appVersion, statusForExit(typed.Exit), typed.Exit, typed.Code, typed.Error()}
		if outputErr := json.NewEncoder(stdout).Encode(envelope); outputErr != nil {
			fmt.Fprintf(stderr, "Error writing error report: %v\n", outputErr)
			return exitOperational
		}
	}
	fmt.Fprintf(stderr, "Error: %v\n", typed)
	return typed.Exit
}
