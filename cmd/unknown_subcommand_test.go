// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package cmd

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/larksuite/cli/internal/output"
)

func newGroupTree() (root, drive, files *cobra.Command) {
	root = &cobra.Command{Use: "lark-cli"}
	drive = &cobra.Command{Use: "drive", Short: "drive ops"}
	root.AddCommand(drive)

	search := &cobra.Command{Use: "+search", RunE: func(*cobra.Command, []string) error { return nil }}
	upload := &cobra.Command{Use: "+upload", RunE: func(*cobra.Command, []string) error { return nil }}
	hidden := &cobra.Command{Use: "+secret", Hidden: true, RunE: func(*cobra.Command, []string) error { return nil }}
	drive.AddCommand(search, upload, hidden)

	files = &cobra.Command{Use: "files", Short: "files ops"}
	drive.AddCommand(files)
	files.AddCommand(&cobra.Command{Use: "list", RunE: func(*cobra.Command, []string) error { return nil }})

	return root, drive, files
}

func TestInstallUnknownSubcommandGuard_InstallsOnGroupsOnly(t *testing.T) {
	root, drive, files := newGroupTree()
	leaf := drive.Commands()[0] // +search

	installUnknownSubcommandGuard(root)

	if drive.RunE == nil {
		t.Error("drive should have RunE installed")
	}
	if files.RunE == nil {
		t.Error("files should have RunE installed")
	}
	if err := leaf.RunE(leaf, []string{"unexpected-arg"}); err != nil {
		t.Errorf("leaf +search RunE should be untouched, got error %v", err)
	}
}

func TestInstallUnknownSubcommandGuard_PreservesExistingRunE(t *testing.T) {
	root := &cobra.Command{Use: "lark-cli"}
	called := false
	custom := &cobra.Command{
		Use: "custom",
		RunE: func(*cobra.Command, []string) error {
			called = true
			return nil
		},
	}
	// Child makes custom a "group" command, exercising the Run/RunE override guard.
	custom.AddCommand(&cobra.Command{Use: "leaf", RunE: func(*cobra.Command, []string) error { return nil }})
	root.AddCommand(custom)

	installUnknownSubcommandGuard(root)

	if err := custom.RunE(custom, nil); err != nil {
		t.Fatalf("preserved RunE returned error: %v", err)
	}
	if !called {
		t.Error("guard must not overwrite a command that already defines Run/RunE")
	}
}

func TestUnknownSubcommandRunE_NoArgsShowsHelp(t *testing.T) {
	_, drive, _ := newGroupTree()
	installUnknownSubcommandGuard(drive.Root())

	var buf bytes.Buffer
	drive.SetOut(&buf)
	drive.SetErr(&buf)

	if err := drive.RunE(drive, nil); err != nil {
		t.Fatalf("expected no-args invocation to succeed, got: %v", err)
	}
	if !strings.Contains(buf.String(), "drive ops") {
		t.Errorf("expected help output to include the command's Short, got:\n%s", buf.String())
	}
}

func TestUnknownSubcommandRunE_UnknownReturnsStructuredError(t *testing.T) {
	_, drive, _ := newGroupTree()
	installUnknownSubcommandGuard(drive.Root())

	err := drive.RunE(drive, []string{"+bogus"})
	if err == nil {
		t.Fatal("expected error for unknown subcommand")
	}

	var exitErr *output.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected *output.ExitError, got %T", err)
	}
	if exitErr.Code != output.ExitValidation {
		t.Errorf("expected exit code %d, got %d", output.ExitValidation, exitErr.Code)
	}
	if exitErr.Detail == nil {
		t.Fatal("expected ExitError to carry Detail")
	}
	if exitErr.Detail.Type != "unknown_subcommand" {
		t.Errorf("expected Detail.Type=unknown_subcommand, got %q", exitErr.Detail.Type)
	}
	if !strings.Contains(exitErr.Detail.Message, `"+bogus"`) {
		t.Errorf("message should echo the unknown token, got %q", exitErr.Detail.Message)
	}
	if !strings.Contains(exitErr.Detail.Hint, "+search") || !strings.Contains(exitErr.Detail.Hint, "+upload") {
		t.Errorf("hint should list available shortcuts, got %q", exitErr.Detail.Hint)
	}
	if strings.Contains(exitErr.Detail.Hint, "+secret") {
		t.Error("hidden commands must not appear in the hint")
	}

	detail, ok := exitErr.Detail.Detail.(map[string]any)
	if !ok {
		t.Fatalf("expected Detail.Detail to be map[string]any, got %T", exitErr.Detail.Detail)
	}
	if detail["unknown"] != "+bogus" {
		t.Errorf("detail.unknown should be +bogus, got %v", detail["unknown"])
	}
	if detail["command_path"] != "lark-cli drive" {
		t.Errorf("detail.command_path should be %q, got %v", "lark-cli drive", detail["command_path"])
	}
	available, ok := detail["available"].([]string)
	if !ok {
		t.Fatalf("detail.available should be []string, got %T", detail["available"])
	}
	if len(available) != 3 {
		t.Errorf("expected 3 available entries (hidden excluded), got %d: %v", len(available), available)
	}
}

func TestUnknownSubcommandRunE_NestedResourceGroup(t *testing.T) {
	root, _, files := newGroupTree()
	installUnknownSubcommandGuard(root)

	err := files.RunE(files, []string{"bogus"})
	var exitErr *output.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected *output.ExitError on nested group, got %T", err)
	}
	if exitErr.Detail.Detail.(map[string]any)["command_path"] != "lark-cli drive files" {
		t.Errorf("command_path should reflect the nested resource, got %v",
			exitErr.Detail.Detail.(map[string]any)["command_path"])
	}
}

func TestAvailableSubcommandNames_FiltersHelpAndCompletion(t *testing.T) {
	root := &cobra.Command{Use: "lark-cli"}
	root.AddCommand(
		&cobra.Command{Use: "alpha", RunE: func(*cobra.Command, []string) error { return nil }},
		&cobra.Command{Use: "help", RunE: func(*cobra.Command, []string) error { return nil }},
		&cobra.Command{Use: "completion", RunE: func(*cobra.Command, []string) error { return nil }},
		&cobra.Command{Use: "beta", Hidden: true, RunE: func(*cobra.Command, []string) error { return nil }},
		&cobra.Command{Use: "gamma", RunE: func(*cobra.Command, []string) error { return nil }},
	)

	got := availableSubcommandNames(root)
	want := []string{"alpha", "gamma"}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i, name := range want {
		if got[i] != name {
			t.Errorf("availableSubcommandNames[%d] = %q, want %q", i, got[i], name)
		}
	}
}
