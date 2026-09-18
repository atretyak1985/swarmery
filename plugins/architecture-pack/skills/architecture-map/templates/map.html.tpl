<!doctype html>
<html lang="en" data-theme="dark">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Architecture Map</title>
<style>
:root{
  --bg:#0d1017; --panel:#131722; --card:#1a1f2e; --card-on:#242c42;
  --line:#2a3145; --ink:#e6e9f2; --ink-dim:#9aa3b8; --ink-faint:#6b7386;
  --accent:#f5c451; --accent-ink:#1a1503; --edge:#39415a; --flow:#f5c451;
  --chip:#222941; --mono:ui-monospace,'JetBrains Mono',Menlo,monospace;
  --sans:'Inter',system-ui,-apple-system,sans-serif;
}
html[data-theme="light"]{
  --bg:#f6f7fa; --panel:#ffffff; --card:#ffffff; --card-on:#fff7e0;
  --line:#d9dde8; --ink:#171b26; --ink-dim:#555d70; --ink-faint:#8a91a3;
  --accent:#b8860b; --accent-ink:#fff; --edge:#c3c9d8; --flow:#b8860b; --chip:#eceef5;
}
*{box-sizing:border-box} body{margin:0;height:100vh;display:flex;flex-direction:column;overflow:hidden;background:var(--bg);color:var(--ink);font-family:var(--sans);font-size:14px}
header{flex:none;display:flex;justify-content:space-between;gap:16px;padding:20px 24px;border-bottom:1px solid var(--line)}
h1{margin:0 0 4px;font-size:20px} #desc{margin:0 0 8px;color:var(--ink-dim);max-width:70ch}
.chips{display:flex;flex-wrap:wrap;gap:6px;margin-bottom:6px}
.chip{background:var(--chip);border:1px solid var(--line);border-radius:20px;padding:2px 10px;font:11px var(--mono)}
.meta{font:11px var(--mono);color:var(--ink-faint)}
.head-right{display:flex;gap:8px;align-items:flex-start}
#search{background:var(--panel);border:1px solid var(--line);border-radius:8px;color:var(--ink);padding:7px 10px;font:12px var(--mono);width:220px;outline:none}
#theme{background:var(--panel);border:1px solid var(--line);border-radius:8px;color:var(--ink);padding:6px 10px;cursor:pointer}
#tabs{flex:none;display:flex;flex-wrap:wrap;gap:6px;padding:10px 24px;border-bottom:1px solid var(--line)}
.tab{background:none;border:1px solid var(--line);border-radius:20px;color:var(--ink-dim);padding:4px 14px;font:600 11px var(--mono);letter-spacing:.06em;cursor:pointer;transition:color .15s,border-color .15s,background .15s}
.tab:hover{color:var(--ink)}
.tab.on{background:var(--chip);border-color:var(--flow);color:var(--ink)}
#layout{display:none;flex:1;min-height:0}
#layout.on{display:flex}
#boardwrap{flex:1;overflow:auto;padding:20px}
/* Zoom. #board is scaled with a CSS transform (origin top-left); a transform
   does not change layout, so #zoomwrap is sized to the scaled board by script
   and is what #boardwrap actually scrolls. width:max-content (not min-width):
   the board must keep its natural size whatever the wrapper's width is, or the
   wrapper measuring the board would feed back into the board's own width. */
