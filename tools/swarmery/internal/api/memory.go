package api

// fusion phase 12: the Memory surface — a project's durable memory made visible
// and editable in the dashboard. Three roots per project, none of them in the
// sysscan registry (so this cannot ride the sysedit write pipeline; it mirrors
// its BACKUP idiom instead — copy-verify into ~/.swarmery/config-backups before
// an atomic write):
//
//	claude-md    <project.path>/CLAUDE.md                       (single file)
//	auto-memory  ~/.claude/projects/<slug>/memory/*.md          (MEMORY.md + siblings)
//	                 slug = ingest.SlugForPath(project.path) = path with "/"→"-"
//	serena       <project.path>/.serena/memories/*.md           (if present)
//
// Endpoints (self-wired via h.DB; no cmd/main.go edit):
//	GET  /api/projects/{id}/memory            → list {kind, path, name, sizeBytes, updatedAt, writable}
//	GET  /api/projects/{id}/memory/file?path= → {path, content, hash, writable}
//	PUT  /api/projects/{id}/memory/file?path= → versioned write (backup → atomic), 409 on base_hash drift
//	POST /api/memory/consolidate?path=&dry_run= → plan (dry-run) or execute the
//	                                             auto-memory index consolidation
//
// Traversal fence: every ?path= is filepath.Clean'd and required to resolve
// (after EvalSymlinks) STRICTLY inside one of the three roots — `../` walks and
// symlink escapes are 400, never a read/write outside the roots. The consolidate
// endpoint takes a DIRECTORY instead of a file, and fences it the same way: the
// resolved directory must BE some registered project's auto-memory root.

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/memconsolidate"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/sysedit"
)

// memoryClaudeDir anchors the auto-memory root (~/.claude by default). Attached
// at startup with the same resolved --claude-dir the scanner uses; the default
// keeps the endpoints working when nothing attaches it (production wires it).
var memoryClaudeDir = defaultMemoryClaudeDir()

// memoryBackupsDir is where the versioned PUT copies the previous content
// before overwriting (mirrors sysedit's config-backups). Injectable for tests.
var memoryBackupsDir = sysedit.DefaultBackupsDir()

// memoryKeepBackups bounds the rotation depth of the per-write timestamp dirs.
const memoryKeepBackups = 50

// maxMemoryWriteBody bounds a PUT body — memory files are markdown, small.
const maxMemoryWriteBody = 4 << 20

func defaultMemoryClaudeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".claude"
	}
	return filepath.Join(home, ".claude")
}

// AttachMemoryDirs points the Memory surface at a claude dir + backups dir.
// Empty arguments keep the current value (so callers can override just one).
// It also forwards the claude dir to internal/memconsolidate, which resolves the
// same auto-memory root for the advisor's R10 rule and the CLI — the two must
// never point at different directories.
func AttachMemoryDirs(claudeDir, backupsDir string) {
	if claudeDir != "" {
		memoryClaudeDir = claudeDir
		memconsolidate.SetClaudeDir(claudeDir)
	}
	if backupsDir != "" {
		memoryBackupsDir = backupsDir
	}
}

// memoryKind is the closed vocabulary of the three memory sources.
type memoryKind string

const (
	kindClaudeMD   memoryKind = "claude-md"
	kindAutoMemory memoryKind = "auto-memory"
	kindSerena     memoryKind = "serena"
)

// memoryRoot is one resolved source directory (or, for claude-md, the parent of
// the single file). exactFile is set for claude-md: only that basename lists.
type memoryRoot struct {
	kind      memoryKind
	dir       string // the directory that anchors the fence
	exactFile string // non-empty ⇒ list ONLY this basename under dir (claude-md)
}

// memoryFileDTO is one listed memory file.
type memoryFileDTO struct {
	Kind      memoryKind `json:"kind"`
	Path      string     `json:"path"` // absolute on-disk path (the ?path= handle)
	Name      string     `json:"name"` // display basename
	SizeBytes int64      `json:"sizeBytes"`
	UpdatedAt string     `json:"updatedAt"` // RFC3339, file mtime
	Writable  bool       `json:"writable"`
}

type memoryListDTO struct {
	Files []memoryFileDTO `json:"files"`
}

