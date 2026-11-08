package updater

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
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
	protBytes, protdBytes, err := DownloadAsset(ctx, ts.URL)
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
