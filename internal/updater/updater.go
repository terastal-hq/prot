package updater

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// DefaultRepo is the GitHub repository for prot releases.
const DefaultRepo = "terastal-hq/prot"

// ErrNoReleases is returned when no releases exist in the repository.
var ErrNoReleases = errors.New("no releases published yet")

// ReleaseInfo contains GitHub release metadata.
type ReleaseInfo struct {
	TagName     string      `json:"tag_name"`
	Name        string      `json:"name"`
	Body        string      `json:"body"`
	PublishedAt time.Time   `json:"published_at"`
	Assets      []AssetInfo `json:"assets"`
}

// AssetInfo describes a release binary artifact.
type AssetInfo struct {
	Name        string `json:"name"`
	DownloadURL string `json:"browser_download_url"`
	Size        int64  `json:"size"`
}

// FetchLatestRelease queries the GitHub API for the latest release.
func FetchLatestRelease(ctx context.Context, repo string) (*ReleaseInfo, error) {
	if repo == "" {
		repo = DefaultRepo
	}

	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating release request: %w", err)
	}

	req.Header.Set("User-Agent", "prot-updater")
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("querying github releases: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNoReleases
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github api returned status %d", resp.StatusCode)
	}

	var rel ReleaseInfo
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("parsing release json: %w", err)
	}

	return &rel, nil
}

// CheckUpdate compares the current version against the latest release.
func CheckUpdate(ctx context.Context, repo, currentVersion string) (*ReleaseInfo, bool, error) {
	rel, err := FetchLatestRelease(ctx, repo)
	if err != nil {
		return nil, false, err
	}

	cleanCurrent := strings.TrimPrefix(strings.TrimSpace(currentVersion), "v")
	cleanLatest := strings.TrimPrefix(strings.TrimSpace(rel.TagName), "v")

	hasUpdate := cleanLatest != cleanCurrent && cleanLatest != ""
	return rel, hasUpdate, nil
}

// DownloadAsset downloads and extracts the prot and protd binaries from the release archive.
func DownloadAsset(ctx context.Context, downloadURL string) ([]byte, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", "prot-updater")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("downloading release archive: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	gzReader, err := gzip.NewReader(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("decompressing gzip archive: %w", err)
	}
	defer gzReader.Close()

	tarReader := tar.NewReader(gzReader)
	var protData, protdData []byte

	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("reading tar archive: %w", err)
		}

		baseName := filepath.Base(header.Name)
		if baseName == "prot" || baseName == "prot.exe" {
			data, err := io.ReadAll(tarReader)
			if err != nil {
				return nil, nil, fmt.Errorf("extracting prot binary: %w", err)
			}
			protData = data
		} else if baseName == "protd" || baseName == "protd.exe" {
			data, err := io.ReadAll(tarReader)
			if err != nil {
				return nil, nil, fmt.Errorf("extracting protd binary: %w", err)
			}
			protdData = data
		}
	}

	if len(protData) == 0 {
		return nil, nil, errors.New("prot binary not found in release archive")
	}
	if len(protdData) == 0 {
		return nil, nil, errors.New("protd binary not found in release archive")
	}

	return protData, protdData, nil
}

// ApplyUpdate writes the new binaries atomically using rename to avoid ETXTBSY.
func ApplyUpdate(protBytes, protdBytes []byte, targetDir string) error {
	if targetDir == "" {
		execPath, err := os.Executable()
		if err != nil {
			return fmt.Errorf("resolving executable path: %w", err)
		}
		realPath, err := filepath.EvalSymlinks(execPath)
		if err == nil {
			execPath = realPath
		}
		targetDir = filepath.Dir(execPath)
	}

	protDest := filepath.Join(targetDir, "prot")
	protdDest := filepath.Join(targetDir, "protd")

	if err := writeAtomic(protDest, protBytes); err != nil {
		return fmt.Errorf("updating prot binary at %q: %w", protDest, err)
	}

	if err := writeAtomic(protdDest, protdBytes); err != nil {
		return fmt.Errorf("updating protd binary at %q: %w", protdDest, err)
	}

	return nil
}

func writeAtomic(destPath string, data []byte) error {
	dir := filepath.Dir(destPath)
	tmpFile, err := os.CreateTemp(dir, "prot-update-*")
	if err != nil {
		return fmt.Errorf("creating temp file in %q: %w", dir, err)
	}
	tmpName := tmpFile.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("writing update data: %w", err)
	}
	if err := tmpFile.Chmod(0755); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("setting permissions: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}

	// Rename atomically replaces destPath even if currently running
	if err := os.Rename(tmpName, destPath); err != nil {
		return fmt.Errorf("replacing binary via rename: %w", err)
	}

	return nil
}

// RestartDaemon restarts the background protd service.
func RestartDaemon(ctx context.Context) error {
	// 1. Try systemd user service if Linux
	if runtime.GOOS == "linux" {
		if _, err := exec.LookPath("systemctl"); err == nil {
			out, _ := exec.CommandContext(ctx, "systemctl", "--user", "is-active", "protd").Output()
			if strings.TrimSpace(string(out)) == "active" {
				return exec.CommandContext(ctx, "systemctl", "--user", "restart", "protd").Run()
			}
		}
	}

	// 2. Try launchd on macOS
	if runtime.GOOS == "darwin" {
		home, _ := os.UserHomeDir()
		plistPath := filepath.Join(home, "Library", "LaunchAgents", "dev.prot.protd.plist")
		if _, err := os.Stat(plistPath); err == nil {
			_ = exec.CommandContext(ctx, "launchctl", "unload", plistPath).Run()
			return exec.CommandContext(ctx, "launchctl", "load", "-w", plistPath).Run()
		}
	}

	return nil
}

// FindAssetForPlatform finds the matching release tar.gz for the current OS/architecture.
func FindAssetForPlatform(rel *ReleaseInfo) (*AssetInfo, error) {
	target := fmt.Sprintf("prot_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	for i := range rel.Assets {
		if rel.Assets[i].Name == target {
			return &rel.Assets[i], nil
		}
	}

	return nil, fmt.Errorf("no binary artifact found matching %q for release %s", target, rel.TagName)
}
