// Package workspace packages local working trees and safely extracts them on workers.
package workspace

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

var defaultExclusions = []string{
	".git",
	"node_modules",
	"dist",
	"build",
	".next",
	"coverage",
}

// DefaultExclusions returns directory names omitted from workspace archives.
func DefaultExclusions() []string {
	return append([]string(nil), defaultExclusions...)
}

// Stats describes a packaged or extracted workspace.
type Stats struct {
	Files             int
	UncompressedBytes int64
	CompressedBytes   int64
}

// Workspace is an extracted working tree owned by the worker job lifecycle.
type Workspace struct {
	Root  string
	Stats Stats
}

// Archive is a temporary compressed workspace archive owned by the caller.
type Archive struct {
	path  string
	Stats Stats
}

// Open opens the archive for reading.
func (a *Archive) Open() (*os.File, error) {
	file, err := os.Open(a.path)
	if err != nil {
		return nil, fmt.Errorf("open workspace archive: %w", err)
	}
	return file, nil
}

// Remove deletes the temporary archive.
func (a *Archive) Remove() error {
	if a == nil || a.path == "" {
		return nil
	}
	if err := os.Remove(a.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove workspace archive: %w", err)
	}
	return nil
}

// CreateArchive packages root as a gzip-compressed tar archive. Exclusions are
// matched by path component; symlinks are archived but never followed.
func CreateArchive(ctx context.Context, root string, exclusions []string) (*Archive, error) {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace root: %w", err)
	}
	info, err := os.Stat(absoluteRoot)
	if err != nil {
		return nil, fmt.Errorf("stat workspace root: %w", err)
	}
	if !info.IsDir() {
		return nil, errors.New("workspace root is not a directory")
	}

	file, err := os.CreateTemp("", "yonk-workspace-*.tar.gz")
	if err != nil {
		return nil, fmt.Errorf("create workspace archive: %w", err)
	}
	archive := &Archive{path: file.Name()}
	keep := false
	defer func() {
		_ = file.Close()
		if !keep {
			_ = archive.Remove()
		}
	}()

	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	walkErr := filepath.WalkDir(absoluteRoot, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if current == absoluteRoot {
			return nil
		}

		relative, err := filepath.Rel(absoluteRoot, current)
		if err != nil {
			return fmt.Errorf("make workspace path relative: %w", err)
		}
		archiveName := filepath.ToSlash(relative)
		if isExcluded(archiveName, exclusions) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		entryInfo, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stat workspace entry %q: %w", archiveName, err)
		}
		mode := entryInfo.Mode()
		if !mode.IsRegular() && !mode.IsDir() && mode&os.ModeSymlink == 0 {
			return fmt.Errorf("workspace entry %q has unsupported file type %s", archiveName, mode.Type())
		}

		linkTarget := ""
		if mode&os.ModeSymlink != 0 {
			linkTarget, err = os.Readlink(current)
			if err != nil {
				return fmt.Errorf("read workspace symlink %q: %w", archiveName, err)
			}
		}
		header, err := tar.FileInfoHeader(entryInfo, linkTarget)
		if err != nil {
			return fmt.Errorf("create archive header for %q: %w", archiveName, err)
		}
		header.Name = archiveName
		header.Uid = 0
		header.Gid = 0
		header.Uname = ""
		header.Gname = ""
		if err := tarWriter.WriteHeader(header); err != nil {
			return fmt.Errorf("write archive header for %q: %w", archiveName, err)
		}
		archive.Stats.Files++

		if !mode.IsRegular() {
			return nil
		}
		source, err := os.Open(current)
		if err != nil {
			return fmt.Errorf("open workspace file %q: %w", archiveName, err)
		}
		written, copyErr := io.Copy(tarWriter, &contextReader{ctx: ctx, reader: source})
		closeErr := source.Close()
		if copyErr != nil {
			return fmt.Errorf("archive workspace file %q: %w", archiveName, copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close workspace file %q: %w", archiveName, closeErr)
		}
		archive.Stats.UncompressedBytes += written
		return nil
	})
	if walkErr != nil {
		_ = tarWriter.Close()
		_ = gzipWriter.Close()
		return nil, fmt.Errorf("walk workspace: %w", walkErr)
	}
	if err := tarWriter.Close(); err != nil {
		_ = gzipWriter.Close()
		return nil, fmt.Errorf("finish workspace tar archive: %w", err)
	}
	if err := gzipWriter.Close(); err != nil {
		return nil, fmt.Errorf("finish workspace compression: %w", err)
	}
	if err := file.Sync(); err != nil {
		return nil, fmt.Errorf("sync workspace archive: %w", err)
	}
	fileInfo, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat workspace archive: %w", err)
	}
	archive.Stats.CompressedBytes = fileInfo.Size()
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close workspace archive: %w", err)
	}
	keep = true
	return archive, nil
}