#zoomwrap{position:relative}
#board{position:relative;display:flex;gap:28px;align-items:flex-start;width:max-content;padding-bottom:40px;transform-origin:0 0}
#zoomctl{display:flex;align-items:center;gap:2px;margin-left:auto}
#zoomctl button{background:none;border:1px solid var(--line);border-radius:8px;color:var(--ink-dim);cursor:pointer;font:600 11px var(--mono);padding:3px 8px;min-width:28px}
#zoomctl button:hover{color:var(--ink);border-color:var(--flow)}
#zoomctl #zoomlvl{min-width:48px;text-align:center}
#edges{position:absolute;inset:0;width:100%;height:100%;pointer-events:none;overflow:visible}
.col{min-width:220px;max-width:250px}
.col h3{margin:0 0 4px;font:600 10.5px var(--mono);letter-spacing:.14em;text-transform:uppercase;color:var(--ink-faint)}
.col p.ldesc{margin:0 0 12px;font-size:11px;color:var(--ink-faint)}
.card{position:relative;background:var(--card);border:1px solid var(--line);border-radius:10px;padding:10px 12px;margin-bottom:12px;cursor:pointer;transition:opacity .15s,border-color .15s,background .15s}
.card b{display:block;font-size:13px}
.card .path{font:10.5px var(--mono);color:var(--ink-faint);word-break:break-all}
.card .resp{margin-top:4px;font-size:11.5px;color:var(--ink-dim)}
.card.on{border-color:var(--flow);background:var(--card-on)}
.card.dim{opacity:.22}
/* Blast highlight, driven by the embedder's highlight message (see the
   message listener at the end of the script). outline, not border: a border
   would resize the card and move every edge the SVG has already routed.
   Stays visible under .dim so a highlighted module is never faded out. */
.card.blast{outline:2px solid var(--accent);outline-offset:2px}
.card.blast.dim{opacity:1}
.edge{stroke:var(--edge);stroke-width:1.2;fill:none}
.edge.dim{opacity:.12}
.fedge{stroke:var(--flow);stroke-width:1.8;fill:none}
.fnum circle{fill:var(--flow)} .fnum text{fill:var(--accent-ink);font:700 9px var(--mono);text-anchor:middle;dominant-baseline:central}
#side{width:320px;border-left:1px solid var(--line);background:var(--panel);overflow-y:auto;padding:16px}
#side h2{margin:0 0 10px;font:600 10.5px var(--mono);letter-spacing:.14em;text-transform:uppercase;color:var(--ink-faint)}
.flow{border:1px solid var(--line);border-radius:10px;padding:9px 11px;margin-bottom:8px;cursor:pointer}
.flow.on{border-color:var(--flow)}
.flow b{display:block;font-size:12.5px}
.flow span{font-size:11px;color:var(--ink-dim)}
#clear{margin:4px 0 14px;background:none;border:1px solid var(--line);border-radius:8px;color:var(--ink-dim);padding:5px 10px;cursor:pointer;font-size:11px}
#steps{list-style:none;margin:0;padding:0;counter-reset:st}
#steps li{position:relative;border:1px solid var(--line);border-radius:10px;padding:9px 11px 9px 34px;margin-bottom:8px;counter-increment:st;font-size:11.5px}
#steps li::before{content:counter(st);position:absolute;left:10px;top:10px;width:16px;height:16px;border-radius:50%;background:var(--flow);color:var(--accent-ink);font:700 9.5px var(--mono);display:flex;align-items:center;justify-content:center}
#steps li b{font:600 11px var(--mono)}
#steps li .file,#steps li .payload{display:block;margin-top:3px;font:10.5px var(--mono);color:var(--ink-faint);word-break:break-all}
#inspector{position:fixed;left:20px;bottom:20px;width:340px;max-height:44vh;overflow-y:auto;background:var(--panel);border:1px solid var(--line);border-radius:12px;padding:14px 16px;box-shadow:0 12px 32px rgba(0,0,0,.35);z-index:10}
#inspector h4{margin:0;font-size:14px} #inspector .path{font:10.5px var(--mono);color:var(--ink-faint)}
#inspector h5{margin:10px 0 4px;font:600 10px var(--mono);letter-spacing:.12em;text-transform:uppercase;color:var(--ink-faint)}
#inspector ul{margin:0;padding-left:16px;font:11px var(--mono)}
#inspector p{margin:8px 0 0;font-size:12px;color:var(--ink-dim)}
#inspector button{position:absolute;top:8px;right:10px;background:none;border:none;color:var(--ink-faint);cursor:pointer;font-size:14px}
#ref{display:none;flex:1;min-height:0;overflow:auto;padding:16px 24px 40px}
#ref.on{display:block}
.refsec{display:none}
.refsec.on{display:block}
.refsec h2{margin:0 0 12px;font:600 11px var(--mono);letter-spacing:.12em;text-transform:uppercase;color:var(--ink-dim)}
#ref table{border-collapse:collapse;font:11.5px var(--mono)}
#ref td,#ref th{border:1px solid var(--line);padding:4px 10px;text-align:left}
#ref th{color:var(--ink-faint);font-weight:600}
#ref ul{margin:0;padding-left:18px;font-size:12px;color:var(--ink-dim)}
#ref li{margin-bottom:4px}
/* Reader tab. Board cards are capped at 250px so the SVG edges route between
   columns; a 400-char responsibility then becomes a tall sliver, and reading
   the whole map means scrolling every column top to bottom. The reader is the
   same layers → modules → flows laid out to be READ: a grid of reading-width
   entries that spends whatever width the frame has (full-screen included),
   and every flow with its steps expanded instead of hidden behind a click. */
