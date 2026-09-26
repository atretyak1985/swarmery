package api

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/projectscan"
)

func managedRow(id int64, path string) projectDTO {
	return projectDTO{ID: id, Path: path, Plugin: &projectscan.PluginState{Managed: true}}
}

func onboardedByID(ps []projectDTO) map[int64]bool {
	out := map[int64]bool{}
	for _, p := range ps {
		out[p.ID] = p.Onboarded
	}
	return out
}

func TestMarkOnboarded(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, "code")

	cases := []struct {
		name  string
		rows  []projectDTO
		roots []string
		want  map[int64]bool
	}{
		{"unmanaged is never onboarded",
			[]projectDTO{{ID: 1, Path: "/x/a"}, {ID: 2, Path: "/x/b", Plugin: &projectscan.PluginState{}}},
			nil, map[int64]bool{1: false, 2: false}},
		{"umbrella sub-repo is demoted, umbrella stays",
			[]projectDTO{managedRow(1, "/x/umbrella"), managedRow(2, "/x/umbrella/platform/next")},
			nil, map[int64]bool{1: true, 2: false}},
		{"a path-prefix sibling is not an ancestor",
			[]projectDTO{managedRow(1, "/x/foo"), managedRow(2, "/x/foobar")},
			nil, map[int64]bool{1: true, 2: true}},
		{"managed $HOME neither counts nor hides its children",
			[]projectDTO{managedRow(1, home), managedRow(2, filepath.Join(home, "proj"))},
			nil, map[int64]bool{1: false, 2: true}},
		{"managed onboarding root neither counts nor hides its children",
			[]projectDTO{managedRow(1, root), managedRow(2, filepath.Join(root, "a")), managedRow(3, filepath.Join(root, "b"))},
			[]string{root}, map[int64]bool{1: false, 2: true, 3: true}},
		{"managed / neither counts nor hides",
			[]projectDTO{managedRow(1, "/"), managedRow(2, "/x/a")},
			nil, map[int64]bool{1: false, 2: true}},
		{"archived umbrella does not demote",
			[]projectDTO{func() projectDTO { p := managedRow(1, "/x/u"); p.Archived = true; return p }(), managedRow(2, "/x/u/sub")},
			nil, map[int64]bool{2: true, 1: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			markOnboarded(tc.rows, tc.roots)
			got := onboardedByID(tc.rows)
			for id, want := range tc.want {
				if got[id] != want {
					t.Errorf("project %d: onboarded = %v, want %v", id, got[id], want)
				}
			}
		})
	}
}

// The list and detail routes agree: the managed fixture project is onboarded,
// the telemetry-only one is not; a managed sub-repo nested under it is not.
func TestProjectsOnboardedField(t *testing.T) {
	srv, db := projectsTestServer(t)
	var managedPath string
	if err := db.QueryRow(`SELECT path FROM projects WHERE id = 1`).Scan(&managedPath); err != nil {
		t.Fatal(err)
	}
	subPath := filepath.Join(managedPath, "sub")
	writeProjectSettings(t, subPath, `{"enabledPlugins": {"core@swarmery": true}}`)
	execSQL(t, db, `INSERT INTO projects (id, path, slug, name, first_seen, last_activity, archived)
		VALUES (4, ?, 'sub', 'Sub', '2026-07-10T00:00:00Z', '2026-07-10T00:00:00Z', 0)`, subPath)

	resp, err := http.Get(srv.URL + "/api/projects")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var list []projectDTO
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	got := onboardedByID(list)
	for id, want := range map[int64]bool{1: true, 2: false, 4: false} {
		if got[id] != want {
			t.Errorf("list project %d: onboarded = %v, want %v", id, got[id], want)
		}
	}

	for id, want := range map[string]bool{"1": true, "4": false} {
		r, err := http.Get(srv.URL + "/api/projects/" + id)
		if err != nil {
			t.Fatal(err)
		}
		var d projectDetailDTO
		err = json.NewDecoder(r.Body).Decode(&d)
		r.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if d.Project.Onboarded != want {
			t.Errorf("detail project %s: onboarded = %v, want %v", id, d.Project.Onboarded, want)
		}
	}
}
