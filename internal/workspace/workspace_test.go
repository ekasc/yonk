package workspace

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchiveRoundTripIncludesWorkingTreeAndExcludesDefaults(t *testing.T) {
	source := t.TempDir()
	writeTestFile(t, filepath.Join(source, "src", "message.txt"), "uncommitted change")
	writeTestFile(t, filepath.Join(source, "node_modules", "dependency.js"), "excluded")
	writeTestFile(t, filepath.Join(source, ".git", "config"), "excluded")
	writeTestFile(t, filepath.Join(source, "custom", "skip.txt"), "excluded")

	archive, err := CreateArchive(context.Background(), source, append(DefaultExclusions(), "custom"))
	if err != nil {
		t.Fatalf("CreateArchive() error = %v", err)
	}
	defer archive.Remove()
	if archive.Stats.Files != 2 {
		t.Fatalf("archive files = %d, want 2", archive.Stats.Files)
	}
	archiveFile, err := archive.Open()
	if err != nil {
		t.Fatalf("Archive.Open() error = %v", err)
	}
	defer archiveFile.Close()

	destination := t.TempDir()
	stats, err := Extract(context.Background(), archiveFile, destination, Limits{MaxFiles: 10, MaxUncompressedBytes: 1 << 20})
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	content, err := os.ReadFile(filepath.Join(destination, "src", "message.txt"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(content) != "uncommitted change" {
		t.Fatalf("content = %q", content)
	}
	for _, excluded := range []string{"node_modules", ".git", "custom"} {
		if _, err := os.Stat(filepath.Join(destination, excluded)); !os.IsNotExist(err) {
			t.Fatalf("excluded path %q exists or returned unexpected error: %v", excluded, err)
		}
	}
	if stats.UncompressedBytes != int64(len(content)) || stats.CompressedBytes < 1 {
		t.Fatalf("Extract() stats = %+v", stats)
	}
}

func TestExtractRejectsUnsafeArchives(t *testing.T) {
	tests := []struct {
		name     string
		header   tar.Header
		want     string
		maxBytes int64
	}{
		{
			name:     "path traversal",
			header:   tar.Header{Name: "../escape", Mode: 0o600, Size: 1, Typeflag: tar.TypeReg},
			want:     "unsafe path",
			maxBytes: 100,
		},
		{
			name:     "absolute symlink",
			header:   tar.Header{Name: "link", Linkname: "/etc/passwd", Typeflag: tar.TypeSymlink},
			want:     "link target must be relative",
			maxBytes: 100,
		},
		{
			name:     "expanded size",
			header:   tar.Header{Name: "large", Mode: 0o600, Size: 10, Typeflag: tar.TypeReg},
			want:     "expanded size limit",
			maxBytes: 5,
		},
		{
			name:     "special file",
			header:   tar.Header{Name: "device", Typeflag: tar.TypeChar},
			want:     "unsupported archive type",
			maxBytes: 100,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			archive := maliciousArchive(t, test.header)
			_, err := Extract(context.Background(), bytes.NewReader(archive), t.TempDir(), Limits{MaxFiles: 10, MaxUncompressedBytes: test.maxBytes})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Extract() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestExtractRejectsFileCountLimit(t *testing.T) {
	var compressed bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressed)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, name := range []string{"one", "two"} {
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Typeflag: tar.TypeReg}); err != nil {
			t.Fatalf("WriteHeader() error = %v", err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatalf("tar Close() error = %v", err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("gzip Close() error = %v", err)
	}

	_, err := Extract(context.Background(), bytes.NewReader(compressed.Bytes()), t.TempDir(), Limits{MaxFiles: 1, MaxUncompressedBytes: 100})
	if err == nil || !strings.Contains(err.Error(), "file limit") {
		t.Fatalf("Extract() error = %v", err)
	}
}

func maliciousArchive(t *testing.T, header tar.Header) []byte {
	t.Helper()
	var compressed bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressed)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&header); err != nil {
		t.Fatalf("WriteHeader() error = %v", err)
	}
	if header.Size > 0 {
		if _, err := tarWriter.Write(make([]byte, header.Size)); err != nil {
			t.Fatalf("tar Write() error = %v", err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatalf("tar Close() error = %v", err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("gzip Close() error = %v", err)
	}
	return compressed.Bytes()
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}
