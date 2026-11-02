package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const appName = "mini-aigw"

type Target struct {
	OS   string
	Arch string
}

// defaultTargets lists the supported release platforms.
var defaultTargets = []Target{
	{"windows", "amd64"},
	{"darwin", "arm64"},
	{"linux", "amd64"},
	{"linux", "arm64"},
}

func main() {
	var (
		versionFlag = flag.String("version", "", "Release version (e.g. v1.0.0). Defaults to git tag or 0.2.0-dev")
		distDirFlag = flag.String("dist", "dist", "Output directory for release archives")
		osFlag      = flag.String("os", "", "Comma-separated OS filter (default: all supported targets)")
		archFlag    = flag.String("arch", "", "Comma-separated architecture filter (default: all supported targets)")
		dryRunFlag  = flag.Bool("dry-run", false, "Preview build matrix without compiling")
	)
	flag.Parse()

	version := *versionFlag
	if version == "" {
		version = detectVersion()
	}
	if !strings.HasPrefix(version, "v") && version != "dev" {
		version = "v" + version
	}

	distDir := *distDirFlag
	targetOSList := parseList(*osFlag)
	targetArchList := parseList(*archFlag)

	targets := buildTargets(targetOSList, targetArchList)
	if len(targets) == 0 {
		log.Fatalf("No valid targets selected from os=%s, arch=%s", *osFlag, *archFlag)
	}

	gitCommit := getGitCommit()
	buildTime := time.Now().UTC().Format(time.RFC3339)

	fmt.Println("==================================================")
	fmt.Printf(" Building release for %s\n", appName)
	fmt.Printf(" Version:    %s\n", version)
	fmt.Printf(" Git Commit: %s\n", gitCommit)
	fmt.Printf(" Build Time: %s\n", buildTime)
	fmt.Printf(" Dist Dir:   %s\n", distDir)
	fmt.Printf(" Targets:    %d platforms\n", len(targets))
	fmt.Println("==================================================")

	if *dryRunFlag {
		fmt.Println("[DRY RUN] Planned targets and output archives:")
		for _, t := range targets {
			archiveName := formatArchiveName(version, t.OS, t.Arch)
			fmt.Printf(" - %s/%s -> %s/%s\n", t.OS, t.Arch, distDir, archiveName)
		}
		return
	}

	if err := os.MkdirAll(distDir, 0755); err != nil {
		log.Fatalf("Failed to create dist directory: %v", err)
	}

	// Documentation & config files to bundle in release archives
	docFiles, err := findBundleFiles()
	if err != nil {
		log.Fatalf("Failed to collect bundle files: %v", err)
	}

	var checksumLines []string

	for _, target := range targets {
		archivePath, checksum, err := buildAndPackage(target, version, gitCommit, buildTime, distDir, docFiles)
		if err != nil {
			log.Fatalf("Build failed for %s/%s: %v", target.OS, target.Arch, err)
		}

		archiveFilename := filepath.Base(archivePath)
		checksumLines = append(checksumLines, fmt.Sprintf("%s  %s", checksum, archiveFilename))
	}

	// Write SHA256SUMS.txt
	shaPath := filepath.Join(distDir, "SHA256SUMS.txt")
	shaContent := strings.Join(checksumLines, "\n") + "\n"
	if err := os.WriteFile(shaPath, []byte(shaContent), 0644); err != nil {
		log.Fatalf("Failed to write %s: %v", shaPath, err)
	}

	fmt.Println("--------------------------------------------------")
	fmt.Printf("✓ Successfully created %d archives and SHA256SUMS.txt in %s/\n", len(targets), distDir)
	fmt.Println("Checksums:")
	for _, line := range checksumLines {
		fmt.Printf("  %s\n", line)
	}
	fmt.Println("==================================================")
}

func parseList(s string) []string {
	var res []string
	for _, item := range strings.Split(s, ",") {
		trimmed := strings.TrimSpace(item)
		if trimmed != "" {
			res = append(res, trimmed)
		}
	}
	return res
}