.rd-layer{margin:0 0 28px}
.rd-layer .ldesc{margin:-6px 0 12px;font-size:12.5px;line-height:1.5;color:var(--ink-dim);max-width:90ch}
.rd-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(380px,1fr));gap:10px}
.rd-mod{background:var(--card);border:1px solid var(--line);border-radius:10px;padding:11px 14px;cursor:pointer;transition:border-color .15s,background .15s}
.rd-mod:hover{border-color:var(--ink-faint)}
.rd-mod.on{border-color:var(--flow);background:var(--card-on)}
.rd-mod.blast{outline:2px solid var(--accent);outline-offset:2px}
.rd-mod b{display:block;font-size:13.5px}
.rd-mod .path{display:block;font:10.5px var(--mono);color:var(--ink-faint);word-break:break-all}
.rd-mod p{margin:6px 0 0;font-size:12.5px;line-height:1.5;color:var(--ink-dim)}
.rd-flow{border:1px solid var(--line);border-radius:10px;padding:11px 14px;margin-bottom:10px;max-width:110ch}
.rd-flow.on{border-color:var(--flow)}
.rd-flow>b{font-size:13.5px}
.rd-flow>p{margin:4px 0 8px;font-size:12.5px;line-height:1.5;color:var(--ink-dim);max-width:90ch}
.rd-flow ol{margin:0;padding-left:22px;font-size:12px;line-height:1.5;color:var(--ink)}
.rd-flow li{margin-bottom:3px}
.rd-flow li b{font:600 11px var(--mono)}
.rd-flow li .file,.rd-flow li .payload{font:10.5px var(--mono);color:var(--ink-faint);word-break:break-all}
.rd-show{float:right;margin-left:10px;background:none;border:1px solid var(--line);border-radius:8px;color:var(--ink-dim);padding:3px 9px;cursor:pointer;font:11px var(--mono)}
.rd-show:hover{color:var(--ink);border-color:var(--flow)}
</style>
</head>
<body>
<header>
  <div>
    <h1 id="title"></h1>
    <p id="desc"></p>
    <div id="stack" class="chips"></div>
    <div id="meta" class="meta"></div>
  </div>
  <div class="head-right">
    <input id="search" type="search" placeholder="filter modules…" aria-label="filter modules">
    <button id="theme" title="toggle theme">☾</button>
  </div>
</header>
<nav id="tabs" aria-label="map sections">
  <div id="zoomctl" role="group" aria-label="diagram zoom" title="pinch on the trackpad, or ctrl/⌘ + scroll, to zoom the diagram">
    <button type="button" id="zoomout" aria-label="zoom out">−</button>
    <button type="button" id="zoomlvl" title="reset zoom">100%</button>
    <button type="button" id="zoomin" aria-label="zoom in">+</button>
  </div>
</nav>
<div id="layout" class="on">
  <main id="boardwrap">
    <div id="zoomwrap"><div id="board"><svg id="edges" aria-hidden="true"></svg></div></div>
  </main>
  <aside id="side">
    <h2>Flows</h2>
    <div id="flowlist"></div>
    <button id="clear" hidden>Clear selection</button>
    <h2 id="stepsh" hidden>Steps</h2>
    <ol id="steps"></ol>
  </aside>
