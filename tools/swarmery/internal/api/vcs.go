package api

// GET /api/projects/{id}/vcs — which code host a project's repo belongs to, the
// vocabulary its UI uses for it, and whether the daemon is signed in to it.
//
// The Plans page and the project banner read it on every open, and answering it
// costs a `git remote get-url`, possibly an HTTP probe of an unknown host, and
// a `gh auth token` + `gh api user` round-trip. So the answer is cached per
// project for vcsCacheTTL; InvalidateVcsCache drops one project's entry (a
// sign-in, a config write), and MarkVcsAuthExpired records that the host just
// rejected the credentials (a not-authenticated land) without waiting for the
// TTL.
//
// Uncached answers are single-flight per project: the banner and the Plans
// page open together on a cold cache, and a Re-check (?fresh=1) can land while
// a probe is already running — each of those joins the in-flight probe and
// shares its answer instead of starting a second `gh` round-trip.
//
// `auth.status = "expired"` rule. landing_error carries no timestamp of its
// own, so "a not-authenticated landing failure newer than the last successful
// probe" is decided from what is cheaply available:
//
//  1. the probe itself answered expired (the host rejected the stored token);
//  2. MarkVcsAuthExpired(projectID) was called AFTER the cached probe ran — the
//     in-memory stamp is the "newer than the probe" timestamp;
//  3. the probe could not confirm anything (status unknown) and a phase of the
//     project carries a landing_error starting `not-authenticated`.
//
// A probe that answers ok wins over a stale landing_error (the error is only
// cleared by the next successful push, so after a re-login it would otherwise
// keep the banner up), and a probe that answers missing stays missing (no
// login at all is the more precise statement). Rules 2 and 3 only ever turn an
// ok/unknown answer into expired.

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider/credstore"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider/providers"
)

// vcsCacheTTL is how long one project's answer is reused.
const vcsCacheTTL = 60 * time.Second

// vcsProbeTimeout bounds one uncached answer (detection + auth probe). The
// providers' own network budget (repoprovider.NetTimeout) is sized for a push,
// not for a page load.
const vcsProbeTimeout = 15 * time.Second

// The process boundary of the endpoint. Package vars for the same reason
// landProvider is one: tests swap them for a repoprovider.FakeExec, a scripted
// prober and a nil credential env, with a restore in t.Cleanup.
var (
	vcsExec   repoprovider.Exec   = repoprovider.OSExec{}
	vcsProber repoprovider.Prober = repoprovider.HTTPProber{}
	vcsEnv                        = credstore.Env
)

// vcsDTO is the response (mirrored in web/src/api/types.ts as VcsInfo).
type vcsDTO struct {
	// github | gitlab | unknown
	Provider repoprovider.Kind  `json:"provider"`
	Host     string             `json:"host"`
	Terms    repoprovider.Terms `json:"terms"`
	Remote   vcsRemoteDTO       `json:"remote"`
	Auth     vcsAuthDTO         `json:"auth"`
	// The configured change-request target; "" = the host's default branch.
	BaseBranch      string `json:"baseBranch"`
	AllowPushToBase bool   `json:"allowPushToBase"`
	// Why Provider is what it is: config | host | probe | unknown.
	Source string `json:"source"`
	// The terminal command that signs the provider's CLI in to Host
	// ("gh auth login --hostname github.com"); "" for an unknown provider or
	// without a host. Computed here so the web app never branches on Provider
	// to pick a CLI (SC-11).
	CliLogin string `json:"cliLogin"`
	// True when the origin exists but its host could not be classified: the UI
	// asks the operator once which service hosts it, and PUT …/vcs/provider
	// (vcs_provider.go) stores the answer.
	AskProvider bool `json:"askProvider"`
}

type vcsRemoteDTO struct {
	// The origin URL with any credentials stripped; "" without a remote.
	URL     string `json:"url"`
	Present bool   `json:"present"`
	// https | ssh; "" without a remote.
	Protocol string `json:"protocol"`
}

type vcsAuthDTO struct {
	// ok | missing | expired | unknown
	Status string `json:"status"`
	Login  string `json:"login"`
	// cli | store | none
	Source string `json:"source"`
}

