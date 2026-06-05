package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBinaryNameForTarget(t *testing.T) {
	if got := binaryNameForTarget(releaseTarget{goos: "windows", goarch: "amd64"}); got != "lark-cli.exe" {
		t.Fatalf("binaryNameForTarget() = %q, want %q", got, "lark-cli.exe")
	}
	if got := binaryNameForTarget(releaseTarget{goos: "darwin", goarch: "arm64"}); got != "lark-cli" {
		t.Fatalf("binaryNameForTarget() = %q, want %q", got, "lark-cli")
	}
}

func TestArchiveFileName(t *testing.T) {
	if got := archiveFileName("1.0.46", releaseTarget{goos: "windows", goarch: "amd64"}); got != "lark-cli-1.0.46-windows-amd64.zip" {
		t.Fatalf("archiveFileName() = %q", got)
	}
	if got := archiveFileName("1.0.46", releaseTarget{goos: "darwin", goarch: "arm64"}); got != "lark-cli-1.0.46-darwin-arm64.tar.gz" {
		t.Fatalf("archiveFileName() = %q", got)
	}
}

func TestHasServicesMeta(t *testing.T) {
	tempDir := t.TempDir()
	validPath := filepath.Join(tempDir, "valid.json")
	if err := os.WriteFile(validPath, []byte(`{"services":[{"name":"calendar"}]}`), 0o644); err != nil {
		t.Fatalf("write valid file: %v", err)
	}
	ok, err := hasServicesMeta(validPath)
	if err != nil {
		t.Fatalf("hasServicesMeta(valid) error = %v", err)
	}
	if !ok {
		t.Fatal("hasServicesMeta(valid) = false, want true")
	}

	emptyPath := filepath.Join(tempDir, "empty.json")
	if err := os.WriteFile(emptyPath, []byte(`{"services":[]}`), 0o644); err != nil {
		t.Fatalf("write empty file: %v", err)
	}
	ok, err = hasServicesMeta(emptyPath)
	if err != nil {
		t.Fatalf("hasServicesMeta(empty) error = %v", err)
	}
	if ok {
		t.Fatal("hasServicesMeta(empty) = true, want false")
	}

	invalidPath := filepath.Join(tempDir, "invalid.json")
	if err := os.WriteFile(invalidPath, []byte(`not-json`), 0o644); err != nil {
		t.Fatalf("write invalid file: %v", err)
	}
	ok, err = hasServicesMeta(invalidPath)
	if err != nil {
		t.Fatalf("hasServicesMeta(invalid) error = %v", err)
	}
	if ok {
		t.Fatal("hasServicesMeta(invalid) = true, want false")
	}
}
