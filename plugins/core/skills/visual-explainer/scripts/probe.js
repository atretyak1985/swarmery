() => {
  // probe.js — run inside the rendered page (pass this whole file as the `function`
  // argument of a browser evaluate tool). Returns measured layout defects as JSON.
  // Run it once per viewport/theme you check; it does not modify the page.
  const W = window.innerWidth;
  const report = { viewport: W + 'x' + window.innerHeight, scheme: matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light' };

  // 1. Horizontal overflow of the page body (ignores things inside their own scroll
  //    container, fixed/off-canvas panels, and zero-size nodes).
  const inScroller = (el) => {
    for (let p = el.parentElement; p && p !== document.body; p = p.parentElement) {
      const s = getComputedStyle(p);
      if (/(auto|scroll|hidden|clip)/.test(s.overflowX)) return true;
      if (s.position === 'fixed') return true;
    }
    return false;
  };
  const overflow = [];
  for (const el of document.body.querySelectorAll('*')) {
    const r = el.getBoundingClientRect();
    if (!r.width || r.right <= W + 1) continue;
    const s = getComputedStyle(el);
    if (s.position === 'fixed' || s.visibility === 'hidden' || inScroller(el)) continue;
    overflow.push(`${el.tagName.toLowerCase()}${el.id ? '#' + el.id : ''}${typeof el.className === 'string' && el.className ? '.' + el.className.split(' ')[0] : ''} right=${Math.round(r.right)} "${(el.textContent || '').trim().slice(0, 40)}"`);
  }
  report.pageScrollWidth = document.documentElement.scrollWidth;
  report.horizontalOverflow = document.documentElement.scrollWidth > W + 1 ? overflow.slice(0, 12) : [];

  // 2. SVG labels: outside the viewBox, and overlapping each other.
  const svgIssues = [];
  document.querySelectorAll('svg[viewBox]').forEach((svg, si) => {
    const vb = svg.viewBox.baseVal;
    if (!vb || !vb.width) return;
    const label = svg.getAttribute('aria-label') ? svg.getAttribute('aria-label').slice(0, 30) : `svg#${si}`;
    const boxes = [];
    svg.querySelectorAll('text').forEach((t) => {
      if (!t.textContent.trim() || t.closest('defs,marker')) return;
      let b; try { b = t.getBBox(); } catch (e) { return; }
      if (!b.width) return;
      // map through the element's transform to the svg user space
      const m = t.getCTM && svg.getCTM ? svg.getCTM().inverse().multiply(t.getCTM()) : null;
      const x = m ? b.x * m.a + m.e : b.x, y = m ? b.y * m.d + m.f : b.y;
      const w = m ? b.width * m.a : b.width, h = m ? b.height * m.d : b.height;
      const txt = t.textContent.trim().slice(0, 30);
      if (x < vb.x - 1 || y < vb.y - 1 || x + w > vb.x + vb.width + 1 || y + h > vb.y + vb.height + 1) {
        svgIssues.push(`[${label}] "${txt}" leaves the viewBox (x ${Math.round(x)}–${Math.round(x + w)} of ${vb.width})`);
      }
      boxes.push({ x, y, w, h, txt });
    });
    for (let i = 0; i < boxes.length; i++) for (let j = i + 1; j < boxes.length; j++) {
      const a = boxes[i], b = boxes[j];
      const ix = Math.min(a.x + a.w, b.x + b.w) - Math.max(a.x, b.x);
      const iy = Math.min(a.y + a.h, b.y + b.h) - Math.max(a.y, b.y);
      if (ix > 2 && iy > 2 && ix * iy > 0.15 * Math.min(a.w * a.h, b.w * b.h)) svgIssues.push(`[${label}] "${a.txt}" overlaps "${b.txt}"`);
    }
  });
  report.svgLabelIssues = svgIssues.slice(0, 20);

  // 3. Text clipped by its own box (overflow hidden/clip and content wider than the box).
  const clipped = [];
  for (const el of document.body.querySelectorAll('main *')) {
    const s = getComputedStyle(el);
    if (!/(hidden|clip)/.test(s.overflowX) || s.textOverflow === 'ellipsis') continue;
    if (el.scrollWidth > el.clientWidth + 2 && el.clientWidth > 0 && el.children.length === 0) clipped.push(`${el.tagName.toLowerCase()} "${el.textContent.trim().slice(0, 40)}"`);
  }
  report.clippedText = clipped.slice(0, 10);

  // 4. Shell wiring at runtime: hotspots without content, terms without glossary.
  const g = document.getElementById('glossary');
  let gl = {}; try { gl = g ? JSON.parse(g.textContent) : {}; } catch (e) { report.glossaryError = String(e); }
  const d = document.getElementById('details');
  let dl = {}; try { dl = d ? JSON.parse(d.textContent) : {}; } catch (e) { report.detailsError = String(e); }
  report.deadHotspots = [...new Set([...document.querySelectorAll('[data-d]')].map((e) => e.getAttribute('data-d')))].filter((k) => !document.getElementById('d-' + k) && !(k in dl));
  report.undefinedTerms = [...new Set([...document.querySelectorAll('[data-t]')].map((e) => e.getAttribute('data-t')))].filter((k) => !(k in gl));

  report.ok = !report.horizontalOverflow.length && !report.svgLabelIssues.length && !report.clippedText.length && !report.deadHotspots.length && !report.undefinedTerms.length;
  return report;
}