// vcsCacheEntry is one project's cached answer and when it was computed.
type vcsCacheEntry struct {
	dto vcsDTO
	at  time.Time
}

// vcsProbeCall is one in-flight uncached answer; done closes once entry is set.
type vcsProbeCall struct {
	done  chan struct{}
	entry vcsCacheEntry
}

// vcsCache is the per-project cache. now is injectable for the TTL tests;
// onJoin (nil outside tests) observes a request joining an in-flight probe.
type vcsCache struct {
	mu        sync.Mutex
	now       func() time.Time
	entries   map[int64]vcsCacheEntry
	expiredAt map[int64]time.Time
	inflight  map[int64]*vcsProbeCall
	onJoin    func(projectID int64)
}

func newVcsCache() *vcsCache {
	return &vcsCache{
		now:       time.Now,
		entries:   map[int64]vcsCacheEntry{},
		expiredAt: map[int64]time.Time{},
		inflight:  map[int64]*vcsProbeCall{},
	}
}

// projectVcsCache is the process-wide cache behind the endpoint and the two
// exported hooks.
var projectVcsCache = newVcsCache()

// InvalidateVcsCache drops projectID's cached answer, so the next GET re-detects
// and re-probes. Callers: a sign-in or token import, a vcs config write.
func InvalidateVcsCache(projectID int64) {
	c := projectVcsCache
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, projectID)
}

// MarkVcsAuthExpired records that the code host just rejected the project's
// credentials (a not-authenticated land or status poll). Until a probe that
// runs AFTER this call answers ok, GET /vcs reports auth.status "expired".
func MarkVcsAuthExpired(projectID int64) {
	c := projectVcsCache
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expiredAt[projectID] = c.now()
}

