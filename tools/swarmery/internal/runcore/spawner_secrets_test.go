package runcore

// The daemon half of the secret channel: a spawn carries the BOUND account's
// secret store into the child's environment, and a spawn that is not bound to
// that account carries nothing.
//
// Both directions are asserted on purpose. Only the negative one proves the
// scoping property — that the store is per ACCOUNT and not, as a launchd plist
// would have made it, machine-wide.
//
// The variable seeded below is a literal non-secret and the store lives in a
// t.TempDir(): no test reads the operator's real ~/.swarmery/secrets.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	// secretVar is the name the fake `claude` reports back. Prefixed so it can
	// never collide with something the test process really has.
	secretVar = "SWARMERY_TEST_STORE_SECRET"
	// secretValue is not a secret; it is a marker string.
	secretValue = "store-delivered"
	// absentMarker is what the fake prints when the variable is not in its
	// environment AT ALL. `${VAR-default}` (no colon) distinguishes "unset" from
	// "set to empty" — the claim is absence, not emptiness.
	absentMarker = "__ABSENT__"
)

// seedSecretStore points SWARMERY_SECRETS_DIR at a temp dir and writes one
// account's store, 0600.
func seedSecretStore(t *testing.T, account string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("SWARMERY_SECRETS_DIR", dir)
	t.Setenv("HOME", t.TempDir())
	// The test process must not already carry the variable, or the negative case
	// would pass for the wrong reason.
	os.Unsetenv(secretVar)
	path := filepath.Join(dir, account+".env")
	if err := os.WriteFile(path, []byte(secretVar+"="+secretValue+"\n"), 0o600); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("chmod store: %v", err)
	}
}

// childSecret runs the real ClaudeRunner against a fake `claude` that reports
// what IT saw, and returns that.
func childSecret(t *testing.T, account string) string {
	t.Helper()
	res, err := ClaudeRunner{Engine: "test"}.Start(context.Background(), Spec{
		Prompt: "p", SessionUUID: "u-secret-" + account, Cwd: t.TempDir(), Account: account,
		Bin:           fakeBin(t, `printf '%s\n' "${`+secretVar+`-`+absentMarker+`}"`+"\n"),
		Timeout:       30 * time.Second,
		CaptureStdout: true,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return strings.TrimSpace(res.Output)
}

// A run bound to the account that HAS a store reaches the CLI with the store's
// variables — which is what makes ${VAR} in a plugin's .mcp.json expand.
func TestStart_BoundAccountCarriesItsSecretStore(t *testing.T) {
	seedSecretStore(t, "work")
	if got := childSecret(t, "work"); got != secretValue {
		t.Fatalf("child saw %s=%q, want %q — the MCP servers would fail to start", secretVar, got, secretValue)
	}
}

// THE scoping proof. A store exists on this machine, and a run under the
// DEFAULT account — or under no binding at all — must not see it. If this test
// ever passes vacuously, the channel has silently become machine-wide.
func TestStart_UnboundAndDefaultSpawnsCarryNoSecrets(t *testing.T) {
	seedSecretStore(t, "work")
	for _, account := range []string{"", "default", "other"} {
		name := account
		if name == "" {
			name = "unbound"
		}
		t.Run(name, func(t *testing.T) {
			if got := childSecret(t, account); got != absentMarker {
				t.Fatalf("a %s spawn saw %s=%q, want it ABSENT — another account's "+
					"secrets leaked into this run", name, secretVar, got)
			}
		})
	}
}