// buildTargets filters defaultTargets by the given OS / arch lists (empty means no filter).
func buildTargets(osList, archList []string) []Target {
	var targets []Target
	for _, t := range defaultTargets {
		if (len(osList) == 0 || contains(osList, t.OS)) && (len(archList) == 0 || contains(archList, t.Arch)) {
			targets = append(targets, t)
		}
	}
	return targets
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

// buildEnv replaces build-related variables instead of appending duplicates.
func buildEnv(target Target) []string {
	overrides := map[string]string{
		"GOOS": target.OS, "GOARCH": target.Arch, "CGO_ENABLED": "0",
		"GOFLAGS": "", "GOAMD64": "v1", "GOARM64": "v8.0",
	}
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := overrides[strings.ToUpper(key)]; !replaced {
			env = append(env, entry)
		}
	}
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}
	return env
}

func formatArchiveName(version, goos, goarch string) string {
	ext := "tar.gz"
	if goos == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("%s_%s_%s_%s.%s", appName, version, goos, goarch, ext)
}

func detectVersion() string {
	out, err := exec.Command("git", "describe", "--tags", "--exact-match").Output()
	if err == nil {
		tag := strings.TrimSpace(string(out))
		if tag != "" {
			return tag
		}
	}
	return "v0.2.0-dev"
}

func getGitCommit() string {
	out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
	if err == nil {
		commit := strings.TrimSpace(string(out))
		if commit != "" {
			return commit
		}
	}
	return "none"
}

// findBundleFiles returns repository-relative paths bundled into every archive.
// Paths are kept as-is in the archive so that testdata/eval matches the
// default EVAL_DATASETS_DIR.
func findBundleFiles() ([]string, error) {
	optional := []string{
		"README.md",
		".env.sample",
		"LICENSE",
	}
	required := []string{
		"docs/RAG_EVALUATION_GUIDE.md",
		"testdata/eval/sample_dataset.jsonl",
	}
	var found []string
	for _, c := range optional {
		if _, err := os.Stat(c); err == nil {
			found = append(found, c)
		}
	}
	for _, c := range required {
		if _, err := os.Stat(c); err != nil {
			return nil, fmt.Errorf("required bundle file missing: %w", err)
		}
		found = append(found, c)
	}
	return found, nil
}

// parentDirs returns the archive directory entries (e.g. "testdata/", "testdata/eval/")
// needed for the given files, in creation order without duplicates.
func parentDirs(files []string) []string {
	seen := map[string]bool{}
	var dirs []string
	for _, f := range files {
		var chain []string
		for dir := filepath.ToSlash(filepath.Dir(f)); dir != "." && dir != "/"; dir = filepath.ToSlash(filepath.Dir(dir)) {
			chain = append([]string{dir + "/"}, chain...)
		}
		for _, d := range chain {
			if !seen[d] {
				seen[d] = true
				dirs = append(dirs, d)
			}
		}
	}
	return dirs
}

