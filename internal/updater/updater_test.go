package updater

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestWriteAtomic(t *testing.T) {
	tempDir := t.TempDir()
	targetFile := filepath.Join(tempDir, "mybinary")

	// 1. Initial write
	initialData := []byte("v1 content")
	if err := writeAtomic(targetFile, initialData); err != nil {
		t.Fatalf("writeAtomic initial failed: %v", err)
	}

	content, _ := os.ReadFile(targetFile)
	if string(content) != "v1 content" {
		t.Errorf("expected v1 content, got %s", content)
	}

	info, _ := os.Stat(targetFile)
	if info.Mode().Perm() != 0755 {
		t.Errorf("expected 0755 permissions, got %o", info.Mode().Perm())
	}

	// 2. Atomic replacement
	v2Data := []byte("v2 content updated")
	if err := writeAtomic(targetFile, v2Data); err != nil {
		t.Fatalf("writeAtomic update failed: %v", err)
	}

	content2, _ := os.ReadFile(targetFile)
	if string(content2) != "v2 content updated" {
		t.Errorf("expected v2 content, got %s", content2)
	}
}

func TestFindAssetForPlatform(t *testing.T) {
	expectedName := "prot_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz"
	rel := &ReleaseInfo{
		TagName: "v0.2.0",
		Assets: []AssetInfo{
			{Name: "prot_darwin_arm64.tar.gz", DownloadURL: "https://example.com/darwin"},
			{Name: expectedName, DownloadURL: "https://example.com/match"},
			{Name: "checksums.txt", DownloadURL: "https://example.com/sum"},
		},
	}

	asset, err := FindAssetForPlatform(rel)
	if err != nil {
		t.Fatalf("FindAssetForPlatform failed: %v", err)
	}
	if asset.Name != expectedName {
		t.Errorf("expected %s, got %s", expectedName, asset.Name)
	}
}

func TestDownloadAndExtractAsset(t *testing.T) {
	// Create an in-memory tar.gz archive with prot and protd binaries
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	files := []struct {
		name string
		body string
	}{
		{"prot", "binary data prot"},
		{"protd", "binary data protd"},
		{"README.md", "ignore this"},
	}

	for _, f := range files {
		hdr := &tar.Header{
			Name: f.name,
			Mode: 0755,
			Size: int64(len(f.body)),
		}
		_ = tw.WriteHeader(hdr)
		_, _ = tw.Write([]byte(f.body))
	}

	_ = tw.Close()
	_ = gw.Close()

	// Spin up test server serving the archive
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(buf.Bytes())
	}))
	defer ts.Close()

	ctx := context.Background()
	sum := sha256.Sum256(buf.Bytes())
	protBytes, protdBytes, err := DownloadAsset(ctx, ts.URL, hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatalf("DownloadAsset failed: %v", err)
	}

	if string(protBytes) != "binary data prot" {
		t.Errorf("unexpected protBytes: %s", protBytes)
	}
	if string(protdBytes) != "binary data protd" {
		t.Errorf("unexpected protdBytes: %s", protdBytes)
	}
}

func TestPackageOwnerOf(t *testing.T) {
	orig := runCommand
	defer func() { runCommand = orig }()

	fake := func(out string, err error) {
		runCommand = func(_ context.Context, _ string, _ ...string) (string, error) { return out, err }
	}

	// Whichever manager is installed on the test machine is the one the fake answers for.
	fake("prot", nil)
	if o := packageOwnerOf(context.Background(), "/usr/bin/prot"); o != nil {
		if o.Package != "prot" || o.UpdateHint() == "" || o.Message() == "" {
			t.Errorf("unexpected owner: %+v", o)
		}
	}

	fake("", errors.New("no package owns /home/x/.local/bin/prot"))
	if o := packageOwnerOf(context.Background(), "/home/x/.local/bin/prot"); o != nil {
		t.Errorf("expected nil owner for unowned file, got %+v", o)
	}
}

func TestOwnerParsers(t *testing.T) {
	for _, q := range ownerQueries {
		switch q.manager {
		case "pacman", "rpm":
			if got := q.parse("prot\n"); got != "prot" {
				t.Errorf("%s parse = %q", q.manager, got)
			}
		case "dpkg":
			if got := q.parse("prot: /usr/bin/prot"); got != "prot" {
				t.Errorf("dpkg parse = %q", got)
			}
		}
	}
}

func TestOwnerHints(t *testing.T) {
	for manager, want := range map[string]string{"pacman": "yay -Syu prot", "dpkg": "apt install", "rpm": "dnf upgrade"} {
		if h := (Owner{Manager: manager, Package: "prot"}).UpdateHint(); !strings.Contains(h, want) {
			t.Errorf("%s hint %q missing %q", manager, h, want)
		}
	}
}

func buildArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0755, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	_ = gw.Close()
	return buf.Bytes()
}