// Limits are provider-controlled extraction limits.
type Limits struct {
	MaxFiles             int
	MaxUncompressedBytes int64
}

// Extract unpacks a gzip-compressed tar stream into an existing empty
// destination directory without allowing paths or links to escape it.
func Extract(ctx context.Context, source io.Reader, destination string, limits Limits) (Stats, error) {
	if limits.MaxFiles < 1 || limits.MaxUncompressedBytes < 1 {
		return Stats{}, errors.New("workspace extraction limits must be positive")
	}
	if err := ensureEmptyDirectory(destination); err != nil {
		return Stats{}, err
	}

	counter := &countingReader{reader: source}
	bufferedSource := bufio.NewReader(counter)
	gzipReader, err := gzip.NewReader(bufferedSource)
	if err != nil {
		return Stats{}, fmt.Errorf("open workspace compression stream: %w", err)
	}
	gzipReader.Multistream(false)
	tarReader := tar.NewReader(gzipReader)
	seen := make(map[string]struct{})
	stats := Stats{}

	for {
		if err := ctx.Err(); err != nil {
			_ = gzipReader.Close()
			return stats, err
		}
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			_ = gzipReader.Close()
			return stats, fmt.Errorf("read workspace archive: %w", err)
		}
		cleanName, err := safeArchivePath(header.Name)
		if err != nil {
			_ = gzipReader.Close()
			return stats, err
		}
		if _, exists := seen[cleanName]; exists {
			_ = gzipReader.Close()
			return stats, fmt.Errorf("workspace archive contains duplicate path %q", cleanName)
		}
		seen[cleanName] = struct{}{}
		stats.Files++
		if stats.Files > limits.MaxFiles {
			_ = gzipReader.Close()
			return stats, fmt.Errorf("workspace exceeds file limit of %d", limits.MaxFiles)
		}
		if header.Size < 0 || header.Size > limits.MaxUncompressedBytes-stats.UncompressedBytes {
			_ = gzipReader.Close()
			return stats, fmt.Errorf("workspace exceeds expanded size limit of %d bytes", limits.MaxUncompressedBytes)
		}

		target := filepath.Join(destination, filepath.FromSlash(cleanName))
		if err := ensureSafeParents(destination, filepath.Dir(target)); err != nil {
			_ = gzipReader.Close()
			return stats, fmt.Errorf("prepare workspace path %q: %w", cleanName, err)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.Mkdir(target, fs.FileMode(header.Mode).Perm()); err != nil && !errors.Is(err, os.ErrExist) {
				_ = gzipReader.Close()
				return stats, fmt.Errorf("create workspace directory %q: %w", cleanName, err)
			}
		case tar.TypeReg, tar.TypeRegA:
			file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, fs.FileMode(header.Mode).Perm())
			if err != nil {
				_ = gzipReader.Close()
				return stats, fmt.Errorf("create workspace file %q: %w", cleanName, err)
			}
			written, copyErr := io.CopyN(file, &contextReader{ctx: ctx, reader: tarReader}, header.Size)
			closeErr := file.Close()
			if copyErr != nil {
				_ = gzipReader.Close()
				return stats, fmt.Errorf("extract workspace file %q: %w", cleanName, copyErr)
			}
			if closeErr != nil {
				_ = gzipReader.Close()
				return stats, fmt.Errorf("close workspace file %q: %w", cleanName, closeErr)
			}
			stats.UncompressedBytes += written
		case tar.TypeSymlink:
			if err := validateSymlink(destination, target, header.Linkname); err != nil {
				_ = gzipReader.Close()
				return stats, fmt.Errorf("unsafe workspace symlink %q: %w", cleanName, err)
			}
			if err := os.Symlink(header.Linkname, target); err != nil {
				_ = gzipReader.Close()
				return stats, fmt.Errorf("create workspace symlink %q: %w", cleanName, err)
			}
		default:
			_ = gzipReader.Close()
			return stats, fmt.Errorf("workspace path %q has unsupported archive type %d", cleanName, header.Typeflag)
		}
	}
	var trailing [1]byte
	count, err := gzipReader.Read(trailing[:])
	if count != 0 || !errors.Is(err, io.EOF) {
		_ = gzipReader.Close()
		if err == nil {
			return stats, errors.New("workspace archive contains data after the tar end marker")
		}
		return stats, fmt.Errorf("finish workspace compression stream: %w", err)
	}
	if err := gzipReader.Close(); err != nil {
		return stats, fmt.Errorf("close workspace compression stream: %w", err)
	}
	if _, err := bufferedSource.Peek(1); err == nil {
		return stats, errors.New("workspace archive contains trailing compressed data")
	} else if !errors.Is(err, io.EOF) {
		return stats, fmt.Errorf("check workspace archive ending: %w", err)
	}
	stats.CompressedBytes = counter.count
	return stats, nil
}