// memoryFileContentDTO is the GET file body.
type memoryFileContentDTO struct {
	Path     string `json:"path"`
	Kind     string `json:"kind"`
	Content  string `json:"content"`
	Hash     string `json:"hash"` // sha256 of content — the PUT base_hash handle
	Writable bool   `json:"writable"`
}

// memoryWriteRequest is the PUT body.
type memoryWriteRequest struct {
	Content  string `json:"content"`
	BaseHash string `json:"base_hash"` // sha256 the edit is based on (409 guard)
}

// memoryConflictDTO is the 409 body — enough for the UI to re-diff.
type memoryConflictDTO struct {
	Error    string `json:"error"`
	DiskHash string `json:"disk_hash"`
	BaseHash string `json:"base_hash"`
}

// projectMemoryRoots resolves the three roots for the project id. A row miss is
// (nil, false) with a 404 already written; other DB errors write 500.
func (h *Handler) projectMemoryRoots(w http.ResponseWriter, id string) ([]memoryRoot, bool) {
	var projectPath string
	err := h.DB.QueryRow(
		`SELECT path FROM projects WHERE `+projectMatchExpr(""),
		scopeArgs(id)...).Scan(&projectPath)
	if errors.Is(err, sql.ErrNoRows) {
		writeClientErr(w, http.StatusNotFound, "project not found")
		return nil, false
	}
	if err != nil {
		writeErr(w, err)
		return nil, false
	}

	clean := filepath.Clean(projectPath)
	slug := ingest.SlugForPath(clean)
	roots := []memoryRoot{
		{kind: kindClaudeMD, dir: clean, exactFile: "CLAUDE.md"},
		{kind: kindAutoMemory, dir: filepath.Join(memoryClaudeDir, "projects", slug, "memory")},
		{kind: kindSerena, dir: filepath.Join(clean, ".serena", "memories")},
	}
	return roots, true
}

// GET /api/projects/{id}/memory — every readable memory file across the three
// roots. Missing roots are tolerated (a project with none lists []).
func (h *Handler) listMemory(w http.ResponseWriter, r *http.Request) {
	roots, ok := h.projectMemoryRoots(w, r.PathValue("id"))
	if !ok {
		return
	}
	out := memoryListDTO{Files: []memoryFileDTO{}}
	for _, root := range roots {
		files, err := listRootFiles(root)
		if err != nil {
			writeErr(w, err)
			return
		}
		out.Files = append(out.Files, files...)
	}
	// Stable order: kind (claude-md, auto-memory, serena), then name.
	sort.SliceStable(out.Files, func(i, j int) bool {
		if out.Files[i].Kind != out.Files[j].Kind {
			return memoryKindRank(out.Files[i].Kind) < memoryKindRank(out.Files[j].Kind)
		}
		return out.Files[i].Name < out.Files[j].Name
	})
	writeJSON(w, out, nil)
}

func memoryKindRank(k memoryKind) int {
	switch k {
	case kindClaudeMD:
		return 0
	case kindAutoMemory:
		return 1
	case kindSerena:
		return 2
	default:
		return 3
	}
}

// listRootFiles enumerates the *.md files of one root (or the single exactFile
// for claude-md). A missing directory/file is not an error — it yields nil.
func listRootFiles(root memoryRoot) ([]memoryFileDTO, error) {
	if root.exactFile != "" {
		full := filepath.Join(root.dir, root.exactFile)
		info, err := os.Stat(full)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, nil
			}
			return nil, err
		}
		if info.IsDir() {
			return nil, nil
		}
		return []memoryFileDTO{fileDTO(root.kind, full, info)}, nil
	}

	entries, err := os.ReadDir(root.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []memoryFileDTO
	for _, en := range entries {
		if en.IsDir() || !strings.HasSuffix(en.Name(), ".md") {
			continue
		}
		info, err := en.Info()
		if err != nil {
			continue // vanished mid-scan — skip
		}
		out = append(out, fileDTO(root.kind, filepath.Join(root.dir, en.Name()), info))
	}
	return out, nil
}

func fileDTO(kind memoryKind, full string, info os.FileInfo) memoryFileDTO {
	return memoryFileDTO{
		Kind:      kind,
		Path:      full,
		Name:      filepath.Base(full),
		SizeBytes: info.Size(),
		UpdatedAt: info.ModTime().UTC().Format(time.RFC3339),
		Writable:  !memoryReadOnly(),
	}
}

