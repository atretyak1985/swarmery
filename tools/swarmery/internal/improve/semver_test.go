package improve

import "testing"

func TestBumpPatch(t *testing.T) {
	cases := []struct {
		in, want string
		wantErr  bool
	}{
		{in: "2.2.0", want: "2.2.1"},
		{in: "2.2.9", want: "2.2.10"},
		{in: "0.0.0", want: "0.0.1"},
		{in: "10.4.99", want: "10.4.100"},
		{in: "1.2", wantErr: true},
		{in: "1.2.3.4", wantErr: true},
		{in: "1.2.x", wantErr: true},
		{in: "", wantErr: true},
	}
	for _, c := range cases {
		got, err := bumpPatch(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("bumpPatch(%q) = %q, want error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("bumpPatch(%q) unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("bumpPatch(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// bumpCoreVersionInJSON must rewrite only the "version" field, preserving the
// rest of the document byte-for-byte where possible (indentation, key order).
func TestBumpVersionField(t *testing.T) {
	in := "{\n  \"name\": \"core\",\n  \"version\": \"2.2.0\",\n  \"author\": {\"name\": \"x\"}\n}\n"
	out, old, err := bumpVersionField([]byte(in), "2.2.1")
	if err != nil {
		t.Fatalf("bumpVersionField: %v", err)
	}
	if old != "2.2.0" {
		t.Errorf("old version = %q, want 2.2.0", old)
	}
	if want := "\"version\": \"2.2.1\""; !contains(string(out), want) {
		t.Errorf("output missing %q:\n%s", want, out)
	}
	if contains(string(out), "\"version\": \"2.2.0\"") {
		t.Errorf("output still has old version:\n%s", out)
	}
	// Untouched keys survive.
	if !contains(string(out), "\"name\": \"core\"") || !contains(string(out), "\"author\"") {
		t.Errorf("output mangled other keys:\n%s", out)
	}
}

func TestBumpVersionFieldMissing(t *testing.T) {
	if _, _, err := bumpVersionField([]byte(`{"name":"core"}`), "1.0.1"); err == nil {
		t.Fatal("expected error when no version field present")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
