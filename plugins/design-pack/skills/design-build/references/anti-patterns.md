# Anti-patterns

Five named failure modes of design implementation. Each one is written as
**what it is → why it is tempting → what it breaks → what to do instead**, because every one of
them looks locally reasonable at the moment it is chosen. Naming them is what makes them
refusable later.

---

## 1. Paste the exported HTML in as a component

**What it is.** The design export renders correctly, so it is dropped into the project as a
component (or a page) more or less as it came out of the design tool.

**Why it is tempting.** It is instantly pixel-perfect. The diff goes green on the first run and
the phase looks finished in minutes.

**What it breaks.** The token system: the screen's values are literals that no token change
will ever reach. Styles are duplicated in a shape the project's conventions do not cover, so
lint and review have nothing to say about them. The result is an orphan screen living outside
the stack — every later change to it is a special case, and the design system silently stops
being the source of truth for that route.

**What to do instead.** Re-express the design in the project's stack: its components, its
tokens, its conventions. The export is a measurement reference, and stays one — it is the thing
compared against in Phase 6, never a source file.

---

## 2. "Roughly the same colour / roughly the same spacing"

**What it is.** A value from the inventory has no exact equivalent in the project, so the
nearest existing token or scale step is used.

**Why it is tempting.** It keeps the token file clean, avoids a decision, and each individual
substitution really is imperceptible.

**What it breaks.** The error accumulates. Six roughly-right values in one column produce a
visible shift; the diff stays at a couple of percent, which reads as noise rather than as a
defect. A diff that is never zero and never clearly wrong is a diff nobody reads — and the pack
loses the only signal it has.

**What to do instead.** Exact values, arbitrary values where the scale has no step, a new token
where the value repeats. See `fidelity-checklist.md`, rules 1 and 2.

---

## 3. Rebuild from a screenshot when an HTML export exists

**What it is.** A screenshot of the design is at hand, so implementation starts from it —
even though the export could be obtained.

**Why it is tempting.** It saves exactly one action: asking for `Project HTML` and waiting.

**What it breaks.** Ground truth, deliberately. Exact type sizes and tracking are not
recoverable from an image, colours are not recoverable after compression, and states are not
in the picture at all. Everything downstream — the token inventory, the recon, the diff — then
measures against an approximation while reporting as if it were exact.

**What to do instead.** Ask for the export; `design-acquire` has the route. If the operator
insists on continuing without it, record `degraded: screenshots`, and never state a pixel match
anywhere in the run.

---

## 4. Quietly adjust global tokens to make the number better

**What it is.** A global token is nudged — a spacing step, a shade, a radius — because it makes
this screen's diff drop.

**Why it is tempting.** It is the cheapest possible fix: one line, and the number improves
immediately.

**What it breaks.** Every other screen using that token, without the operator knowing it
happened. It is the single most expensive edit in this workflow by consequence-per-line, and
the damage surfaces days later on routes nobody connected to this task. This is exactly why
Phase 4 computes blast radius by grepping `design.routesRoot` and `design.componentsRoot`, and
why the agent is required to stop at the plan.

**What to do instead.** If a global token genuinely needs to change, that is an operator
decision made at the Phase 4 gate, with the affected file list on the table. Otherwise: a local
value, or a new token, and the screens that already exist stay as they are.

---

## 5. Declare the work finished without a diff

**What it is.** The screen is reported as done on the strength of looking at it — no
`screenshot-diff.mjs` run, no `report.json`, no `side-by-side.png`.

**Why it is tempting.** It genuinely looks the same, the remaining steps feel like ceremony,
and running them costs a dev server and a render.

**What it breaks.** The entire premise of the pack. "Looks the same" is the judgement this
workflow exists to replace, and it is systematically wrong about exactly the drift that matters
— a 2px shift is invisible to the eye and unmistakable in the region list. Without the
artefacts there is also nothing for the operator to check the claim against.

**What to do instead.** Run `design-verify` to the end and report the number and the image. And
read `regions` even when `pass` is `true` — a passing global percentage is not the same as a
matching screen.