func sha(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func serve(t *testing.T, routes map[string][]byte) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := routes[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(ts.Close)
	return ts
}

// A tampered archive must be rejected before any of it is decompressed or written anywhere.
func TestDownloadAssetRejectsChecksumMismatch(t *testing.T) {
	archive := buildArchive(t, map[string]string{"prot": "evil", "protd": "evil"})
	ts := serve(t, map[string][]byte{"/a.tar.gz": archive})

	published := sha([]byte("the archive the maintainers really built"))
	prot, protd, err := DownloadAsset(context.Background(), ts.URL+"/a.tar.gz", published)
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("expected ErrChecksumMismatch, got %v", err)
	}
	if prot != nil || protd != nil {
		t.Error("binaries must not be returned for a mismatched archive")
	}
}

func TestDownloadAssetRequiresAChecksum(t *testing.T) {
	if _, _, err := DownloadAsset(context.Background(), "http://127.0.0.1:1/x", ""); err == nil {
		t.Error("an empty expected checksum must be refused, not skipped")
	}
}

func TestDownloadAssetCorruptArchiveWithValidChecksum(t *testing.T) {
	garbage := []byte("definitely not gzip")
	ts := serve(t, map[string][]byte{"/a.tar.gz": garbage})
	if _, _, err := DownloadAsset(context.Background(), ts.URL+"/a.tar.gz", sha(garbage)); err == nil {
		t.Error("expected a decompression error")
	}
}

func TestParseChecksum(t *testing.T) {
	good := sha([]byte("x"))
	manifest := []byte(fmt.Sprintf("%s  prot_linux_amd64.tar.gz\n%s *prot_darwin_arm64.tar.gz\nnot a line\n", good, good))

	for _, name := range []string{"prot_linux_amd64.tar.gz", "prot_darwin_arm64.tar.gz"} {
		if got, err := parseChecksum(manifest, name); err != nil || got != good {
			t.Errorf("parseChecksum(%q) = %q, %v", name, got, err)
		}
	}
	if _, err := parseChecksum(manifest, "prot_freebsd_amd64.tar.gz"); err == nil {
		t.Error("missing entry must be an error")
	}
	if _, err := parseChecksum([]byte("zzzz  a.tar.gz\n"), "a.tar.gz"); err == nil {
		t.Error("malformed hash must be an error")
	}
}

func TestExpectedChecksumFailsClosedWithoutManifest(t *testing.T) {
	rel := &ReleaseInfo{TagName: "v9.9.9", Assets: []AssetInfo{{Name: "prot_linux_amd64.tar.gz"}}}
	_, err := ExpectedChecksum(context.Background(), rel, "prot_linux_amd64.tar.gz")
	if err == nil || !strings.Contains(err.Error(), "refusing to install unverified") {
		t.Errorf("expected a fail-closed error, got %v", err)
	}
}

func TestExpectedChecksumReadsManifest(t *testing.T) {
	want := sha([]byte("archive"))
	ts := serve(t, map[string][]byte{"/checksums.txt": []byte(want + "  prot_linux_amd64.tar.gz\n")})
	manifest := AssetInfo{Name: ChecksumsAssetName, DownloadURL: ts.URL + "/checksums.txt"}
	rel := &ReleaseInfo{TagName: "v1", Assets: []AssetInfo{manifest}}

	got, err := ExpectedChecksum(context.Background(), rel, "prot_linux_amd64.tar.gz")
	if err != nil || got != want {
		t.Errorf("ExpectedChecksum = %q, %v; want %q", got, err, want)
	}
}

func TestHTTPGetLimitedEnforcesLimit(t *testing.T) {
	ts := serve(t, map[string][]byte{"/big": bytes.Repeat([]byte("a"), 2048)})
	if _, err := httpGetLimited(context.Background(), ts.URL+"/big", 1024, 5*time.Second); err == nil {
		t.Error("oversized body must be rejected")
	}
	data, err := httpGetLimited(context.Background(), ts.URL+"/big", 4096, 5*time.Second)
	if err != nil || len(data) != 2048 {
		t.Errorf("within limit: %d bytes, %v", len(data), err)
	}
	if _, err := httpGetLimited(context.Background(), ts.URL+"/missing", 1024, 5*time.Second); err == nil {
		t.Error("404 must be an error")
	}
}

func TestApplyUpdateLeavesNothingBehindAndReplacesBoth(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "prot"), []byte("old prot"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "protd"), []byte("old protd"), 0o755)

	if err := ApplyUpdate([]byte("new prot"), []byte("new protd"), dir); err != nil {
		t.Fatalf("ApplyUpdate: %v", err)
	}
	for name, want := range map[string]string{"prot": "new prot", "protd": "new protd"} {
		if got, _ := os.ReadFile(filepath.Join(dir, name)); string(got) != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Errorf("temp files left behind: %v", entries)
	}
}

// An unwritable install directory must fail cleanly and leave the existing binary untouched.
func TestApplyUpdateFailsCleanlyWhenDirectoryIsReadOnly(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "prot"), []byte("old prot"), 0o755)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Skip("cannot make directory read-only")
	}
	defer os.Chmod(dir, 0o755)
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}

	if err := ApplyUpdate([]byte("new prot"), []byte("new protd"), dir); err == nil {
		t.Fatal("expected an error when the install directory is not writable")
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "prot")); string(got) != "old prot" {
		t.Errorf("prot was modified despite the failed update: %q", got)
	}
}
