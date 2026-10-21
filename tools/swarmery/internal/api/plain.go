package api

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/plainsum"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wsingest"
)

// plainDTO is the GET/POST /api/epics/{taskId}/plain body. Status is
// "ready" (text set), "missing" (not generated yet — POST to generate) or
// "empty" (the source has nothing to retell, e.g. no Completion Report yet).
type plainDTO struct {
	Status string `json:"status"`
	Text   string `json:"text,omitempty"`
}

// getPlain — GET /api/epics/{taskId}/plain?kind=&path=&lang=. The cached
// plain-language retelling of a plan doc; never spawns a model run.
func (h *Handler) getPlain(w http.ResponseWriter, r *http.Request) {
	h.servePlain(w, r, false)
}

// postPlain — POST /api/epics/{taskId}/plain?kind=&path=&lang=. Returns the
// retelling, generating it first when it is not cached (blocks for the run).
func (h *Handler) postPlain(w http.ResponseWriter, r *http.Request) {
	h.servePlain(w, r, true)
}

func (h *Handler) servePlain(w http.ResponseWriter, r *http.Request, generate bool) {
	if h.Plain == nil {
		writeClientErr(w, http.StatusServiceUnavailable, "plain-language summaries are not available")
		return
	}
	taskID, ok := parseTaskIDParam(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	kind, ok := plainsum.ParseKind(q.Get("kind"))
	if !ok {
		writeClientErr(w, http.StatusBadRequest, "kind must be phase, phase-report, plan or plan-report")
		return
	}
	lang := plainsum.ParseLang(q.Get("lang"))
	source, err := h.plainSource(taskID, kind, q.Get("path"))
	switch {
	case errors.Is(err, errPlainPath):
		writeClientErr(w, http.StatusBadRequest, err.Error())
		return
	case errors.Is(err, os.ErrNotExist):
		writeClientErr(w, http.StatusNotFound, "doc not found")
		return
	case err != nil:
		writePlanDocErr(w, err)
		return
	}
	if strings.TrimSpace(source) == "" {
		writeJSON(w, plainDTO{Status: "empty"}, nil)
		return
	}
	if !generate {
		text, ok, err := h.Plain.Cached(plainsum.Key(kind, lang, source))
		if err != nil {
			writeErr(w, err)
			return
		}
		if !ok {
			writeJSON(w, plainDTO{Status: "missing"}, nil)
			return
		}
		writeJSON(w, plainDTO{Status: "ready", Text: text}, nil)
		return
	}
	text, err := h.Plain.Generate(r.Context(), kind, lang, source)
	if err != nil {
		writeClientErr(w, http.StatusBadGateway, fmt.Sprintf("retelling failed: %v", err))
		return
	}
	writeJSON(w, plainDTO{Status: "ready", Text: text}, nil)
}

// errPlainPath is a phase kind sent without its doc path.
var errPlainPath = errors.New("path query param required")

// plainSource assembles the text a kind retells, read from the task's plan dir
// with the same path confinement as the docs endpoint.
func (h *Handler) plainSource(taskID int64, kind plainsum.Kind, rel string) (string, error) {
	switch kind {
	case plainsum.KindPhase, plainsum.KindPhaseReport:
		if strings.TrimSpace(rel) == "" {
			return "", errPlainPath
		}
		doc, err := h.readPlanDoc(taskID, rel)
		if err != nil {
			return "", err
		}
		if kind == plainsum.KindPhase {
			return doc, nil
		}
		return wsingest.ParseCompletionReport(doc), nil
	case plainsum.KindPlan:
		readme, err := h.readPlanDoc(taskID, "README.md")
		if err != nil {
			return "", err
		}
		spec, err := h.readPlanDoc(taskID, "spec.md")
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if strings.TrimSpace(spec) == "" {
			return readme, nil
		}
		return readme + "\n\n---\n\n" + spec, nil
	default: // plainsum.KindPlanReport
		return h.planReportSource(taskID)
	}
}

// planReportSource is SUMMARY.md (when present) followed by every phase doc's
// Completion Report in phase order.
func (h *Handler) planReportSource(taskID int64) (string, error) {
	var parts []string
	summary, err := h.readPlanDoc(taskID, "SUMMARY.md")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if strings.TrimSpace(summary) != "" {
		parts = append(parts, summary)
	}
	readme, err := h.resolvePlanDoc(taskID, "README.md")
	if err != nil {
		return "", err
	}
	docs, err := filepath.Glob(filepath.Join(filepath.Dir(readme), "phase-*.md"))
	if err != nil {
		return "", err
	}
	sort.SliceStable(docs, func(i, j int) bool { return phaseDocSeq(docs[i]) < phaseDocSeq(docs[j]) })
	for _, p := range docs {
		body, err := os.ReadFile(p)
		if err != nil {
			continue // a doc that vanished mid-read just drops out
		}
		if rep := wsingest.ParseCompletionReport(string(body)); rep != "" {
			parts = append(parts, "## "+strings.TrimSuffix(filepath.Base(p), ".md")+"\n\n"+rep)
		}
	}
	return strings.Join(parts, "\n\n---\n\n"), nil
}

var phaseDocSeqRe = regexp.MustCompile(`^phase-(\d+)`)

// phaseDocSeq is the N of phase-N-<slug>.md (a large number when absent, so
// odd names sort last).
func phaseDocSeq(path string) int {
	m := phaseDocSeqRe.FindStringSubmatch(filepath.Base(path))
	if m == nil {
		return 1 << 30
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 1 << 30
	}
	return n
}

// readPlanDoc reads one path-confined plan doc. A missing file is
// os.ErrNotExist; a task without a plan dir is sql.ErrNoRows.
func (h *Handler) readPlanDoc(taskID int64, rel string) (string, error) {
	path, err := h.resolvePlanDoc(taskID, rel)
	if err != nil {
		return "", err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(body), nil
}