</div>
<section id="ref"></section>
<div id="inspector" hidden></div>
<script id="map-data" type="application/json">{%%%MAP_JSON%%%}</script>
<script>
'use strict';
const MAP = JSON.parse(document.getElementById('map-data').textContent);
const $ = (id) => document.getElementById(id);
const board = $('board'), svg = $('edges');
const modById = new Map(MAP.modules.map((m) => [m.id, m]));
const cardEls = new Map();
// Reader-tab entries keyed by module id — the same modules as cardEls, so
// search, flow selection and the embedder's blast highlight address both.
const readerEls = new Map();
let activeFlow = null;

// ---- header ----
$('title').textContent = MAP.project.name + ' — Architecture & Flows';
document.title = $('title').textContent;
$('desc').textContent = MAP.project.description;
$('stack').innerHTML = MAP.project.techStack.map((t) => `<span class="chip">${esc(t)}</span>`).join('');
$('meta').textContent = `analyzed ${MAP.analyzedAt} @ ${MAP.analyzedAtCommit.slice(0, 10)} · ${MAP.modules.length} modules · ${MAP.flows.length} flows`;

// ---- board ----
for (const layer of [...MAP.layers].sort((a, b) => a.order - b.order)) {
  const col = document.createElement('div');
  col.className = 'col';
  col.innerHTML = `<h3>${esc(layer.name)}</h3><p class="ldesc">${esc(layer.description || '')}</p>`;
  for (const m of MAP.modules.filter((x) => x.layer === layer.id)) {
    const card = document.createElement('div');
    card.className = 'card';
    card.dataset.id = m.id;
    card.innerHTML = `<b>${esc(m.name)}</b><span class="path">${esc(m.path)}</span><div class="resp">${esc(m.responsibility)}</div>`;
    card.addEventListener('click', (e) => { e.stopPropagation(); inspect(m); });
    col.appendChild(card);
    cardEls.set(m.id, card);
  }
  board.appendChild(col);
}

// ---- flows panel ----
for (const f of MAP.flows) {
  const el = document.createElement('div');
  el.className = 'flow';
  el.dataset.id = f.id;
  el.innerHTML = `<b>${esc(f.name)}</b><span>${esc(f.description)}</span>`;
  el.addEventListener('click', () => selectFlow(activeFlow === f.id ? null : f.id));
  $('flowlist').appendChild(el);
}
$('clear').addEventListener('click', () => selectFlow(null));

function selectFlow(id) {
  activeFlow = id;
  document.querySelectorAll('.flow').forEach((el) => el.classList.toggle('on', el.dataset.id === id));
  $('clear').hidden = $('stepsh').hidden = id === null;
  const steps = $('steps');
  steps.innerHTML = '';
  const flow = MAP.flows.find((f) => f.id === id);
  const involved = new Set();
  if (flow) for (const s of flow.steps) {
    involved.add(s.from); involved.add(s.to);
    const li = document.createElement('li');
    li.innerHTML = `<b>${esc(name(s.from))} → ${esc(name(s.to))}</b><div>${esc(s.action)}</div>` +
      (s.file ? `<span class="file">${esc(s.file)}</span>` : '') +
      (s.payload ? `<span class="payload">⇢ ${esc(s.payload)}</span>` : '');
    steps.appendChild(li);
  }
  for (const [mid, el] of cardEls) {
    el.classList.toggle('on', involved.has(mid));
    el.classList.toggle('dim', flow !== undefined && flow !== null && !involved.has(mid));
  }
  for (const [mid, el] of readerEls) el.classList.toggle('on', involved.has(mid));
  document.querySelectorAll('.rd-flow').forEach((el) => el.classList.toggle('on', el.dataset.id === id));
  draw();
}

