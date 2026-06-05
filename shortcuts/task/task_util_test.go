// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package task

import (
	"errors"
	"testing"

	"github.com/larksuite/cli/internal/output"
	"github.com/smartystreets/goconvey/convey"
)

func TestContains(t *testing.T) {
	convey.Convey("contains", t, func() {
		list := []string{"a", "b", "c"}
		convey.So(contains(list, "a"), convey.ShouldBeTrue)
		convey.So(contains(list, "d"), convey.ShouldBeFalse)
		convey.So(contains([]string{}, "a"), convey.ShouldBeFalse)
	})
}

func TestParseRelativeTime_StructuredErrors(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantCode   int
		wantType   string
		wantSubstr string
	}{
		{
			name:       "invalid format returns ErrValidation",
			input:      "not-relative",
			wantCode:   output.ExitValidation,
			wantType:   "validation",
			wantSubstr: "invalid relative time format",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseRelativeTime(tt.input)
			if err == nil {
				t.Fatalf("parseRelativeTime(%q) expected error, got nil", tt.input)
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
