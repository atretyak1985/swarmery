# Components — markup for `templates/shell.html`

Contents: 1 Page skeleton · 2 Text and terms · 3 Drawer content · 4 Lists and cards ·
5 Charts · 6 Interactive groups · 7 SVG diagrams · 8 Coordinates discipline

Everything below is styled and wired by the shell. Use these class names and attributes as-is;
change look through the token block only.

## 1. Page skeleton

```html
<section class="sec hero" id="top" data-name="Overview">
  <div class="eyebrow"><b>Subject</b><span>what this is · 2026-10-01</span></div>
  <h1>The thesis in one line.</h1>
  <p class="lede">Plain summary …</p>
  <div class="facts">
    <div class="fact"><div class="big">0<small>changes to X</small></div><p>Why it matters.</p></div>
    <!-- 3 facts; use only numbers the page is about -->
  </div>
  <div class="howto"><span class="tag">How to read</span><span>Dotted words explain themselves on hover. Diagram blocks and "Details" buttons open the side panel. ← → move between sections.</span></div>
</section>

<section class="sec" id="today" data-name="How it works today">
  <div class="eyebrow"><b>02</b><span>Today</span></div>
  <h2>Claim as a heading</h2>
  <p class="lede">Plain explanation.</p>
  <!-- figure, then h3 sub-blocks -->
</section>
```

`data-name` feeds the rail, the phone chip-nav and the top bar. Section ids must not collide with
any other id (SVGs included).

## 2. Text and terms

```html
<span data-t="wal">WAL</span>                      <!-- glossary tooltip; key must exist in #glossary -->
<button class="more" data-d="db">Details</button>   <!-- opens drawer d-db -->
<span class="chip good">✓ allowed</span>  <span class="chip bad">✕ blocked</span>  <span class="chip warn">⚠ sensitive</span>
<span class="chip accent">recommended</span>  <span class="chip c1">L1</span>  <span class="chip mono">P2</span>
<p class="lede-sm">Secondary paragraph.</p>  <span class="hint">Click a block</span>
```

Glossary island (one per page): `{"wal": ["WAL", "Write-ahead log: changes go to a side file first, so reads continue during writes."]}`

## 3. Drawer content

```html
<template id="d-db" data-k="Kicker · area" data-title="Human title">
  <p>One paragraph of context.</p>
  <dl><dt>Engine</dt><dd>…</dd><dt>Limit</dt><dd>…</dd></dl>
  <h4>Sub-heading</h4><ul><li>…</li></ul>
  <pre>config or payload</pre>
  <p class="note">A caveat.</p>
  <div class="src">path/file.go:120-144 · source doc §3</div>
</template>
```

Generated items (rows of a table, risk items) can use the `#details` island instead:
`{"r-3": {"k": "Risk · P2", "t": "Orphaned requests", "html": "<p>…<\/p>"}}` — write `<\/` for `</`.
Any element with `data-d` opens it: buttons, `<tr>`, SVG `<g>`, `.card`.

## 4. Lists and cards

```html
<div class="rows cols2"><div><span class="k">Key</span><span class="v">Value sentence.</span></div>…</div>
<div class="theses"><a class="thesis" href="#sync"><span class="n">01</span><span><b>Title</b><span class="ds">Sentence.</span></span></a>…</div>
<div class="tiles"><button class="tile" data-d="a1"><span class="n">1</span><b>Title</b><span>One line.</span></button>…</div>   <!-- 4 or 8 items -->
<div class="statrow"><div><div class="stat">0 of 209</div><div class="stat-sub">…</div></div><ul class="alerts"><li><span>…</span></li></ul></div>
<div class="cards c2"><button class="card" data-d="p0"><span class="top"><span class="id">P0</span><span class="tt">Spike</span><span class="aside">1–2 weeks</span></span><p>What.</p><span class="gate"><span class="label">gate</span>Measured criterion</span></button></div>
<div class="xlist"><div class="xi"><span><b>Credentials</b><span class="ds">Why never.</span></span></div></div>
<div class="pair"><div class="half"><h4>Layer A</h4><div class="q">which question it answers</div><p>…</p></div><div class="op">+</div><div class="half">…</div></div>
<div class="flow"><span class="st">Edit</span><span class="ar">→</span><span class="st hl">commit</span>…</div>
<div class="blocks"><span class="blk"><b>seq 18340</b>prev …a41c</span><span class="ar">→</span><span class="blk bad"><b>fork</b>→ quarantine</span></div>
<div class="hops"><button class="hop" data-d="x"><b>Browser</b><span>sub</span></button><span class="arr">→</span><button class="hop hl" …>…</button></div>
<div class="tree"><div class="ln ka"><span class="p">~/.app/</span><span class="c">What it is.</span></div><div class="ln k1"><span class="p">├── data/</span><span class="c">…</span></div></div>
<ol class="steps"><li><span><b>Step.</b> Text.</span></li></ol>        <!-- .steps.two for 2 columns -->
<ul class="checks"><li><span>Criterion.</span></li></ul>
<div class="meters"><div class="meter-row"><div class="what"><b>Name</b><code>path</code></div><div><div class="meter" data-v="3"><i></i><i></i><i></i></div><div class="meter-lbl">high</div></div><div class="how">Why.</div></div></div>
```

