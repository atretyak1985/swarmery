package phaserun

import (
	"strings"
	"testing"
)

// Start appends exactly what the lesson injector returns (learning-loop phase
// 15), after the run uuid is stamped, and the citation hook runs on the exit
// path next to the actuals recorder. The golden prompts live in
// prompt_lessons_golden_test.go (an external package: internal/lessons reaches
// this package through surprise → actuals).
func TestStartAppendsInjectedLessons(t *testing.T) {
	for _, tc := range []struct {
		name, block string
	}{
		{"no lessons", ""},
		{"lessons", "\n\nLessons from earlier runs in these areas (cite the id if you rely on one):\n- [L-7] Be calm."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, _, p1, _ := fixture(t)
			r := &stubRunner{}
			s := newTestService(db, r, &stubWt{})
			var (
				injectedFor int64
				injectedRun string
				order       []string
				citedDoc    string
			)
			s.InjectLessons = func(phaseID int64, uuid string) string {
				injectedFor, injectedRun = phaseID, uuid
				var stamped string
				_ = db.QueryRow(`SELECT COALESCE(run_session_uuid, '') FROM epic_phases WHERE id = ?`, phaseID).Scan(&stamped)
				if stamped != uuid {
					t.Errorf("injector ran before the run uuid was stamped (%q vs %q)", stamped, uuid)
				}
				return tc.block
			}
			s.Actuals = func(int64, string, string) { order = append(order, "actuals") }
			s.LessonCitations = func(_ int64, _ string, docPath string) {
				order = append(order, "citations")
				citedDoc = docPath
			}
			uuid, err := s.Start(p1, "", "")
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			if injectedFor != p1 || injectedRun != uuid {
				t.Errorf("injector got (%d, %q), want (%d, %q)", injectedFor, injectedRun, p1, uuid)
			}
			r.mu.Lock()
			prompt := r.specs[0].Prompt
			r.mu.Unlock()
			if tc.block == "" {
				if strings.Contains(prompt, "Lessons from earlier runs") || !strings.HasSuffix(prompt, "----------------------------------------") {
					t.Error("an empty injection changed the prompt")
				}
			} else if !strings.HasSuffix(prompt, tc.block) {
				t.Errorf("prompt does not end with the injected block:\n%s", prompt[max(0, len(prompt)-300):])
			}
			if len(order) < 2 || order[0] != "actuals" || order[1] != "citations" {
				t.Errorf("exit order = %v, want actuals then citations", order)
			}
			if citedDoc == "" {
				t.Error("citation hook got no doc path")
			}
		})
	}
}