// resolveMemoryPath fences a requested ?path= into exactly one of the roots.
// It Cleans the path, then requires the EvalSymlinks'd absolute to sit STRICTLY
// under an EvalSymlinks'd root (and, for claude-md, to BE the exact file). The
// resolved-symlinks comparison defeats both `../` walks and symlink escapes.
// Returns the cleaned absolute path to use for I/O.
func resolveMemoryPath(roots []memoryRoot, reqPath string) (string, memoryKind, error) {
	if reqPath == "" {
		return "", "", errBadMemoryPath("path is required")
	}
	clean := filepath.Clean(reqPath)
	if !filepath.IsAbs(clean) {
		return "", "", errBadMemoryPath("path must be absolute")
	}

	// Resolve symlinks in the candidate's EXISTING ancestry so a symlinked
	// component can't smuggle the target outside a root. A not-yet-existing
	// leaf (first write of MEMORY.md) is fine — resolve the deepest existing
	// parent and re-join the remainder.
	resolved, err := evalExistingPath(clean)
	if err != nil {
		return "", "", err
	}

	for _, root := range roots {
		rootResolved, err := evalExistingPath(filepath.Clean(root.dir))
		if err != nil {
			return "", "", err
		}
		if root.exactFile != "" {
			want := filepath.Join(rootResolved, root.exactFile)
			if resolved == want {
				return want, root.kind, nil
			}
			continue
		}
		// Directory root: candidate must be strictly under it AND be a direct
		// *.md child (memory dirs are flat — no nested traversal handles).
		if underMemoryDir(resolved, rootResolved) &&
			filepath.Dir(resolved) == rootResolved &&
			strings.HasSuffix(resolved, ".md") {
			return resolved, root.kind, nil
		}
	}
	return "", "", errBadMemoryPath("path is outside the project's memory roots")
}

// evalExistingPath resolves symlinks over the deepest existing prefix of p and
// re-appends the non-existent remainder — so a path whose leaf doesn't exist
// yet still gets its ancestry de-symlinked (the fence holds on first write).
func evalExistingPath(p string) (string, error) {
	cur := p
	var tail []string
	for {
		resolved, err := filepath.EvalSymlinks(cur)
		if err == nil {
			if len(tail) == 0 {
				return resolved, nil
			}
			parts := append([]string{resolved}, reversed(tail)...)
			return filepath.Join(parts...), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur { // reached root without resolving — use as-is
			return p, nil
		}
		tail = append(tail, filepath.Base(cur))
		cur = parent
	}
}

func reversed(s []string) []string {
	out := make([]string, len(s))
	for i, v := range s {
		out[len(s)-1-i] = v
	}
	return out
}

// underMemoryDir reports whether path sits strictly under dir (a child, never
// dir itself — the fence pairs it with a Dir(path)==dir check so only direct
// *.md children of a root resolve). onboard.go's underDir treats dir-itself as
// "under", which would wrongly admit the root directory as a file handle.
func underMemoryDir(path, dir string) bool {
	return strings.HasPrefix(path, dir+string(os.PathSeparator))
}

// GET /api/projects/{id}/memory/file?path= — one file's content + hash.
func (h *Handler) getMemoryFile(w http.ResponseWriter, r *http.Request) {
	roots, ok := h.projectMemoryRoots(w, r.PathValue("id"))
	if !ok {
		return
	}
	full, kind, err := resolveMemoryPath(roots, r.URL.Query().Get("path"))
	if err != nil {
		writeMemoryPathErr(w, err)
		return
	}
	data, err := os.ReadFile(full)
	if err != nil {
		if os.IsNotExist(err) {
			writeClientErr(w, http.StatusNotFound, "memory file not found")
			return
		}
		writeErr(w, err)
		return
	}
	writeJSON(w, memoryFileContentDTO{
		Path:     full,
		Kind:     string(kind),
		Content:  string(data),
		Hash:     sha256Hex(data),
		Writable: !memoryReadOnly(),
	}, nil)
}

// PUT /api/projects/{id}/memory/file?path= — versioned write. Backs the current
// content up (copy-verify into config-backups) BEFORE an atomic write; base_hash
// must match the current disk content or the write is refused 409.
func (h *Handler) putMemoryFile(w http.ResponseWriter, r *http.Request) {
	if memoryReadOnly() {
		writeClientErr(w, http.StatusForbidden,
			"memory editor is in readonly mode ("+sysedit.EnvReadOnly+")")
		return
	}
	roots, ok := h.projectMemoryRoots(w, r.PathValue("id"))
	if !ok {
		return
	}
	full, _, err := resolveMemoryPath(roots, r.URL.Query().Get("path"))
	if err != nil {
		writeMemoryPathErr(w, err)
		return
	}

	var req memoryWriteRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxMemoryWriteBody)).Decode(&req); err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.BaseHash == "" {
		writeClientErr(w, http.StatusBadRequest, "base_hash is required")
		return
	}

	// Conflict detection: the file must currently exist and match base_hash.
	// (Memory files always pre-exist — the list/read created the base_hash — so
	// a missing file here is a genuine 404, not a create path.)
	disk, err := os.ReadFile(full)
	if err != nil {
		if os.IsNotExist(err) {
			writeClientErr(w, http.StatusNotFound, "memory file not found")
			return
		}
		writeErr(w, err)
		return
	}
	diskHash := sha256Hex(disk)
	if diskHash != req.BaseHash {
		writeJSONStatus(w, http.StatusConflict, memoryConflictDTO{
			Error:    "content changed on disk since base_hash",
			DiskHash: diskHash,
			BaseHash: req.BaseHash,
		})
		return
	}

	// Backup the original BEFORE any change (byte-verified), then atomic write.
	if err := backupMemoryFile(full); err != nil {
		writeErr(w, fmt.Errorf("memory: backup %s: %w", full, err))
		return
	}
	if err := atomicWriteFile(full, []byte(req.Content)); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, memoryFileContentDTO{
		Path:     full,
		Content:  req.Content,
		Hash:     sha256Hex([]byte(req.Content)),
		Writable: true,
	}, nil)
}

