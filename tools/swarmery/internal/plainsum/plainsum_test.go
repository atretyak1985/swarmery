package plainsum

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

type gateRunner struct {
	calls atomic.Int32
	gate  chan struct{}
	err   error
}

func (g *gateRunner) Run(context.Context, string) (string, error) {
	g.calls.Add(1)
	if g.gate != nil {
		<-g.gate
	}
	if g.err != nil {
		return "", g.err
	}
	return "retold", nil
}

func newService(t *testing.T, r Runner) *Service {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "plain.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &Service{DB: db, Runner: r, Model: "m"}
}

func TestKeyDependsOnEveryInput(t *testing.T) {
	base := Key(KindPhase, "uk", "doc")
	for name, k := range map[string]string{
		"kind":   Key(KindPlan, "uk", "doc"),
		"lang":   Key(KindPhase, "en", "doc"),
		"source": Key(KindPhase, "uk", "doc!"),
	} {
		if k == base {
			t.Errorf("key ignores %s", name)
		}
	}
}

func TestParseKindAndLang(t *testing.T) {
	if _, ok := ParseKind("phase-report"); !ok {
		t.Error("phase-report rejected")
	}
	if _, ok := ParseKind("story"); ok {
		t.Error("unknown kind accepted")
	}
	if ParseLang("uk-UA") != "uk" || ParseLang("en") != "en" || ParseLang("") != "en" {
		t.Error("ParseLang mapping wrong")
	}
}

func TestPromptPerKind(t *testing.T) {
	for kind, want := range map[Kind]string{
		KindPhase:       "one phase of a software work plan",
		KindPhaseReport: "completion report",
		KindPlan:        "overview of a software work plan",
		KindPlanReport:  "finished software work plan",
	} {
		p := Prompt(kind, "uk", "SOURCE")
		if !strings.Contains(p, want) || !strings.Contains(p, "Ukrainian") || !strings.Contains(p, "<document>\nSOURCE\n</document>") {
			t.Errorf("%s prompt malformed:\n%s", kind, p)
		}
	}
	long := strings.Repeat("x", maxSource+10)
	if p := Prompt(KindPhase, "en", long); !strings.Contains(p, "[…truncated]") {
		t.Error("oversized source not truncated")
	}
}

// Concurrent callers for one key share a single model run; the result is cached.
func TestGenerateSingleFlightAndCache(t *testing.T) {
	r := &gateRunner{gate: make(chan struct{})}
	s := newService(t, r)
	var wg sync.WaitGroup
	results := make([]string, 4)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			text, err := s.Generate(context.Background(), KindPhase, "en", "doc")
			if err != nil {
				t.Error(err)
			}
			results[i] = text
		}(i)
	}
	for r.calls.Load() == 0 {
		runtime.Gosched() // wait until the leader is inside the runner
	}
	close(r.gate)
	wg.Wait()
	if n := r.calls.Load(); n != 1 {
		t.Fatalf("runs = %d, want 1", n)
	}
	for _, got := range results {
		if got != "retold" {
			t.Errorf("result = %q", got)
		}
	}
	if text, ok, err := s.Cached(Key(KindPhase, "en", "doc")); err != nil || !ok || text != "retold" {
		t.Fatalf("cache = %q %v %v", text, ok, err)
	}
}

func TestGenerateErrorsAreNotCached(t *testing.T) {
	r := &gateRunner{err: errors.New("boom")}
	s := newService(t, r)
	if _, err := s.Generate(context.Background(), KindPlan, "en", "doc"); err == nil {
		t.Fatal("want error")
	}
	if _, ok, _ := s.Cached(Key(KindPlan, "en", "doc")); ok {
		t.Fatal("a failed run was cached")
	}
	if _, err := s.Generate(context.Background(), KindPlan, "en", "  "); !errors.Is(err, ErrEmpty) {
		t.Fatalf("empty source err = %v", err)
	}
}
