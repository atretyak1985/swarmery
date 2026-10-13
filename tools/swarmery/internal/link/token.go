// Package link is the daemon side of the swarmery link: a Claude Code mod that
// POSTs session events to /api/link/* (mods-swarmery-link). This file owns the
// shared secret those requests carry — one token per installation, kept in
// <home>/.swarmery/link-token, which the mod reads with
// `${await $.env.get('HOME')}/.swarmery/link-token` (docs/mods-format.md §M7).
//
// The token value is never logged, by this package or by its callers.
package link

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	// tokenBytes is the entropy of a minted token; it is stored hex-encoded.
	tokenBytes = 32
	// fileMode is the only mode the token file may carry: it is a bearer
	// credential for the daemon's link API.
	fileMode fs.FileMode = 0o600
	// dirMode is used only when <home>/.swarmery does not exist yet; an
	// existing directory keeps the mode it has (it also holds the database and
	// the installed binary, whose layout belongs to internal/installer).
	dirMode fs.FileMode = 0o700
)

// errEmptyToken marks a token file that exists but holds nothing usable; it is
// replaced by a freshly minted token rather than served as "".
var errEmptyToken = errors.New("link token file is empty")

// TokenPath returns <home>/.swarmery/link-token, beside the daemon database
// (internal/installer homeDBPath).
func TokenPath(home string) string {
	return filepath.Join(home, ".swarmery", "link-token")
}

// EnsureToken returns the installation's link token, minting one when the file
// is missing or empty. An existing file whose mode is wider than 0600 is
// narrowed back to 0600 before it is read.
func EnsureToken(home string) (string, error) {
	path := TokenPath(home)
	tok, err := readToken(path)
	switch {
	case err == nil:
		return tok, nil
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, errEmptyToken):
		return writeNewToken(path)
	default:
		return "", err
	}
}

// Rotate replaces the token with a freshly minted one and returns it. A running
// daemon picks the new value up through Holder.Reload; the old value stops
// authenticating from that moment.
func Rotate(home string) (string, error) {
	return writeNewToken(TokenPath(home))
}

// readToken reads the token at path, first narrowing a too-open file to 0600.
func readToken(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("link token: %s is not a regular file", path)
	}
	if info.Mode().Perm()&^fileMode != 0 {
		if err := os.Chmod(path, fileMode); err != nil {
			return "", fmt.Errorf("link token: narrow mode of %s: %w", path, err)
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" {
		return "", errEmptyToken
	}
	return tok, nil
}

// writeNewToken mints a token and installs it at path atomically: it is written
// to a 0600 temp file in the same directory and renamed over the old one, so a
// concurrent reader (the mod, or Holder.Reload) sees either the old token or
// the new one, never a partial write.
func writeNewToken(path string) (string, error) {
	tok, err := mint()
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return "", fmt.Errorf("link token: create %s: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, ".link-token-*")
	if err != nil {
		return "", fmt.Errorf("link token: temp file: %w", err)
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op once the rename has happened
	if err := f.Chmod(fileMode); err != nil {
		f.Close()
		return "", fmt.Errorf("link token: chmod: %w", err)
	}
	if _, err := f.WriteString(tok + "\n"); err != nil {
		f.Close()
		return "", fmt.Errorf("link token: write: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return "", fmt.Errorf("link token: sync: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("link token: close: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", fmt.Errorf("link token: install: %w", err)
	}
	return tok, nil
}

// mint returns tokenBytes of crypto randomness, hex-encoded.
func mint() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("link token: random: %w", err)
	}
	return hex.EncodeToString(b), nil
}
