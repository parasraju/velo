package repository

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

// readProbeBytes is the minimum prefix inspected when reading a file. It is
// larger than the visible limit so binary detection still sees the same bytes
// as before even when the caller asks for a tiny range.
const readProbeBytes = 8192

// hashChunkBytes bounds the buffer used while hashing a file in one pass.
const hashChunkBytes = 64 << 10

// openRegular validates a workspace-relative path and opens it for reading. It
// returns the file, its clean path, and its size.
func (r *Repository) openRegular(rel string) (*os.File, string, int64, error) {
	clean, err := r.normalize(rel)
	if err != nil {
		return nil, "", 0, err
	}
	if isSecretPath(clean) {
		return nil, "", 0, fmt.Errorf("%w: %q", ErrSecret, clean)
	}
	abs, err := r.safeAbs(clean)
	if err != nil {
		return nil, "", 0, err
	}
	info, err := os.Lstat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", 0, fmt.Errorf("%w: %q", ErrNotFound, clean)
		}
		return nil, "", 0, fmt.Errorf("repository: stat %q: %w", clean, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, "", 0, fmt.Errorf("%w: %q", ErrNotFile, clean)
	}
	f, err := os.Open(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", 0, fmt.Errorf("%w: %q", ErrNotFound, clean)
		}
		return nil, "", 0, fmt.Errorf("repository: open %q: %w", clean, err)
	}
	return f, clean, info.Size(), nil
}

// readLimit resolves a caller-supplied byte limit against the repository
// defaults and caps.
func (r *Repository) readLimit(maxBytes int64) int64 {
	limit := maxBytes
	if limit <= 0 {
		limit = r.maxFileBytes
	}
	if limit <= 0 {
		limit = defaultMaxFileBytes
	}
	if limit > maxMaxFileBytes {
		limit = maxMaxFileBytes
	}
	return limit
}

// clampVisible limits a planned read to the bytes actually obtained. A file
// can shrink between stat and read; slicing the shorter buffer with the old
// size panics.
func clampVisible(visible int64, n int) int64 {
	if visible < 0 {
		visible = 0
	}
	if n < 0 {
		n = 0
	}
	if visible > int64(n) {
		return int64(n)
	}
	return visible
}

// visibleSize returns how many bytes of a file of the given size are returned
// to the caller.
func visibleSize(size, limit int64) int64 {
	if size < limit {
		return size
	}
	return limit
}

// probeSize returns how many leading bytes are inspected for binary content.
func probeSize(size, limit int64) int64 {
	probe := limit
	if probe < readProbeBytes {
		probe = readProbeBytes
	}
	if probe > size {
		probe = size
	}
	return probe
}

func (r *Repository) ReadFile(rel string, maxBytes int64) (File, error) {
	f, clean, size, err := r.openRegular(rel)
	if err != nil {
		return File{}, err
	}
	limit := r.readLimit(maxBytes)
	visible := visibleSize(size, limit)
	content := make([]byte, probeSize(size, limit))
	n, readErr := io.ReadFull(f, content)
	closeErr := f.Close()
	if readErr != nil && readErr != io.ErrUnexpectedEOF && readErr != io.EOF {
		return File{}, fmt.Errorf("repository: read %q: %w", clean, readErr)
	}
	if closeErr != nil {
		return File{}, fmt.Errorf("repository: close %q: %w", clean, closeErr)
	}
	content = content[:n]
	visible = clampVisible(visible, len(content))
	file := File{
		Path:      clean,
		Content:   content[:visible],
		Size:      size,
		Binary:    contentHasNUL(content),
		Truncated: size > int64(len(content)) || size > visible,
	}
	if file.Binary {
		return file, fmt.Errorf("%w: %q", ErrBinary, clean)
	}
	return file, nil
}

// ReadFileHashed reads a file once and returns its visible content together
// with the SHA-256 of the complete file. Callers that need both used to open
// and read the file twice; the digest must cover the whole file even when the
// returned content is truncated, because write tools compare it against
// HashFile.
func (r *Repository) ReadFileHashed(rel string, maxBytes int64) (File, string, error) {
	f, clean, size, err := r.openRegular(rel)
	if err != nil {
		return File{}, "", err
	}
	limit := r.readLimit(maxBytes)
	visible := visibleSize(size, limit)
	probe := make([]byte, probeSize(size, limit))
	kept := 0
	hash := sha256.New()
	chunk := make([]byte, hashChunkBytes)
	var readErr error
	for {
		var n int
		n, readErr = f.Read(chunk)
		if n > 0 {
			_, _ = hash.Write(chunk[:n])
			if kept < len(probe) {
				kept += copy(probe[kept:], chunk[:n])
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			break
		}
	}
	closeErr := f.Close()
	if readErr != nil && readErr != io.EOF {
		return File{}, "", fmt.Errorf("repository: read %q: %w", clean, readErr)
	}
	if closeErr != nil {
		return File{}, "", fmt.Errorf("repository: close %q: %w", clean, closeErr)
	}
	probe = probe[:kept]
	visible = clampVisible(visible, len(probe))
	file := File{
		Path:      clean,
		Content:   probe[:visible],
		Size:      size,
		Binary:    contentHasNUL(probe),
		Truncated: size > int64(len(probe)) || size > visible,
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if file.Binary {
		return file, digest, fmt.Errorf("%w: %q", ErrBinary, clean)
	}
	return file, digest, nil
}
