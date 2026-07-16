package api

// CSV export (ops-hygiene wave): ?format=csv on /api/stats/timeseries and
// /api/stats/breakdown renders the SAME computed payload as CSV with an
// attachment disposition — one URL per spreadsheet pull, no second
// aggregation path to keep in sync.

import (
	"encoding/csv"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"
)

// csvStart sets the download headers (text/csv + a dated attachment filename
// like swarmery-breakdown-project-2026-07-16.csv), emits a UTF-8 BOM so Excel
// detects the encoding, and returns the writer.
func csvStart(w http.ResponseWriter, kind string) *csv.Writer {
	filename := fmt.Sprintf("swarmery-%s-%s.csv", kind, time.Now().Format("2006-01-02"))
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	_, _ = w.Write([]byte("\xef\xbb\xbf")) // Excel needs the BOM to read UTF-8
	return csv.NewWriter(w)
}

// csvFinish flushes and surfaces any accumulated write error the same way
// writeJSON logs encode failures — headers are long gone, log is all we have.
func csvFinish(cw *csv.Writer) {
	cw.Flush()
	if err := cw.Error(); err != nil {
		log.Printf("warn: write csv response: %v", err)
	}
}

// fmtCSVFloat renders a float without artificial padding ("0.75", not "0.750000").
func fmtCSVFloat(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// writeTimeseriesCSV renders the daily series wide: a day column plus one
// column per series key, aligned to the buckets (zero-filled, like the JSON).
func writeTimeseriesCSV(w http.ResponseWriter, ts timeseriesDTO) {
	cw := csvStart(w, "timeseries")
	header := []string{"day"}
	for _, s := range ts.Series {
		header = append(header, s.Key)
	}
	_ = cw.Write(header)
	for i, day := range ts.Buckets {
		row := []string{day}
		for _, s := range ts.Series {
			row = append(row, fmtCSVFloat(s.Values[i]))
		}
		_ = cw.Write(row)
	}
	csvFinish(cw)
}

// writeBreakdownCSV renders the ranked rows; nullable measures become empty
// cells — the JSON-null contract, spelled the CSV way.
func writeBreakdownCSV(w http.ResponseWriter, by string, rows []breakdownRow) {
	cw := csvStart(w, "breakdown-"+by)
	_ = cw.Write([]string{"key", "name", "cost_usd", "tokens_in", "tokens_out",
		"runs", "sessions", "last_used", "success_rate"})
	for _, r := range rows {
		row := []string{r.Key, r.Name, "", "", "", "",
			strconv.FormatInt(r.Sessions, 10), "", ""}
		if r.CostUSD != nil {
			row[2] = fmtCSVFloat(*r.CostUSD)
		}
		if r.TokensIn != nil {
			row[3] = strconv.FormatInt(*r.TokensIn, 10)
		}
		if r.TokensOut != nil {
			row[4] = strconv.FormatInt(*r.TokensOut, 10)
		}
		if r.Runs != nil {
			row[5] = strconv.FormatInt(*r.Runs, 10)
		}
		if r.LastUsed != nil {
			row[7] = *r.LastUsed
		}
		if r.SuccessRate != nil {
			row[8] = fmtCSVFloat(*r.SuccessRate)
		}
		_ = cw.Write(row)
	}
	csvFinish(cw)
}
