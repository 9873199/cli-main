package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type releaseTarget struct {
	goos   string
	goarch string
}

type packageJSON struct {
	Version string `json:"version"`
}

var releaseTargets = []releaseTarget{
	{goos: "windows", goarch: "amd64"},
	{goos: "windows", goarch: "arm64"},
	{goos: "darwin", goarch: "amd64"},
	{goos: "darwin", goarch: "arm64"},
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	root, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		return fmt.Errorf("go.mod not found in %s", root)
	}

	version, err := resolveVersion(root)
	if err != nil {
		return err
	}
	buildDate := time.Now().UTC().Format("2006-01-02")
	if err := ensureMeta(root); err != nil {
		return err
	}

	distRoot := filepath.Join(root, "dist", "standalone")
	if err := os.RemoveAll(distRoot); err != nil {
		return fmt.Errorf("clean dist directory: %w", err)
	}
	if err := os.MkdirAll(distRoot, 0o755); err != nil {
		return fmt.Errorf("create dist directory: %w", err)
	}

	archives := make([]string, 0, len(releaseTargets))
	for _, target := range releaseTargets {
		pkgName := packageDirName(version, target)
		pkgDir := filepath.Join(distRoot, pkgName)
		if err := os.MkdirAll(pkgDir, 0o755); err != nil {
			return fmt.Errorf("create package directory %s: %w", pkgDir, err)
		}
		if err := buildBinary(root, pkgDir, version, buildDate, target); err != nil {
			return err
		}
		if err := copyDistributionFiles(root, pkgDir); err != nil {
			return err
		}

		archivePath := filepath.Join(distRoot, archiveFileName(version, target))
		if target.goos == "windows" {
			if err := createZipArchive(distRoot, pkgName, archivePath); err != nil {
				return err
			}
		} else {
			if err := createTarGzArchive(distRoot, pkgName, archivePath); err != nil {
				return err
			}
		}
		archives = append(archives, archivePath)
		fmt.Fprintf(os.Stdout, "packaged %s\n", archivePath)
	}

	if err := writeChecksums(distRoot, archives); err != nil {
		return err
	}
	return nil
}

func resolveVersion(root string) (string, error) {
	if version := strings.TrimSpace(os.Getenv("LARK_CLI_PACKAGE_VERSION")); version != "" {
		return version, nil
	}
	pkgPath := filepath.Join(root, "package.json")
	if data, err := os.ReadFile(pkgPath); err == nil {
		var pkg packageJSON
		if err := json.Unmarshal(data, &pkg); err != nil {
			return "", fmt.Errorf("parse package.json: %w", err)
		}
		if strings.TrimSpace(pkg.Version) != "" {
			return strings.TrimSpace(pkg.Version), nil
		}
	}
	if version := strings.TrimSpace(gitDescribe(root)); version != "" {
		return version, nil
	}
	return "dev", nil
}

func gitDescribe(root string) string {
	cmd := exec.Command("git", "describe", "--tags", "--always", "--dirty")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func ensureMeta(root string) error {
	metaPath := filepath.Join(root, "internal", "registry", "meta_data.json")
	ok, err := hasServicesMeta(metaPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", metaPath, err)
	}
	if ok {
		return nil
	}

	pythonCommands := [][]string{
		{"python3", filepath.Join("scripts", "fetch_meta.py")},
		{"python", filepath.Join("scripts", "fetch_meta.py")},
		{"py", "-3", filepath.Join("scripts", "fetch_meta.py")},
	}
	for _, args := range pythonCommands {
		if _, err := exec.LookPath(args[0]); err != nil {
			continue
		}
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = root
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Env = append(os.Environ(), "PYTHONUTF8=1", "PYTHONIOENCODING=utf-8")
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("refresh metadata with %s: %w", strings.Join(args, " "), err)
		}
		return nil
	}
	return fmt.Errorf("metadata missing and no python runtime found to run scripts/fetch_meta.py")
}