// ---- consolidate (agent-memory phase 3) -----------------------------------

// memoryConsolidateDTO is the POST body: always the plan, plus what was done
// when it was not a dry run.
type memoryConsolidateDTO struct {
	DryRun bool                        `json:"dryRun"`
	Plan   memconsolidate.Plan         `json:"plan"`
	Result *memconsolidate.ApplyResult `json:"result,omitempty"`
	// Error carries an apply failure ALONGSIDE Result rather than instead of it.
	// The field name matches the plain {"error": "..."} shape every other handler
	// returns, so existing clients keep reading the message from the same place.
	Error string `json:"error,omitempty"`
}

// POST /api/memory/consolidate?path=<auto-memory dir>&dry_run=1|0
//
// dry_run is the DEFAULT: only an explicit `dry_run=0` writes anything. A
// missing or malformed parameter must never be the difference between planning
// and moving the operator's memory files.
//
// The apply path takes ONE backup covering the index and every file it will
// move, before the first write, through the same copy-verify + rotation idiom
// the versioned PUT uses; the backup id comes back in the response.
func (h *Handler) consolidateMemory(w http.ResponseWriter, r *http.Request) {
	dir, err := h.resolveAutoMemoryDir(r.URL.Query().Get("path"))
	if err != nil {
		writeMemoryPathErr(w, err)
		return
	}
	dryRun := r.URL.Query().Get("dry_run") != "0"
	if !dryRun && memoryReadOnly() {
		writeClientErr(w, http.StatusForbidden,
			"memory editor is in readonly mode ("+sysedit.EnvReadOnly+")")
		return
	}

	plan, err := memconsolidate.BuildPlan(dir)
	if err != nil {
		if os.IsNotExist(err) {
			writeClientErr(w, http.StatusNotFound, "no MEMORY.md in "+dir)
			return
		}
		writeErr(w, err)
		return
	}
	out := memoryConsolidateDTO{DryRun: dryRun, Plan: plan}
	if dryRun {
		writeJSON(w, out, nil)
		return
	}
	res, err := memconsolidate.Apply(dir, plan, memconsolidate.ApplyOptions{
		Now:    time.Now(),
		Backup: backupMemoryFiles,
	})
	out.Result = &res
	if err != nil {
		// Apply owns the no-rollback trade-off deliberately, which makes the
		// backup id and the list of files already moved the ONLY handles an
		// operator has on a half-applied state. Collapsing this into a bare 500
		// discards both and leaves the Memory page showing a failure over a
		// directory that has already changed. Send them WITH the error.
		log.Printf("error: api: memory consolidate %s: %v", dir, err)
		out.Error = err.Error()
		writeJSONStatus(w, http.StatusInternalServerError, out)
		return
	}
	writeJSON(w, out, nil)
}

