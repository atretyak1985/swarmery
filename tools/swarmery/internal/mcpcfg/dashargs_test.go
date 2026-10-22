package mcpcfg

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// `claude mcp add` parses its argv with an option parser, so a name or command
// beginning with a dash would be read as one of its own options. Both are
// refused outright, on add and on remove, before anything is executed.
func TestDashLedNameAndCommandRejected(t *testing.T) {
	for _, spec := range []AddSpec{
		{Name: "-x", Transport: TransportStdio, Command: "echo", Scope: ScopeLocal},
		{Name: "--scope=user", Transport: TransportHTTP, URL: "https://ex.com/mcp", Scope: ScopeLocal},
		{Name: "ok", Transport: TransportStdio, Command: "-x", Scope: ScopeLocal},
		{Name: "ok", Transport: TransportStdio, Command: "--env=A=1", Scope: ScopeLocal},
	} {
		if _, err := addArgs(spec); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("addArgs(%+v) err = %v, want ErrInvalidInput", spec, err)
		}
	}
	f := &fakeRunner{}
	if err := NewWithRunner(f.run).Remove(context.Background(), "-x", ScopeLocal); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("Remove(-x) err = %v, want ErrInvalidInput", err)
	}
	if f.calls != 0 {
		t.Errorf("a refused remove must not exec; calls = %d", f.calls)
	}
}

// A stdio server's own arguments routinely begin with a dash (`npx -y pkg`,
// `server --port 1`). They belong to the child, so the argv ends the CLI's
// options before the command: without the marker, `-e K=V` among them became
// an environment entry of the server instead of an argument.
func TestAddArgsStdioChildArgsAreData(t *testing.T) {
	got, err := addArgs(AddSpec{
		Name: "s", Transport: TransportStdio, Command: "my-server",
		Args: []string{"-e", "K=V", "--port", "1"}, Scope: ScopeLocal,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"mcp", "add", "-s", "local", "s", "--", "my-server", "-e", "K=V", "--port", "1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("argv = %q, want %q", got, want)
	}
}