// ---- search ----
$('search').addEventListener('input', (e) => {
  const q = e.target.value.trim().toLowerCase();
  const hits = (m) => q === '' || (m.name + ' ' + m.path + ' ' + m.responsibility).toLowerCase().includes(q);
  for (const [mid, el] of cardEls) {
    el.classList.toggle('dim', q !== '' ? !hits(modById.get(mid)) : activeFlow !== null && !el.classList.contains('on'));
  }
  // The reader hides a miss outright: a dimmed entry in a grid is still a hole
  // to scroll past, and the point of that tab is to read without scrolling.
  for (const [mid, el] of readerEls) el.hidden = !hits(modById.get(mid));
  // A layer whose every module missed is just a heading over nothing — hide it
  // too, so a narrow query collapses the reader to only the layers that answer.
  document.querySelectorAll('.rd-layer[data-layer]').forEach((el) => {
    el.hidden = q !== '' && el.querySelector('.rd-mod:not([hidden])') === null;
  });
  draw();
});

// ---- inspector ----
function inspect(m) {
  const box = $('inspector');
  const list = (title, items) => (items && items.length)
    ? `<h5>${title}</h5><ul>${items.map((x) => `<li>${esc(x)}</li>`).join('')}</ul>` : '';
  box.innerHTML = `<button id="insx">✕</button><h4>${esc(m.name)}</h4><span class="path">${esc(m.path)}</span>` +
    `<p>${esc(m.responsibility)}</p>` +
    list('Key files', m.keyFiles) + list('Exports', m.exports) +
    list('Depends on', (m.dependencies || []).map(name));
  box.hidden = false;
  $('insx').addEventListener('click', (e) => { e.stopPropagation(); box.hidden = true; });
}
document.body.addEventListener('click', () => { $('inspector').hidden = true; });