// resolveAutoMemoryDir fences a requested directory: after Clean + EvalSymlinks
// it must BE the auto-memory root of some registered project. Anything else —
// a `../` walk, a symlink escape, an arbitrary directory, even one of the OTHER
// two memory roots — is a 400. The candidate roots are built with the same
// formula projectMemoryRoots uses for kindAutoMemory.
func (h *Handler) resolveAutoMemoryDir(reqPath string) (string, error) {
	if reqPath == "" {
		return "", errBadMemoryPath("path is required")
	}
	clean := filepath.Clean(reqPath)
	if !filepath.IsAbs(clean) {
		return "", errBadMemoryPath("path must be absolute")
	}
	resolved, err := evalExistingPath(clean)
	if err != nil {
		return "", err
	}
	rows, err := h.DB.Query(`SELECT path FROM projects`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var projectPath string
		if err := rows.Scan(&projectPath); err != nil {
			return "", err
		}
		candidate := memconsolidate.AutoMemoryDirIn(memoryClaudeDir, projectPath)
		candResolved, cerr := evalExistingPath(filepath.Clean(candidate))
		if cerr != nil {
			continue // unreadable root — it cannot be the one being asked for
		}
		if candResolved == resolved {
			// Return the candidate — the string built from the projects table —
			// not the request-derived `resolved`. They are equal, but every
			// path the caller touches from here on then derives from the DB
			// row rather than from ?path=, which is also what makes the fence
			// legible to a taint analysis (CodeQL go/path-injection).
			return candResolved, nil
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return "", errBadMemoryPath("path is not a registered project's auto-memory directory")
}

// memoryReadOnly reuses the sysedit kill-switch env so a single flag freezes
// every config write surface, memory included.
func memoryReadOnly() bool {
	v := os.Getenv(sysedit.EnvReadOnly)
	return v == "1" || strings.EqualFold(v, "true")
}

// ---- path-error plumbing ---------------------------------------------------

type memoryPathError struct{ msg string }

func (e memoryPathError) Error() string { return e.msg }

func errBadMemoryPath(msg string) error { return memoryPathError{msg: msg} }

func writeMemoryPathErr(w http.ResponseWriter, err error) {
	var pe memoryPathError
	if errors.As(err, &pe) {
		writeClientErr(w, http.StatusBadRequest, pe.msg)
		return
	}
	writeErr(w, err)
}

// ---- backup + atomic write (mirrors internal/sysedit, standalone) ----------

// backupMemoryFile copies src into <memoryBackupsDir>/<ts>/<mirrored abs path>,
// fsyncs, verifies byte-for-byte, then rotates — the exact contract of
// sysedit.backupFile, but for files outside the registry.
func backupMemoryFile(src string) error {
	// The PUT just matched base_hash against this file, so it must exist: a
	// vanished source here is a race worth refusing, not one to skip the way
	// the plural helper does for a consolidation plan.
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("memory: backup %s: %w", src, err)
	}
	_, err := backupMemoryFiles([]string{src})
	return err
}

// BackupMemoryFiles is backupMemoryFiles for callers outside the package. The
// `swarmery memory consolidate` CLI takes its pre-apply snapshot through it, so
// the CLI and the API share ONE copy-verify + rotation idiom and cannot drift
// (the CLI used to carry its own copy, which skipped fsync and never rotated).
func BackupMemoryFiles(paths []string) (string, error) { return backupMemoryFiles(paths) }

// backupMemoryFiles is the same contract for a SET of files that must be
// recoverable together: ONE timestamp dir holds all of them, so a consolidation
// that moved eleven files is restored as one coherent snapshot rather than
// eleven that have to be correlated by clock. Returns the timestamp dir's name —
// the backup id the API hands back. Files that do not exist are skipped: a plan
// can legitimately reference an index line whose file vanished between the plan
// and the apply, and that is not a reason to refuse the whole operation.
func backupMemoryFiles(paths []string) (string, error) {
	tsDir, err := newMemoryBackupDir()
	if err != nil {
		return "", err
	}
	for _, src := range paths {
		if _, serr := os.Stat(src); serr != nil {
			if os.IsNotExist(serr) {
				continue
			}
			return "", serr
		}
		mirror := strings.TrimPrefix(filepath.Clean(src), string(os.PathSeparator))
		dst := filepath.Join(tsDir, mirror)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return "", err
		}
		if err := copyVerifyFile(src, dst); err != nil {
			return "", err
		}
	}
	if err := rotateMemoryBackups(); err != nil {
		return "", err
	}
	return filepath.Base(tsDir), nil
}

