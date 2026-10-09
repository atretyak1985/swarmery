/* swarmery site: theme, nav, copy buttons, in-view clips, reels, player and lightbox */
(function () {
  var root = document.documentElement;
  root.classList.remove('noscript');
  var reduce = window.matchMedia && matchMedia('(prefers-reduced-motion: reduce)').matches;

  /* theme toggle: dark is the default, light is remembered per browser */
  function store(k, v) { try { if (v === undefined) return localStorage.getItem(k); localStorage.setItem(k, v); } catch (e) { return null; } }
  var tbtn = document.querySelector('[data-theme-toggle]');
  if (tbtn) tbtn.addEventListener('click', function () {
    var next = root.getAttribute('data-theme') === 'light' ? 'dark' : 'light';
    root.setAttribute('data-theme', next); store('swarmery-theme', next);
    themeMedia(next);
  });

  /* themed media: clips, posters, screenshots and full videos each have a light
     twin one folder down (assets/clips/light/…, video/light/…); paths in the
     HTML are the dark ones and are rewritten whenever the theme changes */
  var MEDIA_ATTRS = ['src', 'poster', 'srcset', 'href', 'data-src', 'data-clip', 'data-poster', 'data-play', 'data-zoom'];
  function themedUrl(u, light) {
    if (!u) return u;
    var d = u.replace(/(assets\/(?:clips|posters|img)\/)light\//g, '$1').replace(/(^|[\/\s,])video\/light\/(swarmery-)/g, '$1video/$2');
    return light ? d.replace(/(assets\/(?:clips|posters|img)\/)/g, '$1light/').replace(/(^|[\/\s,])video\/(swarmery-)/g, '$1video/light/$2') : d;
  }
  function themeMedia(theme) {
    var light = theme === 'light';
    document.querySelectorAll('video, img, source, a[download], [data-play], [data-zoom], [data-clip]').forEach(function (el) {
      var changed = false;
      MEDIA_ATTRS.forEach(function (a) {
        var v = el.getAttribute(a);
        if (!v || !/assets\/(clips|posters|img)\/|video\/(light\/)?swarmery-/.test(v)) return;
        var n = themedUrl(v, light);
        if (n !== v) { el.setAttribute(a, n); changed = true; }
      });
      if (changed && el.tagName === 'SOURCE' && el.parentNode && el.parentNode.load) el.parentNode.load();
      if (changed && el.tagName === 'VIDEO' && !el.paused) { var p = el.play(); if (p && p.catch) p.catch(function () {}); }
    });
  }
  if (root.getAttribute('data-theme') === 'light') themeMedia('light');

  /* nav: burger and features dropdown */
  var nav = document.querySelector('.nav');
  var burger = document.querySelector('.burger');
  if (burger) burger.addEventListener('click', function () {
    var o = nav.classList.toggle('open'); burger.setAttribute('aria-expanded', o);
  });
  document.querySelectorAll('.dd').forEach(function (dd) {
    var b = dd.querySelector('button');
    b.addEventListener('click', function (e) { e.stopPropagation(); var o = dd.classList.toggle('open'); b.setAttribute('aria-expanded', o); });
  });
  document.addEventListener('click', function (e) {
    document.querySelectorAll('.dd.open').forEach(function (dd) { if (!dd.contains(e.target)) { dd.classList.remove('open'); dd.querySelector('button').setAttribute('aria-expanded', 'false'); } });
  });
  document.addEventListener('keydown', function (e) { if (e.key === 'Escape') document.querySelectorAll('.dd.open').forEach(function (dd) { dd.classList.remove('open'); }); });

  /* copy buttons: data-copy holds the text, or the nearest pre/code is used */
  document.querySelectorAll('[data-copy]').forEach(function (b) {
    b.hidden = false;
    b.addEventListener('click', function () {
      var t = b.getAttribute('data-copy');
      if (!t) { var box = b.closest('.codeblock,.cmd,.inst'); var el = box && box.querySelector('pre,code,span'); t = el ? el.innerText : ''; }
      var done = function () { var o = b.textContent; b.textContent = 'Copied'; b.classList.add('ok'); setTimeout(function () { b.textContent = o; b.classList.remove('ok'); }, 1400); };
      if (navigator.clipboard) navigator.clipboard.writeText(t.trim()).then(done, function () {}); else done();
    });
  });

  /* GitHub stars, best effort */
  var stars = document.querySelectorAll('[data-stars]');
  if (stars.length && window.fetch) {
    fetch('https://api.github.com/repos/atretyak1985/swarmery').then(function (r) { return r.ok ? r.json() : null; }).then(function (j) {
      if (!j || typeof j.stargazers_count !== 'number') return;
      var n = j.stargazers_count, s = n >= 1000 ? (n / 1000).toFixed(1).replace(/\.0$/, '') + 'k' : String(n);
      stars.forEach(function (el) { el.textContent = '★ ' + s; });
    }).catch(function () {});
  }

  /* clips: muted loops that play only while on screen */
  var clips = [].slice.call(document.querySelectorAll('video[data-clip]'));
  function load(v) { if (v.dataset.src && !v.src) { v.src = v.dataset.src; } }
  if ('IntersectionObserver' in window) {
    var io = new IntersectionObserver(function (es) {
      es.forEach(function (e) {
        var v = e.target;
        if (e.isIntersecting) { load(v); if (!reduce && !v.hasAttribute('data-reel')) { var p = v.play(); if (p && p.catch) p.catch(function () {}); } }
        else if (!v.paused) v.pause();
      });
    }, { rootMargin: '200px 0px', threshold: 0.15 });
    clips.forEach(function (v) { io.observe(v); });
  } else clips.forEach(load);

  /* reels: tabs that swap one clip and advance when it ends */
  document.querySelectorAll('[data-reel-root]').forEach(function (rr) {
    var tabs = [].slice.call(rr.querySelectorAll('.reel-tab'));
    var v = rr.querySelector('video');
    var cap = rr.querySelector('[data-reel-cap]');
    var link = rr.querySelector('[data-reel-link]');
    var url = rr.querySelector('[data-reel-url]');
    var idx = 0, visible = false, raf;
    function bar() {
      cancelAnimationFrame(raf);
      var p = tabs[idx].querySelector('.prog');
      (function tick() { if (v.duration) p.style.width = (100 * v.currentTime / v.duration) + '%'; raf = requestAnimationFrame(tick); })();
    }
    function show(i, play) {
      tabs.forEach(function (t, j) { t.setAttribute('aria-selected', j === i); t.tabIndex = j === i ? 0 : -1; t.querySelector('.prog').style.width = '0'; });
      idx = i; var t = tabs[i];
      v.poster = t.dataset.poster; v.src = t.dataset.clip; v.loop = false;
      if (cap) cap.textContent = t.dataset.cap;
      if (link) link.href = t.dataset.href;
      if (url) url.textContent = t.dataset.url;
      if (play && visible && !reduce) { var p = v.play(); if (p && p.catch) p.catch(function () {}); }
      bar();
    }
    tabs.forEach(function (t, i) {
      t.addEventListener('click', function () { show(i, true); });
      t.addEventListener('keydown', function (e) {
        if (e.key === 'ArrowDown' || e.key === 'ArrowRight') { e.preventDefault(); show((idx + 1) % tabs.length, true); tabs[idx].focus(); }
        if (e.key === 'ArrowUp' || e.key === 'ArrowLeft') { e.preventDefault(); show((idx - 1 + tabs.length) % tabs.length, true); tabs[idx].focus(); }
      });
    });
    v.addEventListener('ended', function () { show((idx + 1) % tabs.length, true); });
    if ('IntersectionObserver' in window) new IntersectionObserver(function (es) {
      visible = es[0].isIntersecting;
      if (visible && !reduce) { var p = v.play(); if (p && p.catch) p.catch(function () {}); } else v.pause();
    }, { threshold: 0.3 }).observe(v);
    show(0, false);
  });

  /* modal player: any [data-play] opens the full video, optional chapters */
  var dlg = document.getElementById('player');
  if (dlg) {
    var pv = dlg.querySelector('video'), ttl = dlg.querySelector('[data-title]'), dl = dlg.querySelector('[data-dl]'), side = dlg.querySelector('.side'), body = dlg.querySelector('.modal-body');
    function close() { pv.pause(); dlg.close(); }
    dlg.querySelector('.modal-close').addEventListener('click', close);
    dlg.addEventListener('click', function (e) { if (e.target === dlg) close(); });
    dlg.addEventListener('close', function () { pv.pause(); });
    document.querySelectorAll('[data-play]').forEach(function (b) {
      b.addEventListener('click', function (e) {
        e.preventDefault();
        pv.src = b.dataset.play; pv.poster = b.dataset.poster || '';
        ttl.textContent = b.dataset.title || ''; dl.href = b.dataset.play;
        var ch = b.dataset.chapters ? JSON.parse(b.dataset.chapters) : null;
        side.innerHTML = '';
        if (ch && ch.length) {
          body.classList.add('with-ch');
          var h = document.createElement('h4'); h.textContent = 'Chapters'; side.appendChild(h);
          var ul = document.createElement('ul'); ul.className = 'chapters';
          ch.forEach(function (c) {
            var li = document.createElement('li'), btn = document.createElement('button');
            btn.innerHTML = '<span class="t"></span><span></span>';
            btn.firstChild.textContent = c[0]; btn.lastChild.textContent = c[1];
            btn.addEventListener('click', function () { var p = c[0].split(':'); pv.currentTime = (+p[0]) * 60 + (+p[1]); pv.play(); });
            li.appendChild(btn); ul.appendChild(li);
          });
          side.appendChild(ul);
        } else body.classList.remove('with-ch');
        dlg.showModal();
        if (b.dataset.t) { var tp = b.dataset.t.split(':'), at = (+tp[0]) * 60 + (+tp[1]); pv.addEventListener('loadedmetadata', function once() { pv.currentTime = at; pv.removeEventListener('loadedmetadata', once); }); }
        var p = pv.play(); if (p && p.catch) p.catch(function () {});
      });
    });
  }

  /* lightbox for screenshots */
  var lb = document.getElementById('lightbox');
  if (lb) {
    var li = lb.querySelector('img'), lc = lb.querySelector('[data-cap]');
    lb.querySelector('.modal-close').addEventListener('click', function () { lb.close(); });
    lb.addEventListener('click', function (e) { if (e.target === lb || e.target === li) lb.close(); });
    document.querySelectorAll('[data-zoom]').forEach(function (b) {
      b.addEventListener('click', function () { li.src = b.dataset.zoom; li.alt = b.dataset.alt || ''; lc.textContent = b.dataset.alt || ''; lb.showModal(); });
    });
  }

  /* plugin filters */
  var fl = document.querySelector('.filters');
  if (fl) fl.addEventListener('click', function (e) {
    var b = e.target.closest('button'); if (!b) return;
    fl.querySelectorAll('button').forEach(function (x) { x.setAttribute('aria-pressed', x === b); });
    var g = b.dataset.g;
    document.querySelectorAll('.pack').forEach(function (p) { p.hidden = g !== 'all' && p.dataset.g !== g; });
  });

  /* reveal */
  var rv = document.querySelectorAll('.rv');
  if ('IntersectionObserver' in window && !reduce) {
    var ro = new IntersectionObserver(function (es) { es.forEach(function (e) { if (e.isIntersecting) { e.target.classList.add('in'); ro.unobserve(e.target); } }); }, { threshold: 0.08 });
    rv.forEach(function (el) { ro.observe(el); });
  } else rv.forEach(function (el) { el.classList.add('in'); });
})();