// ---- edges ----
function anchor(el, side) {
  // Both rects are in screen space, i.e. already scaled by the zoom; the SVG
  // sits inside the scaled board, so its coordinates are in unscaled board units.
  const b = el.getBoundingClientRect(), r = board.getBoundingClientRect();
  return { x: ((side === 'l' ? b.left : b.right) - r.left) / zoom, y: (b.top - r.top + b.height / 2) / zoom };
}
function curve(a, b) {
  const dx = Math.max(36, Math.abs(b.x - a.x) / 2);
  return `M ${a.x} ${a.y} C ${a.x + dx} ${a.y}, ${b.x - dx} ${b.y}, ${b.x} ${b.y}`;
}
function link(fromId, toId) {
  const fe = cardEls.get(fromId), te = cardEls.get(toId);
  if (!fe || !te) return null;
  const f = anchor(fe, 'r'), t = anchor(te, 'l');
  if (t.x < f.x) return curve(anchor(fe, 'l'), anchor(te, 'r'));
  return curve(f, t);
}
function draw() {
  sizeZoomWrap();
  svg.setAttribute('width', board.scrollWidth);
  svg.setAttribute('height', board.scrollHeight);
  svg.innerHTML = '';
  const ns = 'http://www.w3.org/2000/svg';
  const flow = MAP.flows.find((f) => f.id === activeFlow);
  for (const m of MAP.modules) for (const d of m.dependencies || []) {
    const dPath = link(m.id, d);
    if (!dPath) continue;
    const p = document.createElementNS(ns, 'path');
    p.setAttribute('d', dPath);
    p.setAttribute('class', 'edge' + (flow ? ' dim' : ''));
    svg.appendChild(p);
  }
  if (!flow) return;
  flow.steps.forEach((s, i) => {
    const dPath = link(s.from, s.to);
    if (!dPath) return;
    const p = document.createElementNS(ns, 'path');
    p.setAttribute('d', dPath);
    p.setAttribute('class', 'fedge');
    svg.appendChild(p);
    const mid = p.getPointAtLength(p.getTotalLength() / 2);
    const g = document.createElementNS(ns, 'g');
    g.setAttribute('class', 'fnum');
    g.innerHTML = `<circle cx="${mid.x}" cy="${mid.y}" r="8"></circle><text x="${mid.x}" y="${mid.y}">${i + 1}</text>`;
    svg.appendChild(g);
  });
}
// ---- zoom ----
// A trackpad pinch reaches the page as a wheel event with ctrlKey set (Chrome,
// Firefox, Edge) or as gesture events (Safari) — both are handled, and while a
// Safari gesture is in progress wheel events are ignored so a browser that
// sends both cannot zoom twice. ⌘/ctrl + scroll works the same way for a mouse.
// preventDefault is what keeps the pinch from zooming the whole page (or, when
// this map is embedded, the page around it). Zoom is applied around the cursor:
// the board point under it stays under it, by shifting the scroll position by
// that point's displacement. Persisted, because the map is regenerated in place
// and the reader's zoom should survive a rebuild.
const ZOOM_MIN = 0.4, ZOOM_MAX = 2.5;
const boardwrap = $('boardwrap'), zoomwrap = $('zoomwrap');
let zoom = 1;
function sizeZoomWrap() {
  // A transform does not change layout: reserve the scaled size explicitly so
  // #boardwrap scrolls exactly the zoomed board — no dead space, no clipping.
  zoomwrap.style.width = zoom === 1 ? '' : board.offsetWidth * zoom + 'px';
  zoomwrap.style.height = zoom === 1 ? '' : board.offsetHeight * zoom + 'px';
}
function setZoom(next, cx, cy) {
  next = Math.min(ZOOM_MAX, Math.max(ZOOM_MIN, next));
  if (next === zoom) return;
  const r = boardwrap.getBoundingClientRect();
  if (cx === undefined) { cx = r.left + r.width / 2; cy = r.top + r.height / 2; }
  // The board point under the focal point, in unscaled board units.
  const z = zoomwrap.getBoundingClientRect();
  const px = (cx - z.left) / zoom, py = (cy - z.top) / zoom;
  const prev = zoom;
  zoom = next;
  board.style.transform = zoom === 1 ? '' : `scale(${zoom})`;
  sizeZoomWrap();
  // Keep that point under the cursor. Vertically the scroller may be the
  // document rather than #boardwrap (an embedder can let the whole map page
  // scroll), so whatever #boardwrap could not absorb goes to the window.
  boardwrap.scrollLeft += px * (zoom - prev);
  const dy = py * (zoom - prev), before = boardwrap.scrollTop;
  boardwrap.scrollTop += dy;
  const rest = dy - (boardwrap.scrollTop - before);
  if (Math.abs(rest) > 0.5) window.scrollBy(0, rest);
  $('zoomlvl').textContent = Math.round(zoom * 100) + '%';
  try { localStorage.setItem('am-zoom', String(zoom)); } catch (_) { /* storage blocked */ }
}
let gesturing = false;
boardwrap.addEventListener('wheel', (e) => {
  if (gesturing || !(e.ctrlKey || e.metaKey)) return;
  e.preventDefault();
  // A pinch arrives as many small deltas (a few px each); a mouse-wheel notch as
  // one ~120px jump. The clamp caps a single event at ~1.5× so a notch steps
  // rather than leaps, while the pinch stays smooth. exp() keeps in/out symmetric.
  const d = Math.max(-40, Math.min(40, e.deltaMode === 1 ? e.deltaY * 20 : e.deltaY));
  setZoom(zoom * Math.exp(-d * 0.01), e.clientX, e.clientY);
}, { passive: false });
let gestureBase = 1;
boardwrap.addEventListener('gesturestart', (e) => { e.preventDefault(); gesturing = true; gestureBase = zoom; });
boardwrap.addEventListener('gesturechange', (e) => { e.preventDefault(); setZoom(gestureBase * e.scale, e.clientX, e.clientY); });
boardwrap.addEventListener('gestureend', (e) => { e.preventDefault(); gesturing = false; });
$('zoomin').addEventListener('click', () => setZoom(zoom * 1.2));
$('zoomout').addEventListener('click', () => setZoom(zoom / 1.2));
$('zoomlvl').addEventListener('click', () => setZoom(1));
try {
  const savedZoom = parseFloat(localStorage.getItem('am-zoom'));
  if (Number.isFinite(savedZoom) && savedZoom > 0) setZoom(savedZoom);
} catch (_) { /* storage blocked */ }

