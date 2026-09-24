package api

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// TestDTOSlicesNeverNull pins the contract the Plans and Sessions pages map
// over: every slice-typed DTO field encodes as [] — never null. One nullable
// slice (runEvents) crashed the whole Plans page after an install.
//
// The walk is TYPE-DIRECTED: it reflects over the Go DTO the endpoint encodes
// and descends the decoded JSON alongside it, so a slice field added to any
// DTO — or to a nested type from another package — is covered without
// touching this test.
func TestDTOSlicesNeverNull(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)

	// One session with no turns, events or file changes: the shape every
	// just-started run has, and the one whose aggregates come back empty.
	if _, err := db.Exec(`INSERT INTO sessions (id, project_id, session_uuid, status, started_at)
		VALUES (7, 1, 'uuid-empty', 'running', '2026-07-24T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	// Phase 1 has run (in that session); phase 2 never has. The plan run row
	// makes planRun non-null so its own slices are walked too.
	if _, err := db.Exec(`UPDATE epic_phases SET run_state = 'done', run_session_uuid = 'uuid-empty',
		run_started_at = '2026-07-24T00:00:00Z' WHERE workspace_task_id = ? AND seq = 1`, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO plan_runs (workspace_task_id, run_state, run_session_uuid, run_started_at)
		VALUES (?, 'done', 'uuid-empty', '2026-07-24T00:00:00Z')`, taskID); err != nil {
		t.Fatal(err)
	}
	// A spec with one criterion no phase covers, a phase covering an id the
	// spec never declared, and a posterior with no prior whose optional lists
	// are empty or null — so spec.criteria[].coveredBy, spec.unknownRefs,
	// forecasts[].areas/files/risks and forecastLints are all walked.
	if _, err := db.Exec(`INSERT INTO spec_criteria (workspace_task_id, pos, cid, text)
		VALUES (?, 0, 'SC-1', 'uncovered criterion')`, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE epic_phases SET covers = '["SC-9"]'
		WHERE workspace_task_id = ? AND seq = 1`, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO phase_forecasts (phase_id, kind, areas_json, files_json, risks_json)
		SELECT id, 'posterior', 'null', '[]', '' FROM epic_phases
		 WHERE workspace_task_id = ? AND seq = 1`, taskID); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		path string
		typ  reflect.Type
	}{
		{"/api/epics", reflect.TypeFor[[]epicDTO]()},
		{"/api/epics?projectId=1", reflect.TypeFor[[]epicDTO]()},
		{"/api/sessions", reflect.TypeFor[sessionsPageDTO]()},
		{"/api/sessions/7", reflect.TypeFor[sessionDetailDTO]()},
		{"/api/sessions/uuid-empty", reflect.TypeFor[sessionDetailDTO]()},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			var body any
			getJSON(t, srv.URL+c.path, &body)
			if body == nil {
				t.Fatalf("GET %s: body is null", c.path)
			}
			walked := assertNoNullSlices(t, body, c.typ, "$")
			t.Logf("GET %s: %d slice fields walked", c.path, walked)
			if walked == 0 {
				t.Fatalf("GET %s: walked no slice field — the fixture or the DTO mapping is off", c.path)
			}
		})
	}
}

var (
	jsonMarshalerType = reflect.TypeFor[json.Marshaler]()
	rawMessageType    = reflect.TypeFor[json.RawMessage]()
)

// assertNoNullSlices descends v (decoded JSON) alongside typ (the Go type that
// encoded it) and reports every slice-typed field whose value is null. It
// returns how many slice-typed fields it saw, so a caller can tell a clean walk
// from one that never matched the payload.
func assertNoNullSlices(t *testing.T, v any, typ reflect.Type, path string) int {
	t.Helper()
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	// A custom encoder owns its shape; json.RawMessage is opaque by design
	// (an event payload may legitimately be null).
	if typ == rawMessageType || typ.Implements(jsonMarshalerType) || reflect.PointerTo(typ).Implements(jsonMarshalerType) {
		return 0
	}
	seen := 0
	switch typ.Kind() {
	case reflect.Struct:
		obj, ok := v.(map[string]any)
		if !ok {
			return 0
		}
		for _, f := range jsonFields(typ) {
			val, present := obj[f.name]
			if !present {
				continue // omitempty
			}
			if isJSONArraySlice(f.typ) {
				seen++
				if val == nil {
					t.Errorf("%s.%s is null; a %s must encode as []", path, f.name, f.typ)
					continue
				}
			}
			seen += assertNoNullSlices(t, val, f.typ, path+"."+f.name)
		}
	case reflect.Slice, reflect.Array:
		arr, ok := v.([]any)
		if !ok {
			return 0
		}
		for i, el := range arr {
			seen += assertNoNullSlices(t, el, typ.Elem(), path+"["+strconv.Itoa(i)+"]")
		}
	case reflect.Map:
		obj, ok := v.(map[string]any)
		if !ok {
			return 0
		}
		for k, el := range obj {
			if isJSONArraySlice(typ.Elem()) {
				seen++
				if el == nil {
					t.Errorf("%s[%q] is null; a %s must encode as []", path, k, typ.Elem())
					continue
				}
			}
			seen += assertNoNullSlices(t, el, typ.Elem(), path+"["+k+"]")
		}
	}
	return seen
}

// isJSONArraySlice reports whether encoding/json renders typ as an array —
// every slice except []byte (base64 string) and json.RawMessage.
func isJSONArraySlice(typ reflect.Type) bool {
	return typ.Kind() == reflect.Slice && typ.Elem().Kind() != reflect.Uint8
}

type jsonField struct {
	name string
	typ  reflect.Type
}

// jsonFields lists the JSON keys encoding/json emits for a struct, flattening
// untagged embedded structs (sessionDetailDTO embeds sessionDTO) the way the
// encoder does.
func jsonFields(typ reflect.Type) []jsonField {
	var out []jsonField
	for i := range typ.NumField() {
		f := typ.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		ft := f.Type
		if f.Anonymous && name == "" {
			et := ft
			if et.Kind() == reflect.Pointer {
				et = et.Elem()
			}
			if et.Kind() == reflect.Struct {
				out = append(out, jsonFields(et)...)
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		out = append(out, jsonField{name: name, typ: ft})
	}
	return out
}