func isExcluded(name string, exclusions []string) bool {
	components := strings.Split(path.Clean(name), "/")
	for _, exclusion := range exclusions {
		clean := strings.Trim(path.Clean(filepath.ToSlash(exclusion)), "/")
		if clean == "" || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
			continue
		}
		if strings.Contains(clean, "/") {
			if name == clean || strings.HasPrefix(name, clean+"/") {
				return true
			}
			continue
		}
		for _, component := range components {
			if component == clean {
				return true
			}
		}
	}
	return false
}

func safeArchivePath(name string) (string, error) {
	if name == "" || strings.ContainsRune(name, '\x00') || path.IsAbs(name) {
		return "", fmt.Errorf("workspace archive contains unsafe path %q", name)
	}
	clean := path.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("workspace archive contains unsafe path %q", name)
	}
	return clean, nil
}

func ensureEmptyDirectory(destination string) error {
	info, err := os.Stat(destination)
	if err != nil {
		return fmt.Errorf("stat workspace destination: %w", err)
	}
	if !info.IsDir() {
		return errors.New("workspace destination is not a directory")
	}
	entries, err := os.ReadDir(destination)
	if err != nil {
		return fmt.Errorf("read workspace destination: %w", err)
	}
	if len(entries) != 0 {
		return errors.New("workspace destination is not empty")
	}
	return nil
}

func ensureSafeParents(root, parent string) error {
	relative, err := filepath.Rel(root, parent)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("path escapes workspace")
	}
	current := root
	if relative == "." {
		return nil
	}
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o755); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("parent is not a real directory")
		}
	}
	return nil
}

func validateSymlink(root, target, link string) error {
	if link == "" || filepath.IsAbs(link) {
		return errors.New("link target must be relative")
	}
	resolved := filepath.Clean(filepath.Join(filepath.Dir(target), filepath.FromSlash(link)))
	relative, err := filepath.Rel(root, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("link target escapes workspace")
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

type countingReader struct {
	reader io.Reader
	count  int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	count, err := r.reader.Read(p)
	r.count += int64(count)
	return count, err
}
