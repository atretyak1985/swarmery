package acctops

// `swarmery account switch <key>` — move a whole ESTATE's payer in one command.
//
// It is `account use` on the estate root plus three guards:
//
//  1. It operates on a DECLARED estate only. The root comes from --estate, else
//     claudeacct.Resolve(cwd).EstateRoot — and from nothing else. There is no
//     "else cwd" rung: a switch typed inside one repo of a nine-repo estate
//     that silently re-bound only that repo is the exact failure it prevents.
//     --estate is a pointer, not an override: a root that declares no estate
//     is refused the same way.
//  2. It refuses a target account whose quota headroom it cannot vouch for
//     (quota.Headroom unknown) unless --force.
//  3. It classifies the shadowing descendant pins, never clears a disagreeing
//     one, and with --clear-pins clears only pins redundant against the
//     estate's account BOTH before and after the command — on an
//     account-changing run, nothing by construction.
//
// The write is claudeacct.SetBinding: it aborts without writing on unparseable
// JSON, preserves every foreign key, is idempotent, and backs up to .bak once.
// A credential store is reported as a COUNT and a path — never a variable's
// name, never a value.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/quota"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// quotaIntervalEnv is the daemon's poll-interval knob, read here too so the
// staleness bound follows the cadence the poller actually runs at.
const quotaIntervalEnv = "SWARMERY_QUOTA_INTERVAL"

// HeadroomMaxAge is how old a stored quota reading may be and still vouch for
// an account: three EFFECTIVE poll intervals of the configured
// SWARMERY_QUOTA_INTERVAL value, so one missed tick does not turn a healthy
// account into "unknown" and a slow cadence does not turn every reading stale.
// polling=false when the value disables the poller; the bound then falls back
// to three default intervals (no new reading will arrive to refresh it).
func HeadroomMaxAge(intervalValue string) (maxAge time.Duration, polling bool) {
	d, on, _ := quota.ParseInterval(intervalValue) // an invalid value is the daemon's default, as it runs
	if !on {
		return 3 * quota.DefaultInterval, false
	}
	return 3 * d, true
}

// ErrNoEstate is returned (wrapped) when no declared estate root was found.
var ErrNoEstate = errors.New("no declared estate")

// ErrHeadroomUnknown is returned (wrapped) when the target's headroom is
// unknown and --force was not given.
var ErrHeadroomUnknown = errors.New("headroom unknown")

// SwitchOptions are `switch`'s inputs, already parsed.
type SwitchOptions struct {
	Key       string // the account to switch the estate to
	Estate    string // --estate <root>; "" resolves from Cwd
	Cwd       string // the working directory (absolute)
	Force     bool   // proceed with unknown headroom
	ClearPins bool   // sweep redundant pins (standing-still runs only)
	DryRun    bool   // report, write nothing

	DBPath string           // "" → store.DefaultDBPath()
	Now    func() time.Time // nil → time.Now
	MaxAge time.Duration    // 0 → HeadroomMaxAge($SWARMERY_QUOTA_INTERVAL)
}

// SwitchReport is what a switch did (or, on --dry-run / refusal, would do).
type SwitchReport struct {
	EstateRoot   string
	EstateSource string // "binding" | "--estate"
	EstateKey    string
	OldAccount   string
	NewAccount   string
	ConfigDir    string

	HeadroomKnown  bool
	Headroom       quota.H
	HeadroomMaxAge time.Duration // the staleness bound applied
	LastReading    time.Time     // newest reading however old; zero when HasReading is false
	HasReading     bool          // a reading exists (it may be stale)
	PollingOff     bool          // SWARMERY_QUOTA_INTERVAL disables the poller
	Now            time.Time

	Credentials     int
	CredentialStore string // the store path for the estate key
	StoreState      claudeacct.StoreState
	StoreReason     string

	Pins       []Pin
	Untrusted  []string
	Cleared    []string
	ClearAsked bool

	Wrote  bool
	DryRun bool
}

// Redundant and Disagreeing split r.Pins.
func (r SwitchReport) Redundant() []Pin   { return filterPins(r.Pins, true) }
func (r SwitchReport) Disagreeing() []Pin { return filterPins(r.Pins, false) }

func filterPins(pins []Pin, redundant bool) []Pin {
	var out []Pin
	for _, p := range pins {
		if p.Redundant == redundant {
			out = append(out, p)
		}
	}
	return out
}

