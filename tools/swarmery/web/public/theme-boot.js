// Pre-paint theme (Fusion phase 16): resolve {mode, palette} from localStorage
// and apply data-mode / data-theme / data-palette on <html> plus the page
// background BEFORE the stylesheet loads, so there is no flash-of-wrong-theme.
// The bg lookup mirrors --color-bg in index.css for every palette × resolved
// mode; index.css takes over once loaded.
//
// A file rather than an inline <script>: the daemon serves the shell with
// `script-src 'self'`, which runs no inline script. Loaded as a classic,
// render-blocking script from <head>, so it still runs before first paint.
(function () {
  var BG = {
    'swarm:dark': '#0e0f12', 'swarm:light': '#f6f2ea',
    'ocean:dark': '#0b0f14', 'ocean:light': '#eef3f7',
    'forest:dark': '#0c1210', 'forest:light': '#eef4ee',
    'sunset:dark': '#140d0e', 'sunset:light': '#f7efea',
    'zen:dark': '#101214', 'zen:light': '#f4f5f6',
    'ember:dark': '#120e0c', 'ember:light': '#f6f1ec'
  };
  var mode = 'system';
  var palette = 'swarm';
  try {
    var raw = localStorage.getItem('swarmery.theme');
    if (raw === 'light' || raw === 'dark') {
      mode = raw;
    } else if (raw) {
      var p = JSON.parse(raw);
      if (p && (p.mode === 'light' || p.mode === 'dark' || p.mode === 'system')) mode = p.mode;
      if (p && BG[p.palette + ':dark']) palette = p.palette;
    }
  } catch (e) {
    /* storage/JSON blocked — defaults apply */
  }
  var dark = mode === 'dark';
  if (mode === 'system') {
    try {
      dark = window.matchMedia('(prefers-color-scheme: dark)').matches;
    } catch (e) {
      dark = true;
    }
  }
  var resolved = dark ? 'dark' : 'light';
  var el = document.documentElement;
  el.dataset.mode = resolved;
  el.dataset.theme = resolved;
  el.dataset.palette = palette;
  var bg = BG[palette + ':' + resolved] || '#0e0f12';
  el.style.background = bg;
  var meta = document.querySelector('meta[name="theme-color"]');
  if (meta) meta.setAttribute('content', bg);
})();