func newMemoryBackupDir() (string, error) {
	if err := os.MkdirAll(memoryBackupsDir, 0o755); err != nil {
		return "", err
	}
	base := time.Now().UTC().Format("2006-01-02T15-04-05Z")
	name := base
	for i := 2; ; i++ {
		dir := filepath.Join(memoryBackupsDir, name)
		err := os.Mkdir(dir, 0o755)
		if err == nil {
			return dir, nil
		}
		if !os.IsExist(err) {
			return "", err
		}
		name = fmt.Sprintf("%s-%d", base, i)
	}
}

// rotateMemoryBackups keeps the newest memoryKeepBackups timestamp dirs (mtime
// order) and deletes the rest through the prefix-asserting remover.
func rotateMemoryBackups() error {
	entries, err := os.ReadDir(memoryBackupsDir)
	if err != nil {
		return err
	}
	type dirent struct {
		name string
		mod  time.Time
	}
	var dirs []dirent
	for _, en := range entries {
		if !en.IsDir() {
			continue
		}
		info, err := en.Info()
		if err != nil {
			continue
		}
		dirs = append(dirs, dirent{name: en.Name(), mod: info.ModTime()})
	}
	if len(dirs) <= memoryKeepBackups {
		return nil
	}
	sort.Slice(dirs, func(i, j int) bool {
		if dirs[i].mod.Equal(dirs[j].mod) {
			return dirs[i].name < dirs[j].name
		}
		return dirs[i].mod.Before(dirs[j].mod)
	})
	for _, d := range dirs[:len(dirs)-memoryKeepBackups] {
		if err := removeMemoryBackupDir(memoryBackupsDir, d.name); err != nil {
			return err
		}
	}
	return nil
}

// removeMemoryBackupDir refuses to RemoveAll anything not strictly under root.
func removeMemoryBackupDir(root, name string) error {
	rootAbs, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return err
	}
	victim, err := filepath.Abs(filepath.Join(rootAbs, name))
	if err != nil {
		return err
	}
	if !strings.HasPrefix(victim, rootAbs+string(os.PathSeparator)) {
		return fmt.Errorf("memory: refusing to remove %q outside backups root %q", victim, rootAbs)
	}
	return os.RemoveAll(victim)
}

// copyVerifyFile copies src → dst, fsyncs, and re-reads to confirm a byte-for-
// byte match (a backup that cannot be trusted is an error).
func copyVerifyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	mode := os.FileMode(0o600)
	if info, statErr := os.Stat(src); statErr == nil {
		mode = info.Mode().Perm()
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	check, err := os.ReadFile(dst)
	if err != nil {
		return err
	}
	if !bytes.Equal(data, check) {
		return fmt.Errorf("memory: backup verification failed: %s != %s", dst, src)
	}
	return nil
}

// atomicWriteFile writes content to path via tmp-in-same-dir → fsync → rename,
// preserving the original file's permissions.
func atomicWriteFile(path string, content []byte) error {
	dir := filepath.Dir(path)
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(dir, ".memory-*.tmp")
	if err != nil {
		return fmt.Errorf("memory: tmp in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("memory: write %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("memory: fsync %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("memory: close %s: %w", tmpName, err)
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("memory: chmod %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("memory: rename %s → %s: %w", tmpName, path, err)
	}
	return nil
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
