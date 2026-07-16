package api

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

const utf8BOM = "\xef\xbb\xbf"

func TestBreakdownCSV(t *testing.T) {
	srv := analyticsServer(t)
	resp, err := http.Get(srv.URL + "/api/stats/breakdown?by=project&format=csv")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Errorf("Content-Type = %q, want text/csv", ct)
	}
	cd := resp.Header.Get("Content-Disposition")
	if !strings.HasPrefix(cd, `attachment; filename="swarmery-breakdown-project-`) ||
		!strings.HasSuffix(cd, `.csv"`) {
		t.Errorf("Content-Disposition = %q, want attachment with dated csv filename", cd)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(body), utf8BOM) {
		t.Error("body missing UTF-8 BOM (Excel compatibility)")
	}
	lines := strings.Split(strings.TrimSpace(strings.TrimPrefix(string(body), utf8BOM)), "\n")
	if lines[0] != "key,name,cost_usd,tokens_in,tokens_out,runs,sessions,last_used,success_rate" {
		t.Errorf("header = %q", lines[0])
	}
	// analyticsServer: alpha has $0.75 / 110 in / 55 out / 2 sessions in range.
	if len(lines) < 2 || !strings.HasPrefix(lines[1], "-work-alpha,Alpha,0.75,110,55,,2,") {
		t.Errorf("first row = %q, want alpha totals", lines[1])
	}
}

func TestTimeseriesCSV(t *testing.T) {
	srv := analyticsServer(t)
	resp, err := http.Get(srv.URL + "/api/stats/timeseries?metric=cost&group=project&format=csv")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Errorf("Content-Type = %q, want text/csv", ct)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(body), utf8BOM) {
		t.Error("body missing UTF-8 BOM (Excel compatibility)")
	}
	lines := strings.Split(strings.TrimSpace(strings.TrimPrefix(string(body), utf8BOM)), "\n")
	if lines[0] != "day,-work-alpha" {
		t.Errorf("header = %q, want day,-work-alpha", lines[0])
	}
	if len(lines) != 15 { // header + 14 default days
		t.Fatalf("lines = %d, want 15", len(lines))
	}
	// Today (the last bucket) carries alpha's $0.75.
	if !strings.HasSuffix(lines[14], ",0.75") {
		t.Errorf("last row = %q, want …,0.75", lines[14])
	}
}

func TestCSVDoesNotAffectJSON(t *testing.T) {
	srv := analyticsServer(t)
	var rows []breakdownRow
	getJSON(t, srv.URL+"/api/stats/breakdown?by=project", &rows) // asserts application/json
	if len(rows) == 0 {
		t.Fatal("json breakdown empty")
	}
}
