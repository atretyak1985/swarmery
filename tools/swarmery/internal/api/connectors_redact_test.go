package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/mcpcfg"
)

// A connector's detail is its command line or URL as `claude mcp list` prints
// it, and both routinely embed credentials (an --api-key flag, a token in the
// URL query, userinfo). GET /api/connectors masks the secret values the same
// way every other response-layer secret is masked, keeping the rest readable.
func TestGetConnectorsRedactsDetail(t *testing.T) {
	const fixture = `Checking MCP server health…

keyed: npx -y keyed-mcp --api-key=sk-ant-api03-SECRETSECRET - ✔ Connected
tokenurl: https://mcp.example.com/mcp?token=hunter2hunter2 - ! Needs authentication
userinfo: https://bob:pa55word@mcp.example.com/sse (SSE) - ✘ Failed to connect
`
	AttachConnectorReader(mcpcfg.NewWithRunner(func(context.Context, ...string) ([]byte, error) {
		return []byte(fixture), nil
	}))
	t.Cleanup(func() { AttachConnectorReader(nil) })
	mux := http.NewServeMux()
	Routes(mux, &Handler{})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	res, err := http.Get(srv.URL + "/api/connectors")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var resp connectorsResponse
	if err := json.NewDecoder(res.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Connectors) != 3 {
		t.Fatalf("connectors = %d, want 3", len(resp.Connectors))
	}
	for _, c := range resp.Connectors {
		for _, secret := range []string{"SECRETSECRET", "hunter2hunter2", "pa55word"} {
			if strings.Contains(c.Detail, secret) {
				t.Errorf("%s detail leaks %q: %q", c.Name, secret, c.Detail)
			}
		}
		if !strings.Contains(c.Detail, "•••") { // internal/redact's mark
			t.Errorf("%s detail = %q, want the redaction mark", c.Name, c.Detail)
		}
	}
	if got := resp.Connectors[0].Detail; !strings.HasPrefix(got, "npx -y keyed-mcp --api-key=") {
		t.Errorf("non-secret parts must stay readable: %q", got)
	}
}
