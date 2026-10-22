package redact

// Structured redaction: the JSON payloads ingest stores per event, walked leaf
// by leaf so the masking never breaks the JSON around a value.
//
// Two redactors, chosen per value:
//   - Literal for CODE — the content / old / new strings, edit lists and
//     patches of the file-editing tools. The contextual rules (an
//     authorization-header scheme word, a key=value assignment) would rewrite
//     ordinary source code there: an assignment to a variable named token is
//     not a credential.
//   - All for everything else: prompts, prose, Bash input and output, Read
//     results, raw records.

import (
	"bytes"
	"encoding/json"
)

// codeEditTools are the tools whose payload fields in codeFields hold code.
var codeEditTools = map[string]bool{"Write": true, "Edit": true, "MultiEdit": true, "NotebookEdit": true}

// codeFields are the input (snake_case) and result (camelCase) keys of the
// file-editing tools that carry file content, at any depth of the payload.
var codeFields = map[string]bool{
	"content": true, "old_string": true, "new_string": true, "edits": true,
	"new_source": true, "old_source": true,
	"oldString": true, "newString": true, "structuredPatch": true,
	"originalFile": true, "originalFileContents": true,
}

// IsCodeEditTool reports whether toolName's payload carries code fields.
func IsCodeEditTool(toolName string) bool { return codeEditTools[toolName] }

// Value returns v with every string leaf redacted, for a payload of toolName
// ("" for events that are not tool calls), and whether anything changed. v
// itself is never modified: a changed subtree is copied, an unchanged one is
// returned as is. Embedded json.RawMessage values are decoded, redacted and
// re-encoded only when a leaf changed.
func Value(toolName string, v any) (any, bool) {
	var fields map[string]bool
	if codeEditTools[toolName] {
		fields = codeFields
	}
	return tree(v, fields, false)
}

// PayloadJSON redacts a stored events.payload document for toolName. It
// returns the input unchanged (and false) when nothing matched, so a clean row
// keeps its exact bytes. A document that is not JSON is redacted as text.
func PayloadJSON(toolName, doc string) (string, bool) {
	if doc == "" {
		return doc, false
	}
	out, changed := Value(toolName, json.RawMessage(doc))
	if !changed {
		return doc, false
	}
	switch x := out.(type) {
	case json.RawMessage:
		return string(x), true
	case string:
		return x, true
	}
	return doc, false
}

// tree walks v; literal switches the walk to Literal, which a key in fields
// turns on for its whole subtree. Reports whether anything changed.
func tree(v any, fields map[string]bool, literal bool) (any, bool) {
	switch x := v.(type) {
	case string:
		r := text(x, literal)
		return r, r != x
	case map[string]any:
		var out map[string]any
		for k, e := range x {
			ne, changed := tree(e, fields, literal || fields[k])
			if !changed {
				continue
			}
			if out == nil {
				out = make(map[string]any, len(x))
				for k2, e2 := range x {
					out[k2] = e2
				}
			}
			out[k] = ne
		}
		if out == nil {
			return x, false
		}
		return out, true
	case []any:
		var out []any
		for i, e := range x {
			ne, changed := tree(e, fields, literal)
			if !changed {
				continue
			}
			if out == nil {
				out = append([]any(nil), x...)
			}
			out[i] = ne
		}
		if out == nil {
			return x, false
		}
		return out, true
	case []string:
		var out []string
		for i, e := range x {
			r := text(e, literal)
			if r == e {
				continue
			}
			if out == nil {
				out = append([]string(nil), x...)
			}
			out[i] = r
		}
		if out == nil {
			return x, false
		}
		return out, true
	case json.RawMessage:
		return raw(x, fields, literal)
	default:
		return v, false
	}
}

// raw redacts inside an embedded raw JSON value (a whole unknown record, an
// api_error body). The bytes are kept verbatim unless a value changed, so a
// clean record is stored exactly as it was read.
func raw(doc json.RawMessage, fields map[string]bool, literal bool) (any, bool) {
	if len(doc) == 0 {
		return doc, false
	}
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil || dec.More() {
		// Not one JSON value: redact it as text. A changed text is returned
		// as a string (a RawMessage must stay valid JSON).
		s := string(doc)
		if r := text(s, literal); r != s {
			return r, true
		}
		return doc, false
	}
	nv, changed := tree(v, fields, literal)
	if !changed {
		return doc, false
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(nv); err != nil {
		return text(string(doc), literal), true
	}
	return json.RawMessage(bytes.TrimRight(buf.Bytes(), "\n")), true
}

func text(s string, literal bool) string {
	if literal {
		return Literal(s)
	}
	return All(s)
}
