package redact

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// CountLiteral is Count over the redactor for code: classes 1–8 and 11 plus
// PEM private-key blocks.
func CountLiteral(s string) map[string]int { return countWith(storageLiteralRules, s) }

// countWith is Count's algorithm over an arbitrary rule list.
func countWith(rules []redactRule, s string) map[string]int {
	var out map[string]int
	for _, r := range rules {
		n := 0
		for _, m := range r.re.FindAllStringSubmatchIndex(s, -1) {
			if string(r.re.ExpandString(nil, r.repl, s, m)) != s[m[0]:m[1]] {
				n++
			}
		}
		if n > 0 {
			if out == nil {
				out = map[string]int{}
			}
			out[r.name] += n
			s = r.re.ReplaceAllString(s, r.repl)
		}
	}
	return out
}

// StoredCount is what CountStored found: per "table.column", per rule class,
// the number of values the rescrub would mask. Values are never reported.
type StoredCount struct {
	Columns map[string]map[string]int
	Rows    int // rows read
}

// Total is the sum over every column and class.
func (c StoredCount) Total() int {
	n := 0
	for _, byClass := range c.Columns {
		for _, v := range byClass {
			n += v
		}
	}
	return n
}

// Lines renders the count as sorted "table.column class n" lines, then a
// total line.
func (c StoredCount) Lines() []string {
	var out []string
	for col, byClass := range c.Columns {
		for class, n := range byClass {
			out = append(out, fmt.Sprintf("%-22s %-18s %d", col, class, n))
		}
	}
	sort.Strings(out)
	return append(out, fmt.Sprintf("total %d (rows read %d)", c.Total(), c.Rows))
}

// CountStored reads every row the rescrub covers (Targets, with the same
// filters and the same redactor per value) and counts, per class, the values
// a rescrub would mask. It only reads: point it at a copy of a store.
func CountStored(ctx context.Context, db *sql.DB) (StoredCount, error) {
	out := StoredCount{Columns: map[string]map[string]int{}}
	for _, t := range targets {
		if t.name != "" {
			continue // a subset of another target's rows, counted there
		}
		q := "SELECT " + strings.Join(append(append([]string{}, t.cols...), t.ctx...), ", ") + " FROM " + t.table
		if t.where != "" {
			q += " WHERE " + t.where
		}
		rows, err := db.QueryContext(ctx, q)
		if err != nil {
			return out, fmt.Errorf("count %s: %w", t.table, err)
		}
		n := len(t.cols) + len(t.ctx)
		for rows.Next() {
			vals := make([]sql.NullString, n)
			dest := make([]any, n)
			for i := range vals {
				dest[i] = &vals[i]
			}
			if err := rows.Scan(dest...); err != nil {
				rows.Close()
				return out, fmt.Errorf("count %s: %w", t.table, err)
			}
			out.Rows++
			for i, col := range t.cols {
				if !vals[i].Valid || vals[i].String == "" {
					continue
				}
				var c map[string]int
				switch t.table {
				case "events", "permission_requests":
					c = countPayload(vals[len(t.cols)].String, vals[i].String)
				case "link_events":
					c = countPayload("", vals[i].String)
				case "file_changes":
					c = CountLiteral(vals[i].String)
				default:
					c = Count(vals[i].String)
				}
				if len(c) == 0 {
					continue
				}
				key := t.table + "." + col
				if out.Columns[key] == nil {
					out.Columns[key] = map[string]int{}
				}
				for class, k := range c {
					out.Columns[key][class] += k
				}
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return out, fmt.Errorf("count %s: %w", t.table, err)
		}
	}
	return out, nil
}

// countPayload counts a stored events.payload or permission_requests
// request_json leaf by leaf, with Literal's
// classes inside the code fields of the file-editing tools — the same choice
// PayloadJSON makes, so a rescrubbed store counts zero.
func countPayload(toolName, doc string) map[string]int {
	var v any
	if json.Unmarshal([]byte(doc), &v) != nil {
		return Count(doc)
	}
	var fields map[string]bool
	if codeEditTools[toolName] {
		fields = codeFields
	}
	acc := map[string]int{}
	countTree(v, fields, false, acc)
	return acc
}

func countTree(v any, fields map[string]bool, literal bool, acc map[string]int) {
	switch x := v.(type) {
	case string:
		c := Count(x)
		if literal {
			c = CountLiteral(x)
		}
		for class, n := range c {
			acc[class] += n
		}
	case map[string]any:
		for k, e := range x {
			sub := literal || fields[k]
			rec := func(v any) { countTree(v, fields, sub, acc) }
			if c := keyCue(k); c != noCue && !literal && countCued(c, e, acc, rec) {
				continue
			}
			rec(e)
		}
	case []any:
		for _, e := range x {
			countTree(e, fields, literal, acc)
		}
	}
}