Tables: wrap in `<div class="tbl-wrap">`; `td.num` for numbers; a `<tr data-d="…">` row opens the
drawer. Use `.yes` / `.no` / `.dash` for boolean cells.

## 5. Charts (HTML, drawn to scale)

Single-series score bars — the `--max` custom property sets the scale:
```html
<div class="bars" style="--max:5">
  <button class="bar best" data-d="ov-a"><span class="nm">Option A</span><span class="track"><i class="f"></i><i class="f"></i><i class="f"></i><i class="f"></i><i class="f"></i></span><span class="sc"><b>5</b><span class="chip accent">recommended</span></span></button>
</div>
```
100 % stacked bar — `flex` is the real value; label inside only when it fits, else leave it to
the legend and `title`:
```html
<div class="sbar-legend"><span><i class="sw s3"></i>own state (c)</span>…</div>
<div class="sbar-row"><div class="lbl">By tables<small>72 tables</small></div>
  <div class="sbar" role="img" aria-label="…"><div class="s3" style="flex:33" title="Own state: 33">33</div><div class="s-ink" style="flex:20">20</div><div class="s-bad" style="flex:6">6</div><div class="s-mute" style="flex:1"></div></div></div>
```
Threshold scale — flex values in real units; axis labels at their true percentage:
```html
<div class="scale-bar"><div class="g" style="flex:30">online</div><div class="w" style="flex:90">stale</div><div class="x" style="flex:60">offline</div></div>
<div class="scale-axis"><span style="left:0">0 s</span><span style="left:16.67%">30 s</span><span style="left:66.67%">120 s</span><span class="end" style="left:100%">since last heartbeat →</span></div>
```
Matrix (rows × columns), items are buttons:
```html
<div class="matrix" style="--cols:2"><div class="hd">probability ↓</div><div class="hd">impact: medium</div><div class="hd">impact: high</div>
  <div class="rh">High</div><div class="cell s2">…</div><div class="cell s3"><button class="mx" data-d="r-0">Snapshots carry secrets<span class="tag">P3</span></button></div>
</div>
```
For anything beyond these (lines over time, distributions) draw SVG with one scale for marks,
ticks and labels, and run the data-viz skill's palette validator if you have one.

## 6. Interactive groups