func buildAndPackage(
	target Target,
	version, gitCommit, buildTime, distDir string,
	docFiles []string,
) (string, string, error) {
	fmt.Printf("\n▶ Building %s/%s...\n", target.OS, target.Arch)
	start := time.Now()

	tempDir, err := os.MkdirTemp("", "mini-aigw-build-*")
	if err != nil {
		return "", "", fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tempDir)

	binName := appName
	if target.OS == "windows" {
		binName += ".exe"
	}
	binPath := filepath.Join(tempDir, binName)

	ldflags := fmt.Sprintf("-s -w -X mini-aigw/internal/version.Version=%s -X mini-aigw/internal/version.GitCommit=%s -X mini-aigw/internal/version.BuildTime=%s",
		version, gitCommit, buildTime)

	cmd := exec.Command("go", "build", "-ldflags", ldflags, "-o", binPath, "./cmd/mini-aigw")
	cmd.Env = buildEnv(target)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return "", "", fmt.Errorf("go build failed: %w", err)
	}

	fi, err := os.Stat(binPath)
	if err != nil {
		return "", "", fmt.Errorf("failed to stat binary: %w", err)
	}
	binSizeMB := float64(fi.Size()) / (1024 * 1024)

	// Create archive
	archiveFilename := formatArchiveName(version, target.OS, target.Arch)
	archivePath := filepath.Join(distDir, archiveFilename)

	if target.OS == "windows" {
		if err := createZip(archivePath, binPath, binName, docFiles); err != nil {
			return "", "", fmt.Errorf("failed to create zip: %w", err)
		}
	} else {
		if err := createTarGz(archivePath, binPath, binName, docFiles); err != nil {
			return "", "", fmt.Errorf("failed to create tar.gz: %w", err)
		}
	}

	checksum, err := computeSHA256(archivePath)
	if err != nil {
		return "", "", fmt.Errorf("failed to compute sha256: %w", err)
	}

	archFi, _ := os.Stat(archivePath)
	archSizeMB := float64(archFi.Size()) / (1024 * 1024)
	fmt.Printf("✓ %s (binary: %.1f MB -> archive: %.1f MB) in %v\n",
		archiveFilename, binSizeMB, archSizeMB, time.Since(start).Round(time.Millisecond))

	return archivePath, checksum, nil
}

func createZip(zipPath, binPath, binName string, docFiles []string) error {
	f, err := os.Create(zipPath)
	if err != nil {
		return err
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	defer zw.Close()

	// 1. Add binary
	if err := addFileToZip(zw, binPath, binName); err != nil {
		return err
	}

	// 2. Add documentation / config / eval sample files (keeping relative paths)
	for _, dir := range parentDirs(docFiles) {
		header := &zip.FileHeader{Name: dir, Method: zip.Store}
		header.SetMode(os.ModeDir | 0755)
		header.Modified = time.Now()
		if _, err := zw.CreateHeader(header); err != nil {
			return err
		}
	}
	for _, doc := range docFiles {
		if err := addFileToZip(zw, doc, filepath.ToSlash(doc)); err != nil {
			return err
		}
	}

	return nil
}

func addFileToZip(zw *zip.Writer, srcPath, nameInZip string) error {
	srcFile, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	info, err := srcFile.Stat()
	if err != nil {
		return err
	}

	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	header.Name = nameInZip
	header.Method = zip.Deflate

	w, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}

	_, err = io.Copy(w, srcFile)
	return err
}

func createTarGz(tarGzPath, binPath, binName string, docFiles []string) error {
	f, err := os.Create(tarGzPath)
	if err != nil {
		return err
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	// 1. Add executable (mode 0755)
	if err := addFileToTar(tw, binPath, binName, 0755); err != nil {
		return err
	}

	// 2. Add documentation / config / eval sample files (mode 0644, keeping relative paths)
	for _, dir := range parentDirs(docFiles) {
		header := &tar.Header{
			Typeflag: tar.TypeDir,
			Name:     dir,
			Mode:     0755,
			ModTime:  time.Now(),
		}
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
	}
	for _, doc := range docFiles {
		if err := addFileToTar(tw, doc, filepath.ToSlash(doc), 0644); err != nil {
			return err
		}
	}

	return nil
}

func addFileToTar(tw *tar.Writer, srcPath, nameInTar string, mode int64) error {
	srcFile, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	info, err := srcFile.Stat()
	if err != nil {
		return err
	}

	header := &tar.Header{
		Name:    nameInTar,
		Size:    info.Size(),
		Mode:    mode,
		ModTime: time.Now(),
	}

	if err := tw.WriteHeader(header); err != nil {
		return err
	}

	_, err = io.Copy(tw, srcFile)
	return err
}

func computeSHA256(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
