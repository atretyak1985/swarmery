package ingest

import (
	"encoding/json"
	"testing"
)

func TestIsTestCommand(t *testing.T) {
	cases := []struct {
		cmd  string
		want bool
	}{
		{"go test ./...", true},
		{"cd repo && npm test", true},
		{"pnpm run test", true},
		{"yarn test --watch=false", true},
		{"npx vitest run", true},
		{"pytest -q tests/", true},
		{"cargo test", true},
		{"gradle :app:test", true},
		{"mvn -q verify test", true},
		// non-tests that superficially resemble one
		{"bash .claude/hooks/session-start.sh", false},
		{"echo latest && git log", false},
		{"go build ./...", false},
		{"npm run lint", false},
		{"contest_runner --go", false},
	}
	for _, c := range cases {
		if got := isTestCommand(c.cmd); got != c.want {
			t.Errorf("isTestCommand(%q) = %v, want %v", c.cmd, got, c.want)
		}
	}
}

func TestParseTestCounts(t *testing.T) {
	cases := []struct {
		name                          string
		text                          string
		passed, failed, skipped       int
		parsed                        bool
	}{
		{"pytest", "===== 212 passed, 3 skipped in 4.51s =====", 212, 0, 3, true},
		{"pytest fail", "== 1 failed, 211 passed in 5s ==", 211, 1, 0, true},
		{"jest", "Tests:       2 failed, 210 passed, 212 total", 210, 2, 0, true},
		{"vitest skip", "Test Files  1 passed (1)\n Tests  40 passed | 2 skipped (42)", 40, 0, 2, true},
		{"go -v", "--- PASS: TestA\n--- FAIL: TestB\n--- SKIP: TestC\n--- PASS: TestD\nFAIL", 2, 1, 1, true},
		{"none", "build failed: cannot find package", 0, 0, 0, false},
		{"empty", "", 0, 0, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, f, s, ok := parseTestCounts(c.text)
			if p != c.passed || f != c.failed || s != c.skipped || ok != c.parsed {
				t.Errorf("parseTestCounts(%q) = (%d,%d,%d,%v), want (%d,%d,%d,%v)",
					c.name, p, f, s, ok, c.passed, c.failed, c.skipped, c.parsed)
			}
		})
	}
}

func TestTestFramework(t *testing.T) {
	cases := map[string]string{
		"pytest -q":            "pytest",
		"npx vitest run":       "vitest",
		"npx jest":             "jest",
		"go test ./...":        "go",
		"cargo test":           "cargo",
		"bundle exec rspec":    "rspec",
		"vendor/bin/phpunit":   "phpunit",
		"dotnet test":          "dotnet",
		"cd x && npm test":     "unknown",
	}
	for cmd, want := range cases {
		if got := testFramework(cmd); got != want {
			t.Errorf("testFramework(%q) = %q, want %q", cmd, got, want)
		}
	}
}

func TestTestResultText(t *testing.T) {
	// object with stdout/stderr
	obj, _ := json.Marshal(map[string]any{"stdout": "212 passed", "stderr": "warn"})
	if got := testResultText(obj); got != "212 passed\nwarn\n" {
		t.Errorf("object result = %q", got)
	}
	// bare string
	if got := testResultText(json.RawMessage(`"3 skipped"`)); got != "3 skipped" {
		t.Errorf("string result = %q", got)
	}
	// empty
	if got := testResultText(nil); got != "" {
		t.Errorf("nil result = %q, want empty", got)
	}
}