// Switch runs the command. A refusal returns a non-nil error AND the report
// gathered so far; nothing is written on any refusal path.
func Switch(opts SwitchOptions) (SwitchReport, error) {
	rep := SwitchReport{DryRun: opts.DryRun}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	rep.Now = now()

	root, source, err := estateRoot(opts)
	if err != nil {
		return rep, err
	}
	rep.EstateRoot, rep.EstateSource = root, source
	res := claudeacct.Resolve(root)
	rep.EstateKey = res.Estate
	rep.OldAccount = res.Account
	if rep.OldAccount == "" {
		rep.OldAccount = ingest.DefaultAccount
	}

	key := strings.TrimSpace(opts.Key)
	if !claudeacct.ValidKey(key) {
		return rep, fmt.Errorf("%q is not a valid account key", key)
	}
	dir, installed := AccountConfigDir(key)
	if !installed && key != ingest.DefaultAccount {
		return rep, fmt.Errorf("unknown account %q — no config dir for it on this machine; "+
			"every session in the estate would start with no login", key)
	}
	rep.NewAccount, rep.ConfigDir = key, dir

	rep.CredentialStore = claudeacct.SecretsPath(rep.EstateKey)
	rep.StoreState, rep.StoreReason = res.CredentialStore()
	rep.Credentials = res.CredentialCount()

	rep.Pins, rep.Untrusted = classifyPins(root, rep.OldAccount)

	headroom(opts, key, &rep)
	if !rep.HeadroomKnown && !opts.Force {
		return rep, fmt.Errorf("%w: headroom for account %s is %s — %s; rerun with --force to switch anyway",
			ErrHeadroomUnknown, key, rep.headroomState(), rep.pollerNote())
	}

	rep.ClearAsked = opts.ClearPins
	if opts.DryRun {
		return rep, nil
	}

	changed := claudeacct.Binding(root) != key
	if err := claudeacct.SetBinding(root, key); err != nil {
		return rep, err
	}
	if err := claudeacct.VerifyBinding(root, key); err != nil {
		return rep, err
	}
	rep.Wrote = changed

	// Redundant before (held == old) AND after (held == new): only a
	// standing-still run can clear anything.
	if opts.ClearPins && rep.OldAccount == key {
		clear := rep.Redundant()
		if err := ClearPins(clear); err != nil {
			return rep, err
		}
		for _, p := range clear {
			rep.Cleared = append(rep.Cleared, p.Dir)
		}
	}
	return rep, nil
}

// estateRoot resolves the declared estate root: --estate, else the cwd's
// estate — never the cwd itself.
func estateRoot(opts SwitchOptions) (root, source string, err error) {
	if strings.TrimSpace(opts.Estate) != "" {
		abs, err := filepath.Abs(opts.Estate)
		if err != nil {
			return "", "", fmt.Errorf("resolve --estate: %w", err)
		}
		if key, r := claudeacct.Estate(abs); key != "" {
			return r, "--estate", nil
		}
		return "", "", noEstateError(abs, true)
	}
	if strings.TrimSpace(opts.Cwd) != "" {
		if r := claudeacct.Resolve(opts.Cwd); r.EstateRoot != "" {
			return r.EstateRoot, "binding", nil
		}
	}
	return "", "", noEstateError(opts.Cwd, false)
}

func noEstateError(dir string, pointed bool) error {
	where := "no directory at or above " + dir + " declares an estate"
	if pointed {
		where = dir + "/" + claudeacct.BindingFile + " declares no estate"
	}
	return fmt.Errorf("%w: %s — switch moves a whole estate and will not guess one.\n"+
		"  declare the estate at its root: {\"swarmery\":{\"claudeAccount\":\"<key>\",\"estate\":\"<name>\"}} in <root>/%s\n"+
		"  or, for this one directory only: swarmery account use <key> --path <dir>",
		ErrNoEstate, where, claudeacct.BindingFile)
}

// headroom reads the target's headroom from the database, without migrating
// it, into rep. A missing or unreadable database is UNKNOWN — never a crash,
// never a pass.
func headroom(opts SwitchOptions, key string, rep *SwitchReport) {
	maxAge, polling := HeadroomMaxAge(os.Getenv(quotaIntervalEnv))
	if opts.MaxAge != 0 {
		maxAge = opts.MaxAge
	}
	rep.HeadroomMaxAge, rep.PollingOff = maxAge, !polling
	path := opts.DBPath
	if path == "" {
		p, err := store.DefaultDBPath()
		if err != nil {
			return
		}
		path = p
	}
	db, err := store.OpenNoMigrate(path)
	if err != nil {
		return
	}
	defer db.Close()
	rep.Headroom, rep.HeadroomKnown = quota.Headroom(db, key, rep.Now, maxAge)
	rep.LastReading, rep.HasReading = quota.LastReading(db, key)
}

// headroomState words an unknown headroom: stale (with the reading's age) or
// never read.
func (r SwitchReport) headroomState() string {
	if r.HasReading {
		return fmt.Sprintf("stale (last reading %s ago)", roundAge(r.Now.Sub(r.LastReading)))
	}
	return "unknown (no reading)"
}