// get returns projectID's answer: the cached one while fresh (unless fresh is
// set), else the answer of the probe already in flight for the project, else
// the answer of a new probe run by compute. The entry is stamped with the
// moment the probe STARTED, so a MarkVcsAuthExpired racing the probe is not
// lost. A caller whose ctx ends while it waits on another request's probe
// gets ctx's error; the probe itself runs on.
func (c *vcsCache) get(ctx context.Context, projectID int64, fresh bool, compute func() vcsDTO) (vcsDTO, error) {
	c.mu.Lock()
	if !fresh {
		if e, ok := c.entries[projectID]; ok && c.now().Sub(e.at) < vcsCacheTTL {
			d := c.overlayLocked(projectID, e)
			c.mu.Unlock()
			return d, nil
		}
	}
	if call, ok := c.inflight[projectID]; ok {
		onJoin := c.onJoin
		c.mu.Unlock()
		if onJoin != nil {
			onJoin(projectID)
		}
		select {
		case <-call.done:
		case <-ctx.Done():
			return vcsDTO{}, ctx.Err()
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.overlayLocked(projectID, call.entry), nil
	}
	call := &vcsProbeCall{done: make(chan struct{})}
	c.inflight[projectID] = call
	call.entry.at = c.now()
	c.mu.Unlock()

	// Deferred so a panicking probe still releases the requests joined to it.
	defer func() {
		c.mu.Lock()
		delete(c.inflight, projectID)
		c.mu.Unlock()
		close(call.done)
	}()
	call.entry.dto = compute()

	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[projectID] = call.entry
	return c.overlayLocked(projectID, call.entry), nil
}

// overlayLocked applies rule 2 of the file header. Caller holds mu.
func (c *vcsCache) overlayLocked(projectID int64, e vcsCacheEntry) vcsDTO {
	d := e.dto
	if t, ok := c.expiredAt[projectID]; ok && !t.Before(e.at) && overridableAuth(d.Auth.Status) {
		d.Auth.Status = repoprovider.AuthExpired
	}
	return d
}

// overridableAuth: only an ok or unknown answer may be turned into expired.
func overridableAuth(status string) bool {
	return status == repoprovider.AuthOK || status == repoprovider.AuthUnknown
}

// projectVcs handles GET /api/projects/{id}/vcs.
func (h *Handler) projectVcs(w http.ResponseWriter, r *http.Request) {
	id, path, ok := h.projectPathByID(w, r)
	if !ok {
		return
	}
	// ?fresh=1 is the banner's "Re-check" after a terminal sign-in: the
	// operator just changed what the probe would answer, so the cached answer
	// is known stale (a probe already in flight is still joined). Every other
	// read takes the cache.
	fresh := r.URL.Query().Get("fresh") == "1"
	dto, err := projectVcsCache.get(r.Context(), id, fresh, func() vcsDTO {
		// Detached from the request that happened to start it: other requests
		// share this probe, so one client going away must not cut it short.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), vcsProbeTimeout)
		defer cancel()
		d := buildVcsDTO(ctx, path)
		if d.Auth.Status == repoprovider.AuthUnknown && h.projectHasAuthLandingError(id) {
			d.Auth.Status = repoprovider.AuthExpired // rule 3
		}
		return d
	})
	if err != nil {
		return // the client went away while waiting on another request's probe
	}
	writeJSON(w, dto, nil)
}

// projectHasAuthLandingError reports whether any phase of the project carries a
// not-authenticated landing_error (stamped by writeLandFailure). A read error
// counts as "no": the banner must not claim expiry on a guess.
func (h *Handler) projectHasAuthLandingError(projectID int64) bool {
	var one int
	err := h.DB.QueryRow(`
		SELECT 1 FROM epic_phases e
		  JOIN tasks t ON t.id = e.workspace_task_id
		 WHERE t.project_id = ? AND e.landing_error LIKE ?
		 LIMIT 1`, projectID, codeNotAuthenticated+"%").Scan(&one)
	return err == nil
}

// vcsCliLogin is the provider CLI's sign-in command for host, or "" when the
// provider has no CLI the daemon drives (unknown) or there is no host.
func vcsCliLogin(kind repoprovider.Kind, host string) string {
	if host == "" {
		return ""
	}
	switch kind {
	case repoprovider.KindGitHub:
		return "gh auth login --hostname " + host
	case repoprovider.KindGitLab:
		return "glab auth login --hostname " + host
	default:
		return ""
	}
}

// buildVcsDTO computes the uncached answer for the repo at projectPath. Every
// failure degrades to a field value — no remote, unknown provider, unknown
// auth — never to an HTTP error: the banner has to render whatever is true.
func buildVcsDTO(ctx context.Context, projectPath string) vcsDTO {
	cfg := repoprovider.LoadConfig(projectPath)
	dto := vcsDTO{
		BaseBranch:      cfg.BaseBranch,
		AllowPushToBase: cfg.AllowPushToBase,
		Auth:            vcsAuthDTO{Status: repoprovider.AuthUnknown, Source: repoprovider.SourceNone},
	}
	det, err := repoprovider.Detect(ctx, vcsExec, projectPath, cfg, vcsProber)
	if err != nil {
		// No usable origin (or no git): nothing to push to and no host to ask.
		kind, source := cfg.ExplicitKind(), repoprovider.SourceConfig
		if kind == "" {
			kind, source = repoprovider.KindUnknown, repoprovider.SourceUnknown
		}
		dto.Provider, dto.Source, dto.Terms = kind, source, repoprovider.TermsFor(kind)
		return dto
	}
	dto.Provider, dto.Source, dto.Terms = det.Kind, det.Source, det.Terms
	dto.Host = det.Remote.Host
	dto.CliLogin = vcsCliLogin(det.Kind, dto.Host)
	dto.Remote = vcsRemoteDTO{
		URL:      credstore.Redact(det.Remote.URL),
		Present:  true,
		Protocol: det.Remote.Protocol,
	}
	provider, err := providers.Factory(det.Kind, vcsExec, vcsEnv)
	if err != nil {
		// Unknown host (ErrUnknownProvider): there is no CLI to ask, so the
		// operator is asked instead.
		dto.AskProvider = true
		return dto
	}
	dto.Terms = provider.Terms()
	// A non-nil error comes with status unknown (an unclassified failure); the
	// status is still the answer.
	st, _ := provider.AuthStatus(ctx, dto.Host)
	if st.Status == "" {
		return dto
	}
	dto.Auth = vcsAuthDTO{Status: st.Status, Login: strings.TrimSpace(st.Login), Source: st.Source}
	if dto.Auth.Source == "" {
		dto.Auth.Source = repoprovider.SourceNone
	}
	return dto
}
