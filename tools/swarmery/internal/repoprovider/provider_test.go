package repoprovider

import "testing"

func TestTermsNeverEmpty(t *testing.T) {
	cases := map[Kind]Terms{
		KindGitHub:  {Provider: "GitHub", Change: "Pull Request", ChangeShort: "PR"},
		KindGitLab:  {Provider: "GitLab", Change: "Merge Request", ChangeShort: "MR"},
		KindUnknown: {Provider: "Repository", Change: "Change request", ChangeShort: "CR"},
		"":          {Provider: "Repository", Change: "Change request", ChangeShort: "CR"},
		"bitbucket": {Provider: "Repository", Change: "Change request", ChangeShort: "CR"},
	}
	for k, want := range cases {
		got := TermsFor(k)
		if got != want {
			t.Errorf("TermsFor(%q) = %+v, want %+v", k, got, want)
		}
		if got.Provider == "" || got.Change == "" || got.ChangeShort == "" {
			t.Errorf("TermsFor(%q) has an empty value: %+v", k, got)
		}
	}
}

func TestRemoteSlug(t *testing.T) {
	r := Remote{Owner: "group/sub", Repo: "app"}
	if r.Slug() != "group/sub/app" {
		t.Fatalf("Slug = %q", r.Slug())
	}
}
