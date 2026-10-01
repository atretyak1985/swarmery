package api

import (
	"testing"
	"time"
)

// A GET that arrives after an invalidation must never join a load that
// started BEFORE the write: it starts a fresh load and gets the post-write view.
func TestBindingCacheInvalidationDetachesInFlightLoad(t *testing.T) {
	var c bindingCache
	release := make(chan struct{})
	started := make(chan struct{})
	stale := bindingView{indexed: map[string][]string{"k": {"/stale"}}}
	fresh := bindingView{indexed: map[string][]string{"k": {"/fresh"}}}

	oldDone := make(chan bindingView)
	go func() {
		v, _ := c.get(func() (bindingView, error) {
			close(started)
			<-release
			return stale, nil
		})
		oldDone <- v
	}()
	<-started
	c.invalidate() // the write lands while the old load is still running

	got := make(chan bindingView)
	go func() {
		v, _ := c.get(func() (bindingView, error) { return fresh, nil })
		got <- v
	}()
	select {
	case v := <-got:
		if v.indexed["k"][0] != "/fresh" {
			t.Fatalf("post-write GET got %v, want the fresh view", v.indexed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the post-write GET joined the pre-write load")
	}
	close(release)
	if v := <-oldDone; v.indexed["k"][0] != "/stale" {
		t.Errorf("the pre-write caller got %v", v.indexed)
	}
	// The pre-write load finishing late must not overwrite the fresh cache.
	v, _ := c.get(func() (bindingView, error) { t.Error("cache miss"); return bindingView{}, nil })
	if v.indexed["k"][0] != "/fresh" {
		t.Errorf("cached view = %v, want the fresh one", v.indexed)
	}
}
