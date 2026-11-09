package updater

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// Limits that keep a hostile or broken download from exhausting memory.
const (
	maxArchiveBytes   = 200 << 20 // compressed release archive
	maxBinaryBytes    = 200 << 20 // a single extracted binary
	maxChecksumsBytes = 1 << 20
)

// ChecksumsAssetName is the checksum manifest GoReleaser publishes with every release.
const ChecksumsAssetName = "checksums.txt"

// ErrChecksumMismatch is returned when a download does not match its published SHA-256.
var ErrChecksumMismatch = errors.New("checksum mismatch")

// ExpectedChecksum returns the published SHA-256 (lower-case hex) of the named release asset. Updating fails
// closed: a release without a checksum manifest, or without an entry for the asset, cannot be installed.
func ExpectedChecksum(ctx context.Context, rel *ReleaseInfo, assetName string) (string, error) {
	var manifest *AssetInfo
	for i := range rel.Assets {
		if rel.Assets[i].Name == ChecksumsAssetName {
			manifest = &rel.Assets[i]
			break
		}
	}
	if manifest == nil {
		return "", fmt.Errorf(
			"release %s has no %s, refusing to install unverified binaries",
			rel.TagName,
			ChecksumsAssetName,
		)
	}

	data, err := httpGetLimited(ctx, manifest.DownloadURL, maxChecksumsBytes, 15*time.Second)
	if err != nil {
		return "", fmt.Errorf("downloading %s: %w", ChecksumsAssetName, err)
	}
	return parseChecksum(data, assetName)
}

// parseChecksum finds assetName in a "sha256sum"-style manifest ("<hex>  <name>" per line).
func parseChecksum(manifest []byte, assetName string) (string, error) {
	for _, line := range strings.Split(string(manifest), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != assetName {
			continue
		}
		sum := strings.ToLower(fields[0])
		if decoded, err := hex.DecodeString(sum); err != nil || len(decoded) != sha256.Size {
			return "", fmt.Errorf("malformed checksum for %q in %s", assetName, ChecksumsAssetName)
		}
		return sum, nil
	}
	return "", fmt.Errorf("%s has no entry for %q", ChecksumsAssetName, assetName)
}

// httpGetLimited downloads url into memory, failing if the body exceeds limit bytes.
func httpGetLimited(ctx context.Context, url string, limit int64, timeout time.Duration) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "prot-updater")

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("download exceeds the %d byte limit", limit)
	}
	return data, nil
}

// DownloadAsset downloads the release archive, verifies it against expectedSHA256 and only then extracts the
// prot and protd binaries. Nothing from an unverified archive is ever decompressed.
func DownloadAsset(ctx context.Context, downloadURL, expectedSHA256 string) ([]byte, []byte, error) {
	if expectedSHA256 == "" {
		return nil, nil, errors.New("expected checksum is required")
	}

	archive, err := httpGetLimited(ctx, downloadURL, maxArchiveBytes, 2*time.Minute)
	if err != nil {
		return nil, nil, fmt.Errorf("downloading release archive: %w", err)
	}

	sum := sha256.Sum256(archive)
	if got := hex.EncodeToString(sum[:]); got != strings.ToLower(expectedSHA256) {
		return nil, nil, fmt.Errorf("%w: archive is %s, expected %s", ErrChecksumMismatch, got, expectedSHA256)
	}

	return extractBinaries(archive)
}

func extractBinaries(archive []byte) ([]byte, []byte, error) {
	gzReader, err := gzip.NewReader(bytes.NewReader(archive))
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

		switch filepath.Base(header.Name) {
		case "prot":
			protData, err = readBinary(tarReader, "prot")
		case "protd":
			protdData, err = readBinary(tarReader, "protd")
		default:
			continue
		}
		if err != nil {
			return nil, nil, err
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

func readBinary(r io.Reader, name string) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxBinaryBytes+1))
	if err != nil {
		return nil, fmt.Errorf("extracting %s binary: %w", name, err)
	}
	if int64(len(data)) > maxBinaryBytes {
		return nil, fmt.Errorf("%s binary exceeds the %d byte limit", name, maxBinaryBytes)
	}
	return data, nil
}

// ApplyUpdate writes the new binaries atomically using rename to avoid ETXTBSY.
func ApplyUpdate(protBytes, protdBytes []byte, targetDir string) error {
	if targetDir == "" {
		if owner := PackageOwner(context.Background()); owner != nil {
			return fmt.Errorf("%w (%s package %q)", ErrPackageManaged, owner.Manager, owner.Package)
		}
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

	// Stage both binaries completely before replacing either, so a full disk or a permission error cannot leave
	// a new prot next to an old protd.
	protStaged, err := stageFile(protDest, protBytes)
	if err != nil {
		return fmt.Errorf("staging prot binary at %q: %w", protDest, err)
	}
	defer protStaged.discard()

	protdStaged, err := stageFile(protdDest, protdBytes)
	if err != nil {
		return fmt.Errorf("staging protd binary at %q: %w", protdDest, err)
	}
	defer protdStaged.discard()

	if err := protStaged.commit(); err != nil {
		return fmt.Errorf("updating prot binary at %q: %w", protDest, err)
	}
	if err := protdStaged.commit(); err != nil {
		return fmt.Errorf("prot was updated but protd could not be replaced at %q: %w", protdDest, err)
	}
	return nil
}

// stagedFile is an executable written next to its destination, waiting to be renamed into place.
type stagedFile struct {
	tmpPath  string
	destPath string
}

func stageFile(destPath string, data []byte) (*stagedFile, error) {
	dir := filepath.Dir(destPath)
	tmpFile, err := os.CreateTemp(dir, "prot-update-*")
	if err != nil {
		return nil, fmt.Errorf("creating temp file in %q: %w", dir, err)
	}
	staged := &stagedFile{tmpPath: tmpFile.Name(), destPath: destPath}

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		staged.discard()
		return nil, fmt.Errorf("writing update data: %w", err)
	}
	if err := tmpFile.Chmod(0o755); err != nil {
		_ = tmpFile.Close()
		staged.discard()
		return nil, fmt.Errorf("setting permissions: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		staged.discard()
		return nil, err
	}
	return staged, nil
}

// commit atomically replaces the destination, which works even while the old binary is running.
func (f *stagedFile) commit() error {
	if err := os.Rename(f.tmpPath, f.destPath); err != nil {
		return fmt.Errorf("replacing binary via rename: %w", err)
	}
	return nil
}

// discard removes the temp file if it was not committed. It is safe to call after commit.
func (f *stagedFile) discard() {
	_ = os.Remove(f.tmpPath)
}

func writeAtomic(destPath string, data []byte) error {
	staged, err := stageFile(destPath, data)
	if err != nil {
		return err
	}
	defer staged.discard()
	return staged.commit()
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
