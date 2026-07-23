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
*{box-sizing:border-box} body{margin:0;background:var(--bg);color:var(--ink);font-family:var(--sans);font-size:14px}
header{display:flex;justify-content:space-between;gap:16px;padding:20px 24px;border-bottom:1px solid var(--line)}
h1{margin:0 0 4px;font-size:20px} #desc{margin:0 0 8px;color:var(--ink-dim);max-width:70ch}
.chips{display:flex;flex-wrap:wrap;gap:6px;margin-bottom:6px}
.chip{background:var(--chip);border:1px solid var(--line);border-radius:20px;padding:2px 10px;font:11px var(--mono)}
.meta{font:11px var(--mono);color:var(--ink-faint)}
.head-right{display:flex;gap:8px;align-items:flex-start}
#search{background:var(--panel);border:1px solid var(--line);border-radius:8px;color:var(--ink);padding:7px 10px;font:12px var(--mono);width:220px;outline:none}
#theme{background:var(--panel);border:1px solid var(--line);border-radius:8px;color:var(--ink);padding:6px 10px;cursor:pointer}
#layout{display:flex;height:calc(100vh - 118px)}
#boardwrap{flex:1;overflow:auto;padding:20px}
#board{position:relative;display:flex;gap:28px;align-items:flex-start;min-width:max-content;padding-bottom:40px}
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
#ref{padding:8px 24px 40px;border-top:1px solid var(--line)}
#ref details{margin-top:10px;border:1px solid var(--line);border-radius:10px;padding:10px 14px;background:var(--panel)}
#ref summary{cursor:pointer;font:600 11px var(--mono);letter-spacing:.12em;text-transform:uppercase;color:var(--ink-dim)}
#ref table{border-collapse:collapse;margin-top:8px;font:11.5px var(--mono)}
#ref td,#ref th{border:1px solid var(--line);padding:4px 10px;text-align:left}
#ref th{color:var(--ink-faint);font-weight:600}
#ref ul{margin:8px 0 0;padding-left:18px;font-size:12px;color:var(--ink-dim)}
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
<div id="layout">
  <main id="boardwrap">
    <div id="board"><svg id="edges" aria-hidden="true"></svg></div>
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
  draw();
}

// ---- search ----
$('search').addEventListener('input', (e) => {
  const q = e.target.value.trim().toLowerCase();
  for (const [mid, el] of cardEls) {
    const m = modById.get(mid);
    const hit = q === '' || (m.name + ' ' + m.path + ' ' + m.responsibility).toLowerCase().includes(q);
    el.classList.toggle('dim', q !== '' ? !hit : activeFlow !== null && !el.classList.contains('on'));
  }
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
  const b = el.getBoundingClientRect(), r = board.getBoundingClientRect();
  return { x: (side === 'l' ? b.left : b.right) - r.left, y: b.top - r.top + b.height / 2 };
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

// ---- reference (APIs / database / external services / notes) ----
(function renderRef() {
  const parts = [];
  if ((MAP.apis || []).length) {
    parts.push(`<details open><summary>API endpoints (${MAP.apis.length})</summary><table><tr><th>Method</th><th>Path</th><th>Handler</th><th>Description</th></tr>` +
      MAP.apis.map((a) => `<tr><td>${esc(a.method)}</td><td>${esc(a.path)}</td><td>${esc(a.handlerFile || '')}</td><td>${esc(a.description || '')}</td></tr>`).join('') + '</table></details>');
  }
  const db = MAP.database;
  if (db && (db.engine || (db.tables || []).length)) {
    parts.push(`<details><summary>Database — ${esc(db.engine || 'unknown')}${db.migrationsPath ? ' · ' + esc(db.migrationsPath) : ''}</summary><table><tr><th>Table</th><th>Purpose</th></tr>` +
      (db.tables || []).map((t) => `<tr><td>${esc(t.name)}</td><td>${esc(t.purpose || '')}</td></tr>`).join('') + '</table></details>');
  }
  if ((MAP.externalServices || []).length) {
    parts.push(`<details><summary>External services (${MAP.externalServices.length})</summary><table><tr><th>Service</th><th>Purpose</th><th>Used by</th></tr>` +
      MAP.externalServices.map((s) => `<tr><td>${esc(s.name)}</td><td>${esc(s.purpose)}</td><td>${esc((s.usedBy || []).map(name).join(', '))}</td></tr>`).join('') + '</table></details>');
  }
  const conv = MAP.conventions;
  if (conv && (conv.naming || conv.folderStructure || (conv.patternsUsed || []).length)) {
    parts.push(`<details><summary>Conventions</summary><ul>` +
      (conv.naming ? `<li>Naming: ${esc(conv.naming)}</li>` : '') +
      (conv.folderStructure ? `<li>Folders: ${esc(conv.folderStructure)}</li>` : '') +
      (conv.patternsUsed || []).map((x) => `<li>${esc(x)}</li>`).join('') + '</ul></details>');
  }
  if ((MAP.importantNotes || []).length) {
    parts.push(`<details open><summary>Important notes</summary><ul>` +
      MAP.importantNotes.map((x) => `<li>${esc(x)}</li>`).join('') + '</ul></details>');
  }
  document.getElementById('ref').innerHTML = parts.join('');
})();

function name(id) { const m = modById.get(id); return m ? m.name : id; }
function esc(s) { return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c])); }
</script>
</body>
</html>
