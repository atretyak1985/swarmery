// Package acctops holds the decisions behind `swarmery account switch` and
// `swarmery account move-session`. cmd/swarmery is excluded from the coverage
// gate, so every rule lives here and cmd/swarmery only parses and prints.
//
// Nothing in this package opens a socket: both commands must work with the
// daemon stopped. The database, when read, is opened through
// store.OpenNoMigrate — a terminal command never migrates behind the daemon.
package acctops

import (
	"fmt"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
)

// Pin is one descendant binding under an estate root, classified against the
// estate root's own account.
//
// Redundant: the pin names the same account the estate root does — it says
// nothing the estate does not already say, and clearing it is
// resolution-neutral. Otherwise it DISAGREES: a deliberate override, which no
// command here ever clears.
type Pin struct {
	Dir       string
	Account   string
	Redundant bool
}

// classifyPins lists the pins under root that shadow it — through
// claudeacct.ScanPins, the ONE downward walk (its depth and skip list are
// claudeacct.PinScanMaxDepth / PinScanSkipDirs; nothing here re-derives them) —
// each classified against estateAccount. untrusted are the descendant binding
// files that walk had to skip, by path and reason only.
func classifyPins(root, estateAccount string) (pins []Pin, untrusted []string) {
	dirs, untrusted := claudeacct.ScanPins(root)
	for _, d := range dirs {
		held := claudeacct.Binding(d)
		pins = append(pins, Pin{Dir: d, Account: held, Redundant: held == estateAccount})
	}
	return pins, untrusted
}

// ClearPins clears every pin in pins through claudeacct.SetBinding(dir, "").
//
// It refuses — returning an error naming the directory, having written NOTHING
// at all — when handed any pin whose Redundant is false: a disagreeing pin is a
// deliberate override, and no caller may obtain a blanket clear by passing a
// hand-built list. A write failure part-way stops there and names the pin; the
// ones before it are cleared.
func ClearPins(pins []Pin) error {
	for _, p := range pins {
		if !p.Redundant {
			return fmt.Errorf("refusing to clear %s: its pin (%s) disagrees with the estate — "+
				"a deliberate override is never cleared; nothing was written", p.Dir, p.Account)
		}
	}
	for _, p := range pins {
		if err := claudeacct.SetBinding(p.Dir, ""); err != nil {
			return fmt.Errorf("clear pin %s: %w", p.Dir, err)
		}
	}
	return nil
}
