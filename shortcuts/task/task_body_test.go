// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package task

import (
	"errors"
	"testing"

	"github.com/larksuite/cli/internal/output"
	"github.com/spf13/cobra"

	"github.com/larksuite/cli/shortcuts/common"
)

func TestBuildTaskCreateBody_StructuredErrors(t *testing.T) {
	tests := []struct {
		name       string
		data       string
		summary    string
		due        string
		wantCode   int
		wantType   string
		wantSubstr string
	}{
		{
			name:       "invalid JSON data returns ErrValidation",
			data:       "not-json",
			summary:    "test",
			wantCode:   output.ExitValidation,
			wantType:   "validation",
			wantSubstr: "--data must be a valid JSON object",
		},
		{
			name:       "missing summary returns ErrValidation",
			data:       "",
			summary:    "",
			wantCode:   output.ExitValidation,
			wantType:   "validation",
			wantSubstr: "task summary is required",
		},
		{
			name:       "invalid due time returns ErrValidation",
			data:       "",
			summary:    "test task",
			due:        "not-a-valid-time",
			wantCode:   output.ExitValidation,
			wantType:   "validation",
			wantSubstr: "failed to parse due time",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &cobra.Command{}
			cmd.Flags().String("data", tt.data, "")
			cmd.Flags().String("summary", tt.summary, "")
			cmd.Flags().String("description", "", "")
			cmd.Flags().String("assignee", "", "")
			cmd.Flags().String("follower", "", "")
			cmd.Flags().String("due", tt.due, "")
			cmd.Flags().String("tasklist-id", "", "")
			cmd.Flags().String("idempotency-key", "", "")

			runtime := &common.RuntimeContext{Cmd: cmd}
			_, err := buildTaskCreateBody(runtime)
			if err == nil {
				t.Fatal("expected error, got nil")
			}

			var exitErr *output.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("error type = %T, want *output.ExitError; error = %v", err, err)
			}
			if exitErr.Code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", exitErr.Code, tt.wantCode)
			}
			if exitErr.Detail == nil {
				t.Fatal("expected non-nil error detail")
			}
			if exitErr.Detail.Type != tt.wantType {
				t.Errorf("error type = %q, want %q", exitErr.Detail.Type, tt.wantType)
			}
		})
	}
}

func TestBuildTaskUpdateBody_StructuredErrors(t *testing.T) {
	tests := []struct {
		name       string
		data       string
		summary    string
		due        string
		wantCode   int
		wantType   string
		wantSubstr string
	}{
		{
			name:       "invalid JSON data returns ErrValidation",
			data:       "not-json",
			summary:    "",
			due:        "",
			wantCode:   output.ExitValidation,
			wantType:   "validation",
			wantSubstr: "--data must be a valid JSON object",
		},
		{
			name:       "no fields to update returns ErrValidation",
			data:       "",
			summary:    "",
			due:        "",
			wantCode:   output.ExitValidation,
			wantType:   "validation",
			wantSubstr: "no fields to update",
		},
		{
			name:       "invalid due time returns ErrValidation",
			data:       "",
			summary:    "",
			due:        "not-a-valid-time",
			wantCode:   output.ExitValidation,
			wantType:   "validation",
			wantSubstr: "failed to parse due time",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &cobra.Command{}
			cmd.Flags().String("data", tt.data, "")
			cmd.Flags().String("summary", tt.summary, "")
			cmd.Flags().String("description", "", "")
			cmd.Flags().String("due", tt.due, "")

			runtime := &common.RuntimeContext{Cmd: cmd}
			_, err := buildTaskUpdateBody(runtime)
			if err == nil {
				t.Fatal("expected error, got nil")
			}

			var exitErr *output.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("error type = %T, want *output.ExitError; error = %v", err, err)
			}
			if exitErr.Code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", exitErr.Code, tt.wantCode)
			}
			if exitErr.Detail == nil {
				t.Fatal("expected non-nil error detail")
			}
			if exitErr.Detail.Type != tt.wantType {
				t.Errorf("error type = %q, want %q", exitErr.Detail.Type, tt.wantType)
			}
		})
	}
}
