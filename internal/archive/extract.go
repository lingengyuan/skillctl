package archive

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/lingengyuan/skillctl/internal/fsutil"
	"github.com/lingengyuan/skillctl/internal/skilldoc"
)

// Unpack validates and extracts ZIP, tar.gz or SKILL.md content.
// The caller supplies a fresh destination directory.
func Unpack(body []byte, destination string) error {
	if bytes.HasPrefix(body, []byte("PK\x03\x04")) {
		archive, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
		if err != nil {
			return err
		}
		if len(archive.File) > maxFiles {
			return fmt.Errorf("archive contains too many files")
		}
		var total uint64
		for _, entry := range archive.File {
			clean, safe := entryPath(entry.Name)
			if !safe || !fsutil.Within(destination, filepath.Join(destination, clean)) {
				return fmt.Errorf("archive contains unsafe path")
			}
			if entry.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("archive contains unsupported symlink")
			}
			target := filepath.Join(destination, clean)
			if entry.FileInfo().IsDir() {
				if err := os.MkdirAll(target, 0o755); err != nil {
					return err
				}
				continue
			}
			if entry.UncompressedSize64 > MaxBytes || total+entry.UncompressedSize64 > MaxBytes {
				return fmt.Errorf("archive exceeds unpacked size limit")
			}
			total += entry.UncompressedSize64
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			input, err := entry.Open()
			if err != nil {
				return err
			}
			mode := entry.Mode().Perm()
			if mode == 0 {
				mode = 0o644
			}
			output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				input.Close()
				return err
			}
			_, copyErr := io.CopyN(output, input, int64(entry.UncompressedSize64))
			closeErr := errors.Join(input.Close(), output.Close())
			if err := errors.Join(copyErr, closeErr); err != nil {
				return err
			}
		}
		return nil
	}
	if bytes.HasPrefix(body, []byte{0x1f, 0x8b}) {
		return TarGzip(body, destination)
	}
	if _, err := skilldoc.FrontMatter(body); err != nil {
		return fmt.Errorf("expected a SKILL.md, ZIP or tar.gz artifact")
	}
	return os.WriteFile(filepath.Join(destination, "SKILL.md"), body, 0o644)
}

// Archive names use slash separators on every host. Validate them before OS
// path conversion so a Unix absolute name, Windows drive, or alternate stream
// cannot become an apparently relative name on another platform.
func entryPath(name string) (string, bool) {
	if strings.HasPrefix(name, "/") || strings.ContainsAny(name, `\:`) {
		return "", false
	}
	clean := filepath.FromSlash(path.Clean(name))
	return clean, clean != "." && filepath.IsLocal(clean)
}

// MaxBytes limits downloaded and unpacked artifact content to 50 MiB.
const MaxBytes = 50 << 20

const maxFiles = 1000

// TarGzip extracts a tar.gz artifact with path, entry-type and size validation.
func TarGzip(data []byte, destination string) error {
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("open well-known archive: %w", err)
	}
	defer reader.Close()

	archive := tar.NewReader(reader)
	files := 0
	var unpacked int64
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read well-known archive: %w", err)
		}
		files++
		if files > maxFiles {
			return fmt.Errorf("well-known archive contains too many files")
		}
		if header.Size < 0 || unpacked+header.Size > MaxBytes {
			return fmt.Errorf("well-known archive exceeds unpacked size limit")
		}
		unpacked += header.Size
		clean, safe := entryPath(header.Name)
		if !safe {
			return fmt.Errorf("well-known archive contains unsafe path")
		}
		path := filepath.Join(destination, clean)
		if !fsutil.Within(destination, path) {
			return fmt.Errorf("well-known archive path escapes destination")
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(header.Mode).Perm()
			if mode == 0 {
				mode = 0o644
			}
			file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				return err
			}
			_, copyErr := io.CopyN(file, archive, header.Size)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return fmt.Errorf("well-known archive contains unsupported entry type")
		}
	}
	return nil
}
