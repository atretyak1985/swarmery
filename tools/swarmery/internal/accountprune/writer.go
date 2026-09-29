package accountprune

// The prune's one writer: remove whole top-level keys from a settings file and
// leave every other byte exactly where it was.
//
// A map round-trip would re-sort the keys and re-indent the file, and a prune
// whose diff is the whole file is a prune nobody can review. So the writer
// SPLICES: it finds each top-level member's byte span with json.Decoder
// offsets and cuts it out, with its separator, from the raw bytes. The result
// is then checked — it must parse, and it must equal the original minus
// exactly the removed keys — before anything touches the disk.
//
// Before the FIRST write to a path the pre-image is copied to the quarantine
// directory (<Q>/prune/<abs path, '/' → '-'>.bak.json, dir 0700, the file's
// own mode). That copy is the only rollback for a gitignored target, so a
// backup that cannot be made aborts the write. It is out of tree on purpose: a
// .bak sibling would show up in the very `git status --porcelain` the operator
// compares across the run.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// maxFileBytes bounds what the writer reads: settings files are small, and the
// trusted loader already refuses anything larger than its own cap.
const maxFileBytes = 1 << 20

// member is one top-level "key": value pair of an object, as byte offsets into
// the raw document: sepStart is where the previous token ended (so
// raw[sepStart:keyStart] is the whitespace and comma before the key), end is
// just past the value.
type member struct {
	key                     string
	sepStart, keyStart, end int
}

// topLevelMembers parses raw as ONE JSON object and returns its members in
// document order. Anything else — an array, trailing data — is an error.
func topLevelMembers(raw []byte) ([]member, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errors.New("not a JSON object")
	}
	prev := int(dec.InputOffset())
	var out []member
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := kt.(string)
		if !ok {
			return nil, errors.New("malformed object key")
		}
		keyEnd := int(dec.InputOffset())
		q := bytes.IndexByte(raw[prev:keyEnd], '"')
		if q < 0 {
			return nil, errors.New("cannot locate an object key")
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		end := int(dec.InputOffset())
		out = append(out, member{key: key, sepStart: prev, keyStart: prev + q, end: end})
		prev = end
	}
	if _, err := dec.Token(); err != nil { // the closing brace
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("trailing data after the settings object")
	}
	return out, nil
}

// removeTopLevelKeys returns raw without the members named in keys (every
// occurrence), everything else byte-for-byte. A key raw does not carry is not
// an error. An object left with no members becomes "{}".
func removeTopLevelKeys(raw []byte, keys []string) ([]byte, error) {
	drop := map[string]bool{}
	for _, k := range keys {
		drop[k] = true
	}
	out := append([]byte(nil), raw...)
	for {
		ms, err := topLevelMembers(out)
		if err != nil {
			return nil, err
		}
		i := -1
		for j, m := range ms {
			if drop[m.key] {
				i = j
				break
			}
		}
		if i < 0 {
			return out, nil
		}
		m := ms[i]
		var next []byte
		switch {
		case len(ms) == 1:
			// The only member: keep the braces and what follows them.
			rest := out[m.end:]
			brace := bytes.IndexByte(rest, '}')
			next = append(append(append([]byte(nil), out[:m.sepStart]...), '}'), rest[brace+1:]...)
		case i == 0:
			// The first of several: cut from its key to the next key, so the
			// next member inherits this one's indentation.
			next = append(append([]byte(nil), out[:m.keyStart]...), out[ms[1].keyStart:]...)
		default:
			// Any later member: cut its leading separator with it.
			next = append(append([]byte(nil), out[:m.sepStart]...), out[m.end:]...)
		}
		out = next
	}
}

// verifyRemoval is the writer's self-check: after must parse, and must equal
// before minus exactly keys, compared as canonical JSON (encoding/json sorts
// map keys). A splice that got it wrong is caught here, before any write.
func verifyRemoval(before, after []byte, keys []string) error {
	var b, a map[string]any
	if err := json.Unmarshal(before, &b); err != nil {
		return fmt.Errorf("original does not parse: %w", err)
	}
	if err := json.Unmarshal(after, &a); err != nil {
		return fmt.Errorf("rewrite does not parse: %w", err)
	}
	for _, k := range keys {
		delete(b, k)
	}
	bb, _ := json.Marshal(b)
	ab, _ := json.Marshal(a)
	if !bytes.Equal(bb, ab) {
		return errors.New("rewrite differs from the original beyond the removed keys")
	}
	return nil
}

// DefaultQuarantineDir is ~/.swarmery/quarantine/<YYYY-MM-DD of now>: the
// phase's $Q. The prune's pre-images go under its prune/ subdirectory.
func DefaultQuarantineDir(now time.Time) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return "", fmt.Errorf("resolve home directory for the quarantine: %v", err)
	}
	return filepath.Join(home, ".swarmery", "quarantine", now.Format("2006-01-02")), nil
}