// pollerNote says where a fresh reading would come from — or that none will.
func (r SwitchReport) pollerNote() string {
	if r.PollingOff {
		return "the daemon's quota poller is disabled (" + quotaIntervalEnv + " is off), so no new reading will arrive"
	}
	return fmt.Sprintf("a reading counts for %s (3x the quota poll interval); the daemon's quota poller records it",
		r.HeadroomMaxAge)
}

// AccountConfigDir resolves an account key to its config dir: where Discover
// finds it (installed=true), else the canonical location (installed=false).
func AccountConfigDir(key string) (dir string, installed bool) {
	for _, a := range claudeacct.Discover() {
		if a.Key == key {
			return a.ConfigDir, true
		}
	}
	canonical, err := claudeacct.ConfigDirFor(key)
	if err != nil {
		return "", false
	}
	if key == ingest.DefaultAccount {
		if fi, err := os.Stat(canonical); err == nil && fi.IsDir() {
			return canonical, true
		}
	}
	return canonical, false
}

// Lines renders the report for stdout. Keys are lowercase so no line can ever
// read as NAME=value; the credential line is a count and a store path only.
func (r SwitchReport) Lines() []string {
	var out []string
	out = append(out,
		fmt.Sprintf("estate:      %s (source: %s)", r.EstateRoot, r.EstateSource),
		fmt.Sprintf("estate key:  %s", r.EstateKey),
		fmt.Sprintf("account:     %s -> %s", r.OldAccount, r.NewAccount),
		fmt.Sprintf("config dir:  %s", r.ConfigDir))
	if r.HeadroomKnown {
		out = append(out, fmt.Sprintf("headroom:    %.0f%% left in %s, read %s ago",
			r.Headroom.PercentLeft, windowName(r.Headroom), roundAge(r.Now.Sub(r.Headroom.FetchedAt))))
	} else {
		line := "headroom:    " + r.headroomState()
		if r.PollingOff {
			line += " — quota polling is disabled"
		}
		out = append(out, line)
	}
	switch r.StoreState {
	case claudeacct.StorePresent:
		out = append(out, fmt.Sprintf("credentials: %d from %s", r.Credentials, r.CredentialStore))
	case claudeacct.StoreRefused:
		out = append(out, fmt.Sprintf("credentials: 0 (store refused: %s)", r.StoreReason))
	default:
		out = append(out, "credentials: 0 (no store file — not an error)")
	}
	red, dis := r.Redundant(), r.Disagreeing()
	if len(r.Pins) == 0 {
		out = append(out, "pins:        none")
	} else {
		out = append(out, fmt.Sprintf("pins:        %d redundant, %d disagreeing: %s",
			len(red), len(dis), strings.Join(pinDirs(dis), ", ")))
		for _, p := range dis {
			out = append(out, fmt.Sprintf("  keeps its own payer after the switch: %s (pinned %s)", p.Dir, p.Account))
		}
		for _, p := range red {
			out = append(out, fmt.Sprintf("  redundant (pins %s, same as the estate): %s", p.Account, p.Dir))
		}
	}
	for _, u := range r.Untrusted {
		out = append(out, "  skipped (untrusted binding file): "+u)
	}
	sweep := fmt.Sprintf("swarmery account switch %s --estate %s --clear-pins", r.OldAccount, r.EstateRoot)
	switch {
	case r.ClearAsked && r.OldAccount != r.NewAccount:
		out = append(out, "cleared:     0 cleared — this run changes the payer, so no pin is redundant both before and after it;",
			"             to sweep redundant pins, stand still: "+sweep)
	case r.ClearAsked && r.DryRun:
		out = append(out, fmt.Sprintf("cleared:     0 cleared (dry run; would clear %d)", len(red)))
	case r.ClearAsked:
		out = append(out, fmt.Sprintf("cleared:     %d cleared", len(r.Cleared)))
		for _, d := range r.Cleared {
			out = append(out, "  cleared pin: "+d)
		}
	case len(red) > 0:
		out = append(out, "cleared:     none — to clear the redundant pins: "+sweep)
	}
	switch {
	case r.DryRun:
		out = append(out, "dry run:     nothing written")
	case r.Wrote:
		out = append(out, fmt.Sprintf("result:      bound %s -> %s", r.EstateRoot, r.NewAccount))
	default:
		out = append(out, "result:      already bound — binding unchanged")
	}
	return out
}

func windowName(h quota.H) string {
	if h.Label != "" {
		return h.Label
	}
	return h.Window
}

func roundAge(d time.Duration) time.Duration {
	if d < 0 {
		return 0
	}
	return d.Round(time.Second)
}

func pinDirs(pins []Pin) []string {
	out := make([]string, 0, len(pins))
	for _, p := range pins {
		out = append(out, p.Dir)
	}
	return out
}
