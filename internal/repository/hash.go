package repository

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
)

// HashFile returns the lowercase hex SHA-256 digest of a workspace file. It is
// used by write tools to detect stale content before replacing a file.
func (r *Repository) HashFile(rel string) (string, error) {
	f, clean, _, err := r.openRegular(rel)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, f)
	closeErr := f.Close()
	if copyErr != nil {
		return "", fmt.Errorf("repository: hash %q: %w", clean, copyErr)
	}
	if closeErr != nil {
		return "", fmt.Errorf("repository: close %q: %w", clean, closeErr)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
