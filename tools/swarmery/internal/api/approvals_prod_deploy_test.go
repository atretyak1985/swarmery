package api

// Production-deploy approval class (needs-you-queue phase 2): a matching hook
// call is handed straight back to the session's native dialog (204), and every
// remote approve/answer on such a row is refused with 403.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/approvals"
)

func prodDeployOptions(t *testing.T) approvals.Options {
	t.Helper()
	g, err := approvals.NewProdGuard(nil)
	if err != nil {
		t.Fatal(err)
	}
	return approvals.Options{ProdGuard: g}
}

// assertLocalOnly403 checks one refused remote decision.
func assertLocalOnly403(t *testing.T, label string, resp *http.Response) {
	t.Helper()
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("%s: status = %d, want 403; body %s", label, resp.StatusCode, body)
	}
	var e struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("%s: body %s is not JSON: %v", label, body, err)
	}
	if e.Code != "prod_deploy_local_only" || !strings.Contains(e.Error, "session's terminal") {
		t.Errorf("%s: body = %+v, want code prod_deploy_local_only", label, e)
	}
}

func postAnswer(t *testing.T, srvURL string, id int64) *http.Response {
	t.Helper()
	resp, err := http.Post(fmt.Sprintf("%s/api/approvals/%d", srvURL, id),
		"application/json", strings.NewReader(`{"action":"answer","answers":{"Deploy?":"yes"}}`))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestProdDeployHookAnswers204AndRefusesRemoteDecisions(t *testing.T) {
	srv, db, _ := approvalsTestServer(t, prodDeployOptions(t))

	start := time.Now()
	var res hookResult
	select {
	case res = <-postHook(srv, hookCtx(t), hookBody("prod-hook", "Bash", "acme-cli deploy --prod")):
	case <-time.After(5 * time.Second):
		t.Fatal("prod-deploy hook still long-polling after 5s, want an immediate 204")
	}
	if res.err != nil {
		t.Fatal(res.err)
	}
	if res.status != http.StatusNoContent {
		t.Fatalf("hook status = %d (body %s), want 204", res.status, res.body)
	}
	if took := time.Since(start); took >= time.Second {
		t.Errorf("hook took %s, want well under 1s", took)
	}

	var id int64
	if err := db.QueryRow(`SELECT id FROM permission_requests WHERE risk_class = ?`,
		approvals.RiskProdDeploy).Scan(&id); err != nil {
		t.Fatalf("no prod-deploy row recorded: %v", err)
	}

	assertLocalOnly403(t, "approve", resolveVia(t, srv, float64(id), "approve", ""))
	assertLocalOnly403(t, "answer", postAnswer(t, srv.URL, id))

	// The list exposes the class; an ordinary row reads ''.
	ordinary := postHook(srv, hookCtx(t), hookBody("prod-hook-ordinary", "Bash", "npm ci --production"))
	waitPending(t, srv)
	var list []map[string]any
	getJSON(t, srv.URL+"/api/approvals?status=all", &list)
	classes := map[string]string{}
	for _, p := range list {
		rc, ok := p["riskClass"].(string)
		if !ok {
			t.Fatalf("row %v has no string riskClass", p)
		}
		classes[p["status"].(string)] = rc
	}
	if classes[approvals.StatusResolvedElsewhere] != approvals.RiskProdDeploy {
		t.Errorf("status=all classes = %v, want the hand-off row as prod-deploy", classes)
	}
	if rc, ok := classes[approvals.StatusPending]; !ok || rc != "" {
		t.Errorf("status=all classes = %v, want the ordinary pending row with riskClass ''", classes)
	}
	select {
	case r := <-ordinary:
		t.Fatalf("ordinary request answered early: %+v", r)
	default:
	}
}

// TestProdDeployPendingRowRefused: a still-pending prod-deploy row (seeded with
// no waiter — the 403 must win over the 410 no-waiter answer) refuses approve
// and answer; deny is not blocked by the class.
func TestProdDeployPendingRowRefused(t *testing.T) {
	srv, db, _ := approvalsTestServer(t, prodDeployOptions(t))
	id, _ := seedOrphanRequest(t, db, "prod-pending", "Bash",
		`{"session_id":"prod-pending","tool_name":"Bash","tool_input":{"command":"terraform apply"}}`, time.Now())
	if _, err := db.Exec(`UPDATE permission_requests SET risk_class = ? WHERE id = ?`,
		approvals.RiskProdDeploy, id); err != nil {
		t.Fatal(err)
	}

	assertLocalOnly403(t, "approve", resolveVia(t, srv, float64(id), "approve", ""))
	assertLocalOnly403(t, "answer", postAnswer(t, srv.URL, id))

	resp := resolveVia(t, srv, float64(id), "deny", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusGone {
		t.Errorf("deny on a waiterless prod-deploy row: status = %d, want 410 (class does not block deny)", resp.StatusCode)
	}
	if got := requestStatusAPI(t, db, id); got != "pending" {
		t.Errorf("status = %q after refused decisions, want pending", got)
	}
}