**Tabs with SVG lanes** — clicking a lane or a tab switches the panel:
```html
<div data-tabs>
  <figure><div class="fig"><svg class="d w-lg" viewBox="0 0 980 400" role="img" aria-label="…">
    <g data-tab-trigger="l1" aria-label="Layer 1"><rect class="lane-bg lw1" x="10" y="40" width="960" height="104" rx="14"/>…</g>
  </svg></div></figure>
  <div class="tabs"><button class="tab" data-tab="l1"><span class="sw" style="background:var(--c1)"></span>L1</button>…</div>
  <div class="panel" data-panel="l1"><div class="panel-grid"><div><h4>What</h4><p>…</p></div>…</div></div>
</div>
```
**Step-through sequence** — index 0 of the steps array is the overview:
```html
<div data-stepper>
  <figure><div class="fig"><svg class="d w-lg" viewBox="0 0 980 540" role="img" aria-label="…">
    <g data-step="1"><rect class="hitbox" x="90" y="80" width="200" height="30"/><line class="e" x1="92" y1="110" x2="286" y2="110" marker-end="url(#ah)"/><text class="tl" x="190" y="102" text-anchor="middle">1 · request</text></g>
  </svg></div></figure>
  <div class="step-ctl"><button class="btn" data-step-prev>← Back</button><button class="btn on" data-step-next>Next →</button><button class="btn" data-step-all>Show all</button><span data-step-count></span></div>
  <div class="step-panel" data-step-panel aria-live="polite"></div>
  <script type="application/json" data-steps>[{"t":"Nine steps","p":"Plain overview.","x":"Legend."},{"t":"Step 1","p":"Plain.","x":"<code>POST /x</code> · file.go:12"}]</script>
</div>
```
**Scenario switcher** — one diagram, states per scenario:
```html
<div data-scenarios>
  <div class="sc-btns"><button class="btn" data-sc="ok">All up</button><button class="btn" data-sc="hub">Hub down</button></div>
  <div class="fig"><svg class="d w-md" viewBox="0 0 980 340" role="img" aria-label="…">
    <line class="e-a" data-el="e-hub-a" …/>
    <g data-el="hub"><rect class="accent" …/><text …>hub</text><g class="x"><line …/><line …/></g></g>
  </svg></div>
  <div class="sc-out"><div class="o-bad"><h4>Stops</h4><p data-sc-out="stop"></p></div><div class="o-good"><h4>Keeps working</h4><p data-sc-out="go"></p></div><div class="o-info"><h4>Recovery</h4><p data-sc-out="fix"></p></div></div>
  <script type="application/json" data-scenario-map>{"ok":{"down":[],"off":[],"out":{"stop":"Nothing.","go":"…","fix":"—"}},"hub":{"down":["hub"],"off":["e-hub-a"],"out":{…}}}</script>
</div>
```
**Filter** — `<div data-filter>` with `<button class="btn" data-f="all">All</button><button class="btn" data-f="P0">P0</button>` and items carrying `data-tags="P0 P3"`.

## 7. SVG diagrams

Vocabulary (classes inside `<svg class="d">`): boxes `box` / `box2` / `accent` (what the page is
about) / `bad`; frames `frame` (dashed boundary) / `frame-s` (filled group); edges `e` (plain),
`e-a` (accent, dashed), `e-r` (danger, dashed), `e-1..e-3` (series), `dotted`, `life` (sequence
lifeline); text `tb` (bold), `ts` (mono small), `tl` (label), `tl-b`, `td` (danger), `ta` (accent);
markers `url(#ah)`, `#ah-a`, `#ah-r`, `#ah-1..3`. Clickable group: `<g class="hot" data-d="key"
aria-label="…">`. Thin lines that must be clickable get a twin `<line class="hit" …>` underneath.

Recipes:
- **Flow inside one host** — frame for the host, a filled group for the process, boxes left to
  right in reading order, arrows labelled with the verb (`writes`, `polls 2 s`), a red label under
  the dangerous box. ~980×380.
- **Mesh of nodes** — diamond layout (top, left, right, bottom) so the two diagonals cross in an
  empty centre; each node box holds 2×2 component boxes; tunnels as `e-a` lines between frames.
- **Zones of trust** — nested rectangles (outer → inner), each with its label top-left and its
  ports as pill boxes; the riskiest inner pill uses `bad`.
- **Lanes** — one row per track, source box left, mechanism text over a coloured arrow, target box
  right; the property line (latency, conflicts) under the arrow.
- **Sequence** — 4–5 lifelines 200 px apart, header boxes at the top, one step per 50 px, label above
  each arrow prefixed with its number; zone labels (`loopback`, `overlay`) between lifelines.
- **DAG of phases** — boxes 150×64, columns 190–200 px apart, parallel branches stacked; a small
  label in the gap where branches may run in parallel.

## 8. Coordinates discipline

- Choose `viewBox` for the content; wide flows ~980 wide; set `class="w-lg"` (min-width 760) or
  `w-md` so phones scroll the figure instead of shrinking text to nothing.
- Estimate label width before placing: `chars × font-size × 0.6` (proportional fonts),
  `× 0.62` for monospace. A label that does not fit its box or gap gets shorter words, not a
  smaller font.
- Put text on a grid: shared baselines (box top + 23 for the title, + 42 for the sub-line in a
  54-high box), equal gaps between boxes.
- Draw order is z-order: draw a line **after** the box it must point into, or its arrowhead hides
  under the box; draw it **before** a box it must pass behind.
- Arrow end coordinates stop 2–3 px short of the target edge; `refX="9"` puts the tip at the end.
- Every `<svg>` that carries meaning: `role="img"` and an `aria-label` that states the claim; wrap
  it in `<figure>` with a `<figcaption>` that says what to notice.
