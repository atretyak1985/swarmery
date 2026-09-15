# Fidelity checklist

The precision rules behind `design-build`. Seven of them; each states the rule, the
consequence of breaking it, and what it looks like in practice. `design-build` and
`design-verify` cite these by number.

---

## 1. Exact values, no rounding

If a value from the inventory has no equivalent on the project's default utility scale, use an
arbitrary value or add a token. Never snap to "the nearest step of the scale".

**Consequence:** rounding is design loss, and it accumulates. One value nudged from 13px to
12px is invisible; six of them in a column move everything below by a dozen pixels, and the
Phase 6 diff then reports a large region whose real cause is spread across six unrelated
declarations.

**Example:** the render says `font-size: 13px`, `gap: 18px`. Write `text-[13px]`, `gap-[18px]`
— not `text-sm`, `gap-4`, and not "close enough, the scale has no 13".

---

## 2. Shared values belong in the token layer

A value that appears in more than one place goes into `design.tokensFile` and is referenced by
name. A value used once, in one component, can stay local.

**Consequence:** the same literal in three files means the next change lands in two of them.
The third one drifts, nobody notices, and the screen it belongs to slowly stops matching the
design system.

**Example:** a surface colour used by a card, a modal and a table header becomes one token
referenced three times. A one-off inset on a single decorative element stays where it is.

---

## 3. Never substitute a font silently — the highest-severity rule here

A required family that is not in the project is a **stop**: report the required weights and
styles plus the concrete instruction for the configured `design.fontLoader`. Do not fall back
to another family, not as a placeholder, not temporarily, not "just to see the layout".

**Consequence:** this rule is first in importance even though it is third in the list. A
substituted font changes text metrics — advance width, x-height, line box — so the Phase 6 diff
lights up on *every* text block. The one real cause is then hidden among a hundred small
regions, and the next hours go into chasing paddings that were never wrong.

**Example:** `fontFamiliesRequired` lists a display family in 500 and 700. The project has
neither. Correct action: stop, name both weights, and give the loader-specific instruction.
Incorrect action: render with whatever the system falls back to and "fix the spacing later".

---

## 4. Reuse before create

A component that already fits is used as it is. A component that fits except for a state or a
modifier gets a new variant. A new component is built only when nothing comparable exists.

**Consequence:** a parallel component built next to an existing one duplicates its behaviour
without its fixes — accessibility, keyboard handling, edge-case states — and doubles the
maintenance surface for the sake of one screen.

**Example:** the design's list row differs from the existing one only by a compact height.
That is a `variant`, not a `build`.

---

## 5. The authoring breakpoint is exact; the others follow the design's logic

The viewport comes from `authoredBreakpoints` in `tokens.json`; it is never guessed. That
breakpoint is matched exactly. Other breakpoints are derived from the design's own responsive
logic and the project's conventions, and what was derived rather than measured is stated.

**Consequence:** measuring at a viewport the design was not authored at produces a diff full of
real-looking regions that describe nothing but the wrong measurement.

**Example:** `authoredBreakpoints` reports a desktop authoring width. Verify pixel-exactly
there; treat the narrow layout as derived work and say so in the Phase 4 plan.

---

## 6. States are derived from the project's system, not invented

A design usually shows the resting state only. Hover, focus, active and disabled come from the
project's existing interaction system, and the derivation is written into the Phase 4 plan as
an explicit item so the operator can correct it before any code exists.

**Consequence:** invented states are the part of the work nobody reviews — the diff cannot see
them, since the design has no reference for them — so they ship as unreviewed design decisions.
Focus states invented ad hoc are also where accessibility regressions enter.

**Example:** the design shows a resting button. The plan says: hover/focus/active/disabled
follow the existing button component's system; nothing new is introduced.

---

## 7. Close the diff in order: geometry, then type, then colour

Fix in this order: container sizes and spacing → typography → colours and shadows.

**Consequence:** the reverse order is an endless loop. Geometry moves everything below it, so
every colour and shadow already "fixed" above moves out of place again and the same regions
reappear run after run with slightly different numbers.

**Example:** `report.json` shows a dominant region at a card and several small colour regions
under it. Fix the card's padding first, re-run, and re-read the region list — most of the small
ones are usually gone.
