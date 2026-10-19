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

func main() {
	var (
		versionFlag = flag.String("version", "", "Release version (e.g. v1.0.0). Defaults to git tag or 0.2.0-dev")
		distDirFlag = flag.String("dist", "dist", "Output directory for release archives")
		osFlag      = flag.String("os", "windows,darwin,linux", "Comma-separated target operating systems")
		archFlag    = flag.String("arch", "amd64,arm64", "Comma-separated target architectures")
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
	docFiles := findBundleFiles()

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

func buildTargets(osList, archList []string) []Target {
	var targets []Target
	for _, o := range osList {
		for _, a := range archList {
			targets = append(targets, Target{OS: o, Arch: a})
		}
	}
	return targets
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

func findBundleFiles() []string {
	candidates := []string{
		"README.md",
		".env.sample",
		"LICENSE",
	}
	var found []string
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			found = append(found, c)
		}
	}
	return found
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
	cmd.Env = append(os.Environ(),
		"CGO_ENABLED=0",
		"GOOS="+target.OS,
		"GOARCH="+target.Arch,
	)
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

	// 2. Add documentation / config files
	for _, doc := range docFiles {
		if err := addFileToZip(zw, doc, filepath.Base(doc)); err != nil {
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

	// 2. Add documentation / config files (mode 0644)
	for _, doc := range docFiles {
		if err := addFileToTar(tw, doc, filepath.Base(doc), 0644); err != nil {
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

