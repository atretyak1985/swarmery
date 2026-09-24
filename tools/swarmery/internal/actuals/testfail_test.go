package actuals

import (
	"reflect"
	"testing"
)

func TestFailureLoci(t *testing.T) {
	cases := []struct {
		name, cmd, out string
		want           []string
	}{
		{
			name: "go package FAIL line wins over the command's scope",
			cmd:  "cd tools/app && go test ./...",
			out:  "--- FAIL: TestX (0.00s)\n    store_test.go:12: boom\nFAIL\tgithub.com/x/app/internal/store\t0.1s\nFAIL\n",
			want: []string{"github.com/x/app/internal/store", "store_test.go"},
		},
		{
			name: "vitest file",
			cmd:  "npx vitest run",
			out:  " FAIL  src/orders/cart.test.ts > cart > totals\n",
			want: []string{"src/orders/cart.test.ts"},
		},
		{
			name: "pytest node id",
			cmd:  "pytest",
			out:  "FAILED tests/test_orders.py::test_total - assert 1 == 2\n",
			want: []string{"tests/test_orders.py"},
		},
		{
			name: "no output: command path arguments, minus flags, redirects and env",
			cmd:  "FOO=bar/baz go test -run TestX ./internal/store/... 2>&1 > /dev/null | tee out/log.txt",
			out:  "",
			want: []string{"internal/store", "out/log.txt"},
		},
		{
			name: "nothing locatable",
			cmd:  "make test",
			out:  "FAIL\n",
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := failureLoci(tc.cmd, tc.out); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("failureLoci = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestForecastScopeExpects(t *testing.T) {
	s := &forecastScope{
		Areas: []string{"internal/store", "./web/src/"},
		Files: []string{"internal/cost/*.go", "config/pricing.json"},
		Risks: []string{"The recost path diverges", "orders table migration"},
	}
	cases := map[string]bool{
		"github.com/x/app/internal/store": true,  // module-relative area inside an import path
		"tools/app/internal/store/sub":    true,  // repo-relative, deeper
		"internal/restore":                false, // `store` must not match `restore`
		"web/src/a.test.ts":               true,  // decorated area normalised
		"tools/app/internal/cost/cost.go": true,  // glob against a suffix
		"config/pricing.json":             true,  // plain file entry
		"internal/recost":                 true,  // named by a risk
		"src/orders":                      true,  // risk mentions the base name
		"internal/ingest":                 false,
	}
	for locus, want := range cases {
		if got := s.expects([]string{locus}); got != want {
			t.Errorf("expects(%q) = %v, want %v", locus, got, want)
		}
	}
	if (&forecastScope{}).expects([]string{"anything"}) {
		t.Error("an empty forecast expected a failure")
	}
}

func TestCountTestFailures(t *testing.T) {
	failing := func(cmd, out string) testRunEvent {
		return testRunEvent{
			Status:        "error",
			Payload:       `{"command":` + quote(cmd) + `,"failed":1}`,
			ParentPayload: `{"result":` + quote(out) + `}`,
		}
	}
	evs := []testRunEvent{
		failing("go test ./internal/store", ""),                           // inside the forecast
		failing("go test ./internal/ingest", ""),                          // outside it
		failing("make test", ""),                                          // unlocatable: failure, not a surprise
		{Status: "ok", Payload: `{"command":"go test ./...","failed":3}`}, // exit 0, parsed failures
		{Status: "ok", Payload: `{"command":"go test ./...","failed":0}`}, // a pass
		{Status: "error", Payload: `not json`},                            // garbled payload still has a status
	}
	scope := &forecastScope{Areas: []string{"internal/store"}}
	failures, unexpected := countTestFailures(evs, scope)
	if failures != 5 || unexpected != 1 {
		t.Errorf("with forecast: failures %d unexpected %d, want 5 / 1", failures, unexpected)
	}
	failures, unexpected = countTestFailures(evs, nil)
	if failures != 5 || unexpected != 0 {
		t.Errorf("without forecast: failures %d unexpected %d, want 5 / 0", failures, unexpected)
	}
}

func TestResultText(t *testing.T) {
	for payload, want := range map[string]string{
		"":                                       "",
		`not json`:                               "",
		`{"input":{}}`:                           "",
		`{"result":"Error: exit 1"}`:             "Error: exit 1",
		`{"result":{"stdout":"a","stderr":"b"}}`: "a\nb\n",
		`{"result":[1,2]}`:                       "",
	} {
		if got := resultText(payload); got != want {
			t.Errorf("resultText(%q) = %q, want %q", payload, got, want)
		}
	}
}

func quote(s string) string {
	b := []byte{'"'}
	for _, r := range s {
		switch r {
		case '"', '\\':
			b = append(b, '\\', byte(r))
		case '\n':
			b = append(b, '\\', 'n')
		case '\t':
			b = append(b, '\\', 't')
		default:
			b = append(b, string(r)...)
		}
	}
	return string(append(b, '"'))
}