// backupName is the pre-image's file name for path: the absolute path with
// every separator replaced by '-' (readable), then the first 12 hex digits of
// the path's SHA-256 (unique: "/a/b-c" and "/a/b/c" mangle alike, and no
// escaping of '-' alone is injective), then .bak.json.
func backupName(path string) string {
	sum := sha256.Sum256([]byte(path))
	return strings.ReplaceAll(filepath.ToSlash(path), "/", "-") + "." + hex.EncodeToString(sum[:])[:12] + ".bak.json"
}

// ensureQuarantine creates <q> and <q>/prune at mode 0700 (and re-asserts
// 0700 on both when they already exist: a quarantine others can read is a
// defect, whoever made it). Returns <q>/prune.
func ensureQuarantine(q string) (string, error) {
	dir := filepath.Join(q, "prune")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create quarantine %s: %w", dir, err)
	}
	for _, d := range []string{q, dir} {
		if err := os.Chmod(d, 0o700); err != nil {
			return "", fmt.Errorf("chmod quarantine %s: %w", d, err)
		}
	}
	return dir, nil
}

// writeBackup copies raw to bak with mode perm, never overwriting: an
// existing pre-image with the SAME bytes is reused (a re-run the same day after
// a rollback), one with DIFFERENT bytes aborts — the earliest pre-image is the
// rollback artifact and nothing may replace it. (Apply's pre-flight refuses
// that case before any write; this is the last guard.)
func writeBackup(bak, path string, raw []byte, perm os.FileMode) error {
	if existing, err := os.ReadFile(bak); err == nil {
		if bytes.Equal(existing, raw) {
			return nil
		}
		return fmt.Errorf("a different pre-image of %s already exists at %s; move it aside before pruning again", path, bak)
	}
	f, err := os.OpenFile(bak, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return fmt.Errorf("write pre-image %s: %w", bak, err)
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		os.Remove(bak)
		return fmt.Errorf("write pre-image %s: %w", bak, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(bak)
		return fmt.Errorf("sync pre-image %s: %w", bak, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(bak)
		return fmt.Errorf("close pre-image %s: %w", bak, err)
	}
	return nil
}

// readForWrite reads path for a rewrite: a regular file (never through a
// symlink), at most maxFileBytes. Returns its bytes and permission bits.
func readForWrite(path string) ([]byte, os.FileMode, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, 0, err
	}
	if !fi.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("%s is not a regular file (a symlink is never written through)", path)
	}
	if fi.Size() > maxFileBytes {
		return nil, 0, fmt.Errorf("%s is larger than %d bytes", path, maxFileBytes)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	return raw, fi.Mode().Perm(), nil
}

// pruneFile writes one job, re-checking at write time what the plan
// established: the path still has no symlink below its estate root (unlinked),
// and its bytes are still the ones Apply verified (j.raw). Either failing is a
// skip with its reason, never a write. changed is false — and nothing, not even
// a backup, is written — when the file carries none of the keys. Otherwise the
// pre-image goes to j.backup first, then the rewrite lands through a temp file
// in the file's own directory and os.Rename, with the original mode.
func pruneFile(j job, unlinked func(path, root string) bool) (changed bool, skip string, err error) {
	path, keys := j.t.Path, j.t.Keys
	if !unlinked(path, j.t.EstateRoot) {
		return false, ReasonSymlinked, nil
	}
	raw, perm, err := readForWrite(path)
	if err != nil {
		return false, "", err
	}
	if !bytes.Equal(raw, j.raw) {
		return false, ReasonChangedSincePlan, nil
	}
	after, err := removeTopLevelKeys(raw, keys)
	if err != nil {
		return false, "", fmt.Errorf("%s: %w", path, err)
	}
	if bytes.Equal(after, raw) {
		return false, "", nil
	}
	if err := verifyRemoval(raw, after, keys); err != nil {
		return false, "", fmt.Errorf("%s: %w", path, err)
	}
	if err := writeBackup(j.backup, path, raw, perm); err != nil {
		return false, "", err
	}
	if err := atomicWrite(path, after, perm); err != nil {
		return false, "", err
	}
	return true, "", nil
}

// atomicWrite replaces path with data: a temp file in the same directory,
// chmod perm, fsync, rename.
func atomicWrite(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".prune-*.tmp")
	if err != nil {
		return fmt.Errorf("rewrite %s: %w", path, err)
	}
	name := tmp.Name()
	fail := func(err error) error {
		tmp.Close()
		os.Remove(name)
		return fmt.Errorf("rewrite %s: %w", path, err)
	}
	if err := tmp.Chmod(perm); err != nil {
		return fail(err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return fmt.Errorf("rewrite %s: %w", path, err)
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return fmt.Errorf("rewrite %s: %w", path, err)
	}
	return nil
}