let raf = 0;
window.addEventListener('resize', () => { cancelAnimationFrame(raf); raf = requestAnimationFrame(draw); });
requestAnimationFrame(draw);

// ---- theme ----
const saved = localStorage.getItem('am-theme');
if (saved) document.documentElement.dataset.theme = saved;
syncThemeGlyph();
$('theme').addEventListener('click', () => {
  const next = document.documentElement.dataset.theme === 'dark' ? 'light' : 'dark';
  document.documentElement.dataset.theme = next;
  localStorage.setItem('am-theme', next);
  syncThemeGlyph();
});
function syncThemeGlyph() { $('theme').textContent = document.documentElement.dataset.theme === 'dark' ? '☀' : '☾'; }

// ---- reference tabs (diagram / modules & flows / APIs / database / external services / notes) ----
// Each reference section is a full-height tab panel instead of an accordion
// below the board — no long page scroll; only tabs with content are rendered.
(function renderRef() {
  const sections = [];

  // Modules & flows: the board's content at reading width (see the .rd-* CSS
  // for why the board itself cannot be that). Always rendered — a map with no
  // modules is not a map. Clicking an entry opens the same inspector as a
  // board card; "show on diagram" jumps to the board with that flow selected.
  const layers = [...MAP.layers].sort((a, b) => a.order - b.order);
  const readerHtml = layers.map((layer) => {
    const mods = MAP.modules.filter((m) => m.layer === layer.id);
    return `<div class="rd-layer" data-layer="${esc(layer.id)}"><h2>${esc(layer.name)}</h2><p class="ldesc">${esc(layer.description || '')}</p><div class="rd-grid">` +
      mods.map((m) => `<div class="rd-mod" data-id="${esc(m.id)}"><b>${esc(m.name)}</b><span class="path">${esc(m.path)}</span><p>${esc(m.responsibility)}</p></div>`).join('') +
      '</div></div>';
  }).join('') + (MAP.flows.length
    ? '<div class="rd-layer"><h2>Flows</h2>' + MAP.flows.map((f) =>
        `<div class="rd-flow" data-id="${esc(f.id)}"><button type="button" class="rd-show" data-id="${esc(f.id)}">show on diagram</button><b>${esc(f.name)}</b><p>${esc(f.description)}</p><ol>` +
        f.steps.map((s) => `<li><b>${esc(name(s.from))} → ${esc(name(s.to))}</b> ${esc(s.action)}` +
          (s.file ? ` <span class="file">${esc(s.file)}</span>` : '') +
          (s.payload ? ` <span class="payload">⇢ ${esc(s.payload)}</span>` : '') + '</li>').join('') +
        '</ol></div>').join('') + '</div>'
    : '');
  sections.push({ id: 'reader', label: `Modules & flows (${MAP.modules.length} · ${MAP.flows.length})`, html: readerHtml });

  if ((MAP.apis || []).length) {
    sections.push({ id: 'apis', label: `API endpoints (${MAP.apis.length})`,
      html: `<table><tr><th>Method</th><th>Path</th><th>Handler</th><th>Description</th></tr>` +
        MAP.apis.map((a) => `<tr><td>${esc(a.method)}</td><td>${esc(a.path)}</td><td>${esc(a.handlerFile || '')}</td><td>${esc(a.description || '')}</td></tr>`).join('') + '</table>' });
  }
  const db = MAP.database;
  if (db && (db.engine || (db.tables || []).length)) {
    sections.push({ id: 'db', label: 'Database',
      html: `<h2>${esc(db.engine || 'unknown')}${db.migrationsPath ? ' · ' + esc(db.migrationsPath) : ''}</h2>` +
        `<table><tr><th>Table</th><th>Purpose</th></tr>` +
        (db.tables || []).map((t) => `<tr><td>${esc(t.name)}</td><td>${esc(t.purpose || '')}</td></tr>`).join('') + '</table>' });
  }
  if ((MAP.externalServices || []).length) {
    sections.push({ id: 'ext', label: `External services (${MAP.externalServices.length})`,
      html: `<table><tr><th>Service</th><th>Purpose</th><th>Used by</th></tr>` +
        MAP.externalServices.map((s) => `<tr><td>${esc(s.name)}</td><td>${esc(s.purpose)}</td><td>${esc((s.usedBy || []).map(name).join(', '))}</td></tr>`).join('') + '</table>' });
  }
  const conv = MAP.conventions;
  if (conv && (conv.naming || conv.folderStructure || (conv.patternsUsed || []).length)) {
    sections.push({ id: 'conv', label: 'Conventions',
      html: '<ul>' +
        (conv.naming ? `<li>Naming: ${esc(conv.naming)}</li>` : '') +
        (conv.folderStructure ? `<li>Folders: ${esc(conv.folderStructure)}</li>` : '') +
        (conv.patternsUsed || []).map((x) => `<li>${esc(x)}</li>`).join('') + '</ul>' });
  }
  if ((MAP.importantNotes || []).length) {
    sections.push({ id: 'notes', label: 'Important notes',
      html: '<ul>' + MAP.importantNotes.map((x) => `<li>${esc(x)}</li>`).join('') + '</ul>' });
  }

  const ref = $('ref');
  ref.innerHTML = sections.map((s) => `<section class="refsec" data-tab="${s.id}">${s.html}</section>`).join('');
  ref.querySelectorAll('.rd-mod').forEach((el) => {
    readerEls.set(el.dataset.id, el);
    el.addEventListener('click', (e) => { e.stopPropagation(); inspect(modById.get(el.dataset.id)); });
  });
  ref.querySelectorAll('.rd-show').forEach((btn) => {
    btn.addEventListener('click', () => { selectTab('board'); selectFlow(btn.dataset.id); });
  });

  const tabs = [{ id: 'board', label: 'Diagram' }, ...sections];
  const bar = $('tabs');
  // Inserted before the static #zoomctl rather than replacing the bar's content.
  bar.insertAdjacentHTML('afterbegin', tabs.map((t) => `<button class="tab${t.id === 'board' ? ' on' : ''}" data-tab="${t.id}">${esc(t.label)}</button>`).join(''));
  bar.addEventListener('click', (e) => {
    const btn = e.target.closest('.tab');
    if (btn) selectTab(btn.dataset.tab);
  });

  function selectTab(id) {
    bar.querySelectorAll('.tab').forEach((el) => el.classList.toggle('on', el.dataset.tab === id));
    const onBoard = id === 'board';
    $('layout').classList.toggle('on', onBoard);
    ref.classList.toggle('on', !onBoard);
    ref.querySelectorAll('.refsec').forEach((el) => el.classList.toggle('on', el.dataset.tab === id));
    $('inspector').hidden = true;
    $('zoomctl').hidden = !onBoard;
    // Edges are sized from getBoundingClientRect — zero while the board is
    // display:none, so redraw after it is visible again.
    if (onBoard) requestAnimationFrame(draw);
  }
})();

// ---- embed channel: blast highlight ----
// The dashboard that frames this map posts {type:'swarmery:highlight',
// moduleIds:[...]} to say "these are the modules the current branch touches".
// An empty (or absent) moduleIds clears the highlight, so one message shape
// covers both select and deselect.
//
// Opened standalone (file:// straight from architecture-out/) nothing ever
// posts, so this listener costs nothing and changes nothing. The origin is NOT
// checked on purpose: the payload is a list of module ids that only ever
// toggles a CSS class on cards this document already rendered — an id that
// matches nothing is a no-op, and nothing here is injected as markup.
window.addEventListener('message', (e) => {
  const d = e.data;
  if (!d || d.type !== 'swarmery:highlight') return;
  const wanted = new Set(Array.isArray(d.moduleIds) ? d.moduleIds : []);
  for (const [mid, el] of cardEls) el.classList.toggle('blast', wanted.has(mid));
  for (const [mid, el] of readerEls) el.classList.toggle('blast', wanted.has(mid));
});

function name(id) { const m = modById.get(id); return m ? m.name : id; }
function esc(s) { return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c])); }
</script>
</body>
</html>