func hasServicesMeta(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var payload struct {
		Services []json.RawMessage `json:"services"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return false, nil
	}
	return len(payload.Services) > 0, nil
}

func buildBinary(root, pkgDir, version, buildDate string, target releaseTarget) error {
	binaryPath := filepath.Join(pkgDir, binaryNameForTarget(target))
	ldflags := fmt.Sprintf("-s -w -X github.com/larksuite/cli/internal/build.Version=%s -X github.com/larksuite/cli/internal/build.Date=%s", version, buildDate)
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags", ldflags, "-o", binaryPath, ".")
	cmd.Dir = root
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+target.goos, "GOARCH="+target.goarch)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build %s/%s failed: %w", target.goos, target.goarch, err)
	}
	return nil
}

func copyDistributionFiles(root, pkgDir string) error {
	for _, name := range []string{"README.md", "LICENSE", "CHANGELOG.md"} {
		srcPath := filepath.Join(root, name)
		dstPath := filepath.Join(pkgDir, name)
		if err := copyFile(srcPath, dstPath); err != nil {
			return fmt.Errorf("copy %s: %w", name, err)
		}
	}
	return nil
}

func copyFile(srcPath, dstPath string) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()

	info, err := src.Stat()
	if err != nil {
		return err
	}

	dst, err := os.OpenFile(dstPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode())
	if err != nil {
		return err
	}
	defer dst.Close()

	_, err = io.Copy(dst, src)
	return err
}

func binaryNameForTarget(target releaseTarget) string {
	if target.goos == "windows" {
		return "lark-cli.exe"
	}
	return "lark-cli"
}

func packageDirName(version string, target releaseTarget) string {
	return fmt.Sprintf("lark-cli-%s-%s-%s", version, target.goos, target.goarch)
}

func archiveFileName(version string, target releaseTarget) string {
	name := packageDirName(version, target)
	if target.goos == "windows" {
		return name + ".zip"
	}
	return name + ".tar.gz"
}

func createZipArchive(baseDir, dirName, archivePath string) error {
	archiveFile, err := os.Create(archivePath)
	if err != nil {
		return fmt.Errorf("create %s: %w", archivePath, err)
	}
	defer archiveFile.Close()

	writer := zip.NewWriter(archiveFile)
	defer writer.Close()

	sourceDir := filepath.Join(baseDir, dirName)
	return filepath.WalkDir(sourceDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(baseDir, path)
		if err != nil {
			return err
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(relPath)
		header.Method = zip.Deflate
		fileWriter, err := writer.CreateHeader(header)
		if err != nil {
			return err
		}
		src, err := os.Open(path)
		if err != nil {
			return err
		}
		defer src.Close()
		_, err = io.Copy(fileWriter, src)
		return err
	})
}

func createTarGzArchive(baseDir, dirName, archivePath string) error {
	archiveFile, err := os.Create(archivePath)
	if err != nil {
		return fmt.Errorf("create %s: %w", archivePath, err)
	}
	defer archiveFile.Close()

	gzipWriter := gzip.NewWriter(archiveFile)
	defer gzipWriter.Close()

	tarWriter := tar.NewWriter(gzipWriter)
	defer tarWriter.Close()

	sourceDir := filepath.Join(baseDir, dirName)
	return filepath.WalkDir(sourceDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(baseDir, path)
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(relPath)
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
		src, err := os.Open(path)
		if err != nil {
			return err
		}
		defer src.Close()
		_, err = io.Copy(tarWriter, src)
		return err
	})
}

func writeChecksums(distRoot string, archives []string) error {
	lines := make([]string, 0, len(archives))
	for _, archivePath := range archives {
		hash, err := sha256File(archivePath)
		if err != nil {
			return fmt.Errorf("checksum %s: %w", archivePath, err)
		}
		lines = append(lines, fmt.Sprintf("%s  %s", hash, filepath.Base(archivePath)))
	}
	content := strings.Join(lines, "\n") + "\n"
	checksumsPath := filepath.Join(distRoot, "checksums.txt")
	if err := os.WriteFile(checksumsPath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", checksumsPath, err)
	}
	fmt.Fprintf(os.Stdout, "packaged %s\n", checksumsPath)
	return nil
}

func sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	checksum := sha256.New()
	if _, err := io.Copy(checksum, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(checksum.Sum(nil)), nil
}
