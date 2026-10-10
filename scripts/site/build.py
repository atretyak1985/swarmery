#!/usr/bin/env python3
"""Builds the static swarmery site (site/) from data.py. No dependencies.

Usage: python3 scripts/site/build.py [--strict] [outdir]
Pages are generated; site/assets/site.css and site.js are edited by hand.
Short clips, posters and screenshots under site/assets/ are made by scripts/site/media.sh.

Languages: English is written to the root, every other language in LANGS to
<lang>/ with the same paths. Template strings come from locales/<lang>.py UI
(merged over locales/en.py); translated data.py copy comes from that module's
CONTENT. A string a language does not translate falls back to English with a
warning; --strict lists every such key as <lang>:<key> and exits 1.
SITE_LOCALES_DIR overrides the locales directory (used by the tests).
"""
import copy, importlib.util, json, os, re, sys, html
from datetime import date
from types import SimpleNamespace
from data import *

ARGS = [a for a in sys.argv[1:] if a != "--strict"]
STRICT = "--strict" in sys.argv[1:]
OUT = ARGS[0] if ARGS else "out"
BASE = "https://atretyak1985.github.io/swarmery/"
E = html.escape

# ---------------------------------------------------------------- languages

LANGS = ("en", "uk")
OG_LOCALE = {"en": "en_US", "uk": "uk_UA"}
# Voiced cuts per language: docs/video/<lang>/ (light twins in <lang>/light/);
# an episode a language has no cut for plays the English one.
VIDEO_DIR = os.path.join(os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__)))), "docs", "video")
LOCALES_DIR = os.environ.get("SITE_LOCALES_DIR") or os.path.join(os.path.dirname(os.path.abspath(__file__)), "locales")
MISSING = {}  # "<lang>:<key>" -> None, in first-seen order

def die(msg):
    sys.exit("build.py: " + msg)

def load_locale(lang):
    path = os.path.join(LOCALES_DIR, lang + ".py")
    if not os.path.exists(path): die(f"no locale module {path}")
    spec = importlib.util.spec_from_file_location("site_locale_" + lang, path)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod

EN = load_locale("en")
CTX = SimpleNamespace(lang="en", prefix="", ui=EN.UI, own=EN.UI, content={}, strict=STRICT)
ESCAPED = frozenset(EN.ESCAPED_KEYS)
for _k in ESCAPED - EN.UI.keys(): die(f"locales/en.py: ESCAPED_KEYS names {_k!r}, which UI does not define")

def miss(key):
    if CTX.lang != "en": MISSING.setdefault(f"{CTX.lang}:{key}")

def unknown(key):
    """A translated entry that names nothing in en.UI / data.py: reported like a missing one."""
    MISSING.setdefault(f"{CTX.lang}:unknown:{key}")

def apply_fmt(key, s, args=(), kw=None):
    """s % args, then s.format(**kw); a translation whose placeholders disagree stops the build."""
    try:
        if args: s = s % args
        if kw: s = s.format(**kw)
    except (TypeError, ValueError, KeyError, IndexError) as e:
        die(f"{CTX.lang}:{key}: format mismatch in {s!r} ({type(e).__name__}: {e}); keep the placeholders of locales/en.py")
    return s

def T(key, *args, **kw):
    """The current language's string for key; English (and a missing entry) when it has none."""
    if key not in EN.UI: die(f"unknown UI key {key!r}: add it to locales/en.py")
    if key not in CTX.own: miss(key)
    return apply_fmt(key, CTX.ui[key], args, kw)

def TE(key, *args, **kw):
    """T() HTML-escaped, for attribute values; only for keys listed in en.ESCAPED_KEYS."""
    if key not in ESCAPED: die(f"TE({key!r}): add the key to ESCAPED_KEYS in locales/en.py, or use T() for an HTML key")
    return E(T(key, *args, **kw))

def rel_for(depth):
    """Relative path from a page at depth to the site root, where assets/ and video/ live once."""
    return "../" * (depth + (1 if CTX.prefix else 0))

def pg(rel):
    """Relative path to the current language's root: page links stay inside <lang>/."""
    return rel[len("../"):] if CTX.prefix else rel

FEATURE_FIELDS = ("name", "kicker", "title", "lede", "menu")
CONTENT_MAPS = ("features", "promo", "shot_captions", "principles", "cp_pages", "posts")

def report_unknown(name, over, known):
    """Every CONTENT[name] entry that data.py does not have (a renamed slug, a removed post)."""
    for k in over:
        if k not in known: unknown(f"content.{name}.{k}")

def merge_list(key, base, over, put):
    """Overlay over[i] on base[i] via put(base_item, over_item); None or a short list = missing."""
    if over is None:
        miss(key)
        return list(base)
    if len(over) != len(base): die(f"{CTX.lang}: {key} has {len(over)} entries, data.py has {len(base)}")
    out = []
    for i, (b, o) in enumerate(zip(base, over)):
        if o is None:
            miss(f"{key}.{i}")
            out.append(b)
        else:
            out.append(put(b, o))
    return out

def put_beat(b, o):
    if len(o) != 4: die(f"{CTX.lang}: a beat overlay is (kicker, title, text, bullets), got {len(o)} fields")
    return (o[0], o[1], o[2], list(o[3])) + tuple(b[4:])

def put_chapter(b, o):
    return (b[0], o)

def merged_features():
    if CTX.lang == "en": return FEATURES
    feats = copy.deepcopy(FEATURES)
    over = CTX.content.get("features", {})
    report_unknown("features", over, {f["slug"] for f in feats})
    for f in feats:
        key = "content.features." + f["slug"]
        o = over.get(f["slug"], {})
        for fld in FEATURE_FIELDS:
            if fld in o: f[fld] = o[fld]
            else: miss(f"{key}.{fld}")
        f["beats"] = merge_list(key + ".beats", f["beats"], o.get("beats"), put_beat)
        f["chapters"] = merge_list(key + ".chapters", f["chapters"], o.get("chapters"), put_chapter)
    return feats

def merged_promo():
    if CTX.lang == "en": return PROMO
    p = copy.deepcopy(PROMO)
    o = CTX.content.get("promo", {})
    for fld in ("title", "text"):
        if fld in o: p[fld] = o[fld]
        else: miss("content.promo." + fld)
    if "dur" in o:  # the language ships its own cut: its own length and (time, title) chapters
        if not has_cut(p["file"]): die(f"{CTX.lang}: content.promo.dur is set but docs/video/{CTX.lang}/{p['file']} is missing")
        if any(not isinstance(c, (list, tuple)) or len(c) != 2 for c in o.get("chapters") or [None]):
            die(f"{CTX.lang}: content.promo with its own dur needs (time, title) chapters")
        p["dur"], p["chapters"] = o["dur"], [tuple(c) for c in o["chapters"]]
    else:
        p["chapters"] = merge_list("content.promo.chapters", p["chapters"], o.get("chapters"), put_chapter)
    return p

def has_cut(name):
    return CTX.lang != "en" and os.path.isfile(os.path.join(VIDEO_DIR, CTX.lang, name))

def vid(rel, name):
    """URL of a full video: the current language's voiced cut when there is one, else the English one."""
    return f"{rel}video/{CTX.lang}/{name}" if has_cut(name) else f"{rel}video/{name}"

def merged_principles():
    if CTX.lang == "en": return PRINCIPLES
    over = CTX.content.get("principles", {})
    report_unknown("principles", over, {p[0] for p in PRINCIPLES})
    out = []
    for pid, t, d in PRINCIPLES:
        o = over.get(pid)
        if o is None:
            miss("content.principles." + pid)
            out.append((pid, t, d))
        elif len(o) != 3:
            die(f"{CTX.lang}: content.principles.{pid} is (label, title, text)")
        else:
            out.append(tuple(o))
    return out

def merged_cp_pages():
    if CTX.lang == "en": return CP_PAGES
    over = CTX.content.get("cp_pages", {})
    report_unknown("cp_pages", over, {name for name, _ in CP_PAGES})
    out = []
    for name, d in CP_PAGES:
        if name in over: out.append((name, over[name]))
        else:
            miss("content.cp_pages." + name)
            out.append((name, d))
    return out

def merged_posts():
    """Post tags by post URL (p[3]): adding or reordering a post in data.py never shifts a translation."""
    if CTX.lang == "en": return POSTS
    over = CTX.content.get("posts", {})
    report_unknown("posts", over, {p[3] for p in POSTS})
    out = []
    for p in POSTS:
        if p[3] in over: out.append(tuple(p[:5]) + (over[p[3]],))
        else:
            miss("content.posts." + p[3])
            out.append(p)
    return out

def caption(shot):
    cap = CAPTIONS.get(shot, "")
    if CTX.lang == "en" or not cap: return cap
    over = CTX.content.get("shot_captions", {})
    if shot in over: return over[shot]
    miss("content.shot_captions." + shot)
    return cap

def set_lang(lang):
    mod = EN if lang == "en" else load_locale(lang)
    own = dict(getattr(mod, "UI", {}))
    CTX.lang, CTX.prefix = lang, ("" if lang == "en" else lang + "/")
    CTX.own, CTX.ui = own, {**EN.UI, **own}
    CTX.content = dict(getattr(mod, "CONTENT", {}))
    for key in own:
        if key not in EN.UI: unknown(key)
        elif key in ESCAPED and re.search(r"<[A-Za-z/!]|&[A-Za-z0-9#]+;", own[key]):
            die(f"{lang}:{key}: ESCAPED_KEYS hold plain text (escaped at the call site); drop the markup or entity")
    for m in CTX.content:
        if m not in CONTENT_MAPS: unknown("content." + m)
    report_unknown("shot_captions", CTX.content.get("shot_captions", {}), CAPTIONS)
    for attr in ("MONTHS", "DATE"):
        val = getattr(mod, attr, None)
        if val is None: miss(attr.lower())
        setattr(CTX, attr.lower(), val if val is not None else getattr(EN, attr))
    if len(CTX.months) != 12: die(f"{lang}: MONTHS needs 12 entries")
    CTX.features = merged_features()
    CTX.promo = merged_promo()
    CTX.principles = merged_principles()
    CTX.cp_pages = merged_cp_pages()
    CTX.posts = merged_posts()

def fmt_date(iso):
    d = date.fromisoformat(iso)
    return apply_fmt("date", CTX.date, kw=dict(mon=CTX.months[d.month - 1], d=d.day, y=d.year))

HEX = '<svg viewBox="0 0 32 32" aria-hidden="true"><path fill="currentColor" fill-rule="evenodd" d="M16 2.4 4.22 9.2v13.6L16 29.6l11.78-6.8V9.2ZM16 6.9 8.12 11.45v9.1L16 25.1l7.88-4.55v-9.1Z"/><path fill="currentColor" d="M16 11.5 12.1 13.75v4.5L16 20.5l3.9-2.25v-4.5Z"/></svg>'
GH = '<svg viewBox="0 0 16 16" aria-hidden="true"><path fill="currentColor" d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.013 8.013 0 0016 8c0-4.42-3.58-8-8-8z"/></svg>'
SUN = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" aria-hidden="true"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/></svg>'
MENU = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" aria-hidden="true"><path d="M4 7h16M4 12h16M4 17h16"/></svg>'
CHEV = '<svg viewBox="0 0 10 10" aria-hidden="true"><path d="M1.5 3.5 5 7l3.5-3.5" fill="none" stroke="currentColor" stroke-width="1.5"/></svg>'
PLAY = '<svg viewBox="0 0 20 20" aria-hidden="true"><path fill="currentColor" d="M5 3.5v13l11-6.5z"/></svg>'
ARROW = '<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" aria-hidden="true"><path d="M3 8h10M9 4l4 4-4 4"/></svg>'

def wordmark():
    return '<span>SW<span class="o">&#x2B21;</span>RMERY</span>'

def head(title, desc, rel, path, alternates=True):
    url = BASE + CTX.prefix + path
    # one twin per language at the same path; 404.html exists only in English
    alt = "".join(f'<link rel="alternate" hreflang="{hl}" href="{BASE}{p}{path}">\n'
                  for hl, p in (("en", ""), ("uk", "uk/"), ("x-default", ""))) if alternates else ""
    serif = '<link href="https://fonts.googleapis.com/css2?family=Literata:ital,opsz,wght@0,7..72,400;0,7..72,500;1,7..72,400;1,7..72,500&amp;display=swap" rel="stylesheet">\n' if CTX.lang == "uk" else ""
    return f'''<!doctype html>
<html lang="{CTX.lang}" class="noscript" data-i18n-copied="{TE('js.copied')}" data-i18n-chapters="{TE('js.chapters')}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="dark light">
<meta name="theme-color" content="#0A0B0D">
<title>{E(title)}</title>
<meta name="description" content="{E(desc)}">
<link rel="canonical" href="{url}">
{alt}<meta property="og:type" content="website">
<meta property="og:site_name" content="Swarmery">
<meta property="og:locale" content="{OG_LOCALE[CTX.lang]}">
<meta property="og:title" content="{E(title)}">
<meta property="og:description" content="{E(desc)}">
<meta property="og:url" content="{url}">
<meta property="og:image" content="{BASE}og.png">
<meta property="og:image:width" content="1200">
<meta property="og:image:height" content="630">
<meta name="twitter:card" content="summary_large_image">
<meta name="twitter:site" content="@SwarmeryDev">
<meta name="twitter:title" content="{E(title)}">
<meta name="twitter:description" content="{E(desc)}">
<meta name="twitter:image" content="{BASE}og.png">
<link rel="icon" href="{rel}favicon.svg" type="image/svg+xml">
<script>try{{var t=localStorage.getItem('swarmery-theme');if(t==='light')document.documentElement.setAttribute('data-theme','light')}}catch(e){{}}</script>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Fraunces:ital,opsz,wght@0,9..144,400;0,9..144,500;1,9..144,400;1,9..144,500&amp;family=Inter:wght@400;500;600&amp;family=JetBrains+Mono:wght@400;500;600&amp;display=swap" rel="stylesheet">
{serif}<link rel="stylesheet" href="{rel}assets/site.css">
</head>
<body>
<a class="skip" href="#main">{T('skip')}</a>
'''

def nav(rel, cur, path):
    p = pg(rel)
    feats = "".join(f'<a href="{p}features/{f["slug"]}/"><span class="n">0{f["n"]}</span><span><b>{f["name"]}</b><span>{E(f["menu"])}</span></span></a>' for f in CTX.features)
    def a(href, key):
        c = ' aria-current="page"' if cur == key else ''
        return f'<a href="{p}{href}"{c}>{T("nav." + key)}</a>'
    fcur = ' cur' if cur == 'features' else ''
    # the twin of this page in the other language: rel is the shared (English) root
    other_lang = "en" if CTX.prefix else "uk"
    other = rel + ("" if CTX.prefix else "uk/") + path
    return f'''<header class="nav">
  <div class="wrap">
    <a class="brand" href="{p}" aria-label="{TE('nav.home_aria')}">{HEX}{wordmark()}</a>
    <nav class="menu" aria-label="{TE('nav.main_aria')}">
      <div class="dd{fcur}"><button type="button" aria-expanded="false" aria-haspopup="true">{T('nav.features')} {CHEV}</button>
        <div class="dd-panel">{feats}<a class="all" href="{p}features/">{T('nav.all_features')} &rarr;</a></div>
      </div>
      {a("videos/", "videos")}
      {a("plugins/", "plugins")}
      {a("install/", "install")}
      {a("blog/", "blog")}
      <a href="{REPO}/tree/main/docs">{T('nav.docs')}</a>
    </nav>
    <div class="nav-r">
      <a class="lang" href="{other}" hreflang="{other_lang}" lang="{other_lang}" aria-label="{TE('nav.switch_aria')}">{T('nav.switch_label')}</a>
      <button class="icon-btn" type="button" data-theme-toggle aria-label="{TE('nav.theme_aria')}">{SUN}</button>
      <a class="gh" href="{REPO}">{GH}<span class="lbl">GitHub</span><span class="stars" data-stars></span></a>
      <button class="icon-btn burger" type="button" aria-label="{TE('nav.menu_aria')}" aria-expanded="false">{MENU}</button>
    </div>
  </div>
</header>
<main id="main">
'''

def footer(rel):
    p = pg(rel)
    feats = "".join(f'<li><a href="{p}features/{f["slug"]}/">{f["name"]}</a></li>' for f in CTX.features)
    return f'''</main>
<footer>
  <div class="wrap">
    <div class="foot">
      <div>
        <a class="brand" href="{p}" aria-label="{TE('nav.home_aria')}">{HEX}{wordmark()}</a>
        <p>{T('footer.tagline')}</p>
      </div>
      <div><h4>{T('footer.product')}</h4><ul>{feats}</ul></div>
      <div><h4>{T('footer.learn')}</h4><ul>
        <li><a href="{p}install/">{T('nav.install')}</a></li>
        <li><a href="{p}videos/">{T('nav.videos')}</a></li>
        <li><a href="{p}plugins/">{T('nav.plugins')}</a></li>
        <li><a href="{REPO}/tree/main/docs">{T('nav.docs')}</a></li>
        <li><a href="{REPO}/blob/main/CHANGELOG.md">{T('footer.changelog')}</a></li>
      </ul></div>
      <div><h4>{T('footer.follow')}</h4><ul>
        <li><a href="{REPO}">GitHub</a></li>
        <li><a href="{p}blog/">{T('nav.blog')}</a></li>
        <li><a href="{SUBSTACK}">Substack</a></li>
        <li><a href="{X}">X · @SwarmeryDev</a></li>
      </ul></div>
    </div>
    <div class="foot-b">
      <span>{T('footer.license')}</span>
      <span>{T('footer.disclaimer')}</span>
    </div>
  </div>
</footer>
<dialog class="modal" id="player" aria-label="{TE('dialog.player_aria')}">
  <div class="modal-wrap">
    <button class="modal-close" type="button" aria-label="{TE('dialog.close')}">&times;</button>
    <div class="modal-inner">
      <div class="modal-body"><video controls playsinline preload="none"></video><div class="side"></div></div>
      <div class="modal-foot"><span data-title></span><a data-dl class="muted" href="#" download>{T('dialog.download')}</a></div>
    </div>
  </div>
</dialog>
<dialog class="modal" id="lightbox" aria-label="{TE('dialog.lightbox_aria')}">
  <div class="modal-wrap">
    <button class="modal-close" type="button" aria-label="{TE('dialog.close')}">&times;</button>
    <div class="modal-inner"><img alt=""><div class="modal-foot"><span data-cap></span><span class="muted">{T('dialog.fictional')}</span></div></div>
  </div>
</dialog>
<script src="{rel}assets/site.js" defer></script>
</body>
</html>
'''

def clip(rel, name, url, cls="", live=True):
    lv = f'<span class="live">{T("clip.live")}</span>' if live else ''
    return f'''<div class="frame {cls}">{lv}<video data-clip muted loop playsinline preload="none" poster="{rel}assets/clips/{name}.jpg" data-src="{rel}assets/clips/{name}.mp4" aria-label="{E(url)}"></video></div>'''

def ep_attrs(rel, f):
    ch = json.dumps(f["chapters"])
    return f'data-play="{vid(rel, "swarmery-" + f["ep"] + ".mp4")}" data-poster="{rel}assets/posters/{f["ep"]}.jpg" data-title="{TE("ep.label", f["n"])} · {E(f["name"])} · {f["dur"]}" data-chapters=\'{E(ch, quote=True)}\''

def promo_attrs(rel):
    p = CTX.promo
    ch = json.dumps(p["chapters"])
    return f'data-play="{vid(rel, p["file"])}" data-poster="{rel}assets/posters/promo.jpg" data-title="{E(p["title"])} · {p["dur"]}" data-chapters=\'{E(ch, quote=True)}\''

def ep_card(rel, f, k=None):
    if f.get("soon"):
        return f'''<a class="ep" href="{pg(rel)}features/{f["slug"]}/"><div class="th"><img src="{rel}assets/posters/{f["ep"]}.jpg" alt="" loading="lazy"><span class="soon">{T('ep.soon')}</span><div class="play"><span>{PLAY}</span></div></div>
<div class="meta"><div class="k">{k or T('ep.label', f["n"])}</div><h3>{f["name"]}</h3><p>{E(f["kicker"])}. {T('ep.silent_preview')}</p></div></a>'''
    return f'''<button class="ep" type="button" {ep_attrs(rel, f)}><div class="th"><img src="{rel}assets/posters/{f["ep"]}.jpg" alt="" loading="lazy"><div class="play"><span>{PLAY}</span></div><span class="dur">{f["dur"]}</span></div>
<div class="meta"><div class="k">{k or T('ep.label', f["n"])}</div><h3>{f["name"]}</h3><p>{E(f["kicker"])}.</p></div></button>'''

def post_card(rel, p, lead=False):
    d, t, sub, url, img, tag = p
    dd = fmt_date(d)
    sp = f'<p>{E(sub)}</p>' if sub else ''
    return f'''<a class="post{' lead' if lead else ''}" href="{url}"><div class="cv"><img src="{cover(img, 1100 if lead else 720)}" alt="" loading="lazy" referrerpolicy="no-referrer" onerror="this.remove()"></div>
<div class="meta"><div class="d"><span class="s">{E(tag)}</span><span>{dd}</span></div><h3>{E(t)}</h3>{sp}<span class="go">{T('post.read')} &rarr;</span></div></a>'''

def shots(rel, names, k):
    out = []
    for n in names:
        cap = caption(n)
        out.append(f'''<figure class="shot rv"><button type="button" data-zoom="{rel}assets/img/{n}-lg.webp" data-alt="{E(cap)}"><img src="{rel}assets/img/{n}.webp" srcset="{rel}assets/img/{n}-sm.webp 800w, {rel}assets/img/{n}.webp 1600w" sizes="(max-width:760px) 100vw, 600px" alt="{E(cap)}" loading="lazy" width="1600" height="900"></button><figcaption><b>{k}</b>{E(cap)}</figcaption></figure>''')
    return "\n".join(out)

def cta(rel, title=None, text=None):
    return f'''<section class="cta"><div class="glow"></div><div class="wrap">
<h2>{title or T('cta.title')}</h2><p>{text or T('cta.text')}</p>
<div class="row"><a class="btn btn-primary" href="{pg(rel)}install/">{T('cta.start')} {ARROW}</a><a class="btn" href="{REPO}">{GH} {T('cta.star')}</a></div>
</div></section>'''

# Seeds for the two counter rows. apply-counts.sh owns the numbers: a rebuild keeps
# whatever region the language's index.html already carries (see __main__).
STATS_HERO_SEED = ("stats-hero", "        ", [
    ("14", "stats.hero.packs", ""), ("26", "stats.hero.agents", ""),
    ("70", "stats.hero.skills", ""), (":7777", "stats.hero.port", "hot")])
STATS_CP_SEED = ("stats-control-plane", "            ", [
    ("101", "stats.cp.go_packages", ""), ("222", "stats.cp.routes", ""),
    (":7777", "stats.cp.port", "hot"), ("127.0.0.1", "stats.cp.bind", "sm")])
KEPT = {}  # region name -> the region the current language's index.html carried

def stats(seed):
    name, ind, tiles = seed
    if name in KEPT: return KEPT[name]
    rows = [f"{ind}<!-- BEGIN generated:{name} -->"]
    for n, key, kind in tiles:
        rows.append(f'{ind}<div class="stat{" hot" if kind == "hot" else ""}"><div class="n{" sm" if kind == "sm" else ""}">{n}</div><div class="l">{T(key)}</div></div>')
    rows.append(f"{ind}<!-- END generated:{name} -->")
    return "\n".join(rows)

INSTALL_1 = "/plugin marketplace add atretyak1985/swarmery\n/plugin install core@swarmery"
INSTALL_2 = "bash scripts/init.sh"
INSTALL_3 = "curl -fsSL https://raw.githubusercontent.com/atretyak1985/swarmery/main/scripts/install.sh | bash\nswarmery serve"

def codeblock(txt):
    return f'<div class="codeblock"><pre><code>{E(txt)}</code></pre><button class="copy" type="button" data-copy="{E(txt)}" hidden>{T("code.copy")}</button></div>'

# ---------------------------------------------------------------- pages

def page_home():
    rel = rel_for(0)
    p = pg(rel)
    F = CTX.features
    reel_order = [("plan", F[0], 0), ("run", F[3], 1), ("decide", F[1], 0), ("measure", F[2], 0), ("learn", F[4], 0), ("remember", F[5], 1)]
    tabs = []
    for i, (k, f, bi) in enumerate(reel_order):
        b = f["beats"][bi]
        tabs.append(f'''<button class="reel-tab" type="button" role="tab" data-clip="{rel}assets/clips/{b[4]}.mp4" data-poster="{rel}assets/clips/{b[4]}.jpg" data-cap="{E(b[1])}" data-url="{E(b[5])}" data-href="{p}features/{f["slug"]}/"><span class="k"><b>0{i+1}</b>{T("home.reel.k." + k)}</span><h3>{f["name"]}</h3><p>{TE("home.reel." + k)}</p><span class="prog"></span></button>''')
    bento = []
    spans = ["span-3", "span-3", "span-2", "span-2", "span-2", "span-6 wide"]
    for f, sp in zip(F, spans):
        b = f["beats"][0]
        bento.append(f'''<a class="card rv {sp}" href="{p}features/{f["slug"]}/"><div class="media"><video data-clip muted loop playsinline preload="none" poster="{rel}assets/clips/{b[4]}.jpg" data-src="{rel}assets/clips/{b[4]}.mp4" aria-hidden="true"></video></div><div class="body"><span class="k">0{f["n"]} · {f["name"]}</span><h3>{E(f["kicker"])}</h3><p>{E(f["menu"])}.</p><span class="go">{T("explore", name=f["name"].lower())} {ARROW}</span></div></a>''')
    # the last bento card (knowledge) gets the side-by-side layout on wide screens via span-6; keep it simple
    eps = "".join(ep_card(rel, f) for f in F[:3])
    posts = "".join(post_card(rel, p) for p in CTX.posts[:3])
    principles = "".join(f'<div class="tile rv"><span class="k">{k}</span><h3>{t}</h3><p>{d}</p></div>' for k, t, d in CTX.principles)
    body = f'''
<section class="hero"><div class="hexbg"></div>
<div class="wrap">
  <div class="badge-row"><span class="pill"><span class="dot"></span>{T('home.hero.pill')}</span><span class="pill amber"><span class="dot"></span>{T('home.hero.pill_new')}</span></div>
  <h1>{T('home.hero.title')}</h1>
  <p class="lede">{T('home.hero.lede')}</p>
  <div class="row">
    <a class="btn btn-primary" href="{p}install/">{T('cta.start')} {ARROW}</a>
    <button class="btn" type="button" {promo_attrs(rel)}>{PLAY} {T('home.hero.tour')}</button>
  </div>
  <div class="row" style="margin-top:18px"><div class="cmd"><span class="p">&gt;</span><code>/plugin marketplace add atretyak1985/swarmery</code><button class="copy" type="button" data-copy="/plugin marketplace add atretyak1985/swarmery" hidden>{T('code.copy')}</button></div></div>
  <div class="hero-shot" style="margin-top:64px">
    <div class="glow"></div>
    {clip(rel, "hero", "127.0.0.1:7777/sessions")}
    <div class="float-chip fc-1"><span class="ic">3</span><span>{T('home.hero.chip1')}<small>{T('home.hero.chip1_sub')}</small></span></div>
    <div class="float-chip fc-2"><span class="ic">$</span><span>{T('home.hero.chip2')}<small>{T('home.hero.chip2_sub')}</small></span></div>
  </div>
  <div class="stats rv" style="margin-top:56px">
{stats(STATS_HERO_SEED)}
  </div>
</div></section>

<section class="sec-tight divider"><div class="wrap">
  <div class="pains">
    <div class="pain rv"><span class="tag">{T('home.pain.tag')}</span><div class="q">{T('home.pain.1.q')}</div><p>{T('home.pain.1.p')}</p></div>
    <div class="pain rv"><span class="tag">{T('home.pain.tag')}</span><div class="q">{T('home.pain.2.q')}</div><p>{T('home.pain.2.p')}</p></div>
    <div class="pain rv"><span class="tag">{T('home.pain.tag')}</span><div class="q">{T('home.pain.3.q')}</div><p>{T('home.pain.3.p')}</p></div>
  </div>
</div></section>

<section class="sec divider" id="loop"><div class="wrap">
  <div class="sec-head"><p class="eyebrow">{T('home.loop.eyebrow')}</p><h2>{T('home.loop.title')}</h2><p>{T('home.loop.text')}</p></div>
  <div class="reel" data-reel-root>
    <div class="reel-tabs" role="tablist" aria-label="{TE('home.loop.aria')}">{"".join(tabs)}</div>
    <div class="reel-stage">
      <div class="frame"><span class="live">{T('clip.live')}</span><video data-reel muted playsinline preload="none" aria-label="{TE('home.loop.clip_aria')}"></video></div>
      <div class="cap"><span data-reel-cap></span><a data-reel-link href="{p}features/planning/">{T('home.loop.open')} &rarr;</a></div>
    </div>
  </div>
</div></section>

<section class="sec divider"><div class="wrap">
  <div class="sec-head"><p class="eyebrow">{T('home.features.eyebrow')}</p><h2>{T('home.features.title')}</h2><p>{T('home.features.text')}</p></div>
  <div class="bento">{"".join(bento)}</div>
</div></section>

<section class="sec divider"><div class="wrap">
  <div class="split">
    <div class="txt rv">
      <p class="eyebrow">{T('home.cp.eyebrow')}</p>
      <h2>{T('home.cp.title')}</h2>
      <p>{T('home.cp.text')}</p>
      <ul><li>{T('home.cp.li1')}</li><li>{T('home.cp.li2')}</li><li>{T('home.cp.li3')}</li></ul>
    </div>
    <div class="term rv"><div class="bar"><i></i><i></i><i></i><span>~ zsh</span></div><pre><span class="p">$</span> swarmery serve
<span class="c">{T('home.cp.term.title')}</span>
<span class="g">✓</span> {T('home.cp.term.backfill')}
<span class="g">✓</span> {T('home.cp.term.listen')} <span class="b">127.0.0.1:7777</span>
<span class="g">✓</span> {T('home.cp.term.ready')}</pre></div>
  </div>
  <div class="stats rv" style="margin-top:48px">
{stats(STATS_CP_SEED)}
  </div>
</div></section>

<section class="sec divider"><div class="wrap">
  <div class="split rev">
    <div class="txt rv">
      <p class="eyebrow">{T('home.market.eyebrow')}</p>
      <h2>{T('home.market.title')}</h2>
      <p>{T('home.market.text')}</p>
      <div class="row" style="margin-top:22px"><a class="btn" href="{p}plugins/">{T('home.market.browse')} {ARROW}</a></div>
    </div>
    <div class="rv">{clip(rel, "promo-fleet", T("home.market.clip"), live=False)}</div>
  </div>
</div></section>

<section class="sec divider"><div class="wrap">
  <div class="sec-head"><p class="eyebrow">{T('home.watch.eyebrow')}</p><h2>{T('home.watch.title')}</h2><p>{T('home.watch.text')}</p></div>
  <div class="eps">{eps}</div>
  <div class="row" style="margin-top:28px"><a class="btn" href="{p}videos/">{T('all_episodes')} {ARROW}</a></div>
</div></section>

<section class="sec divider"><div class="wrap">
  <div class="sec-head"><p class="eyebrow">{T('rules.eyebrow')}</p><h2>{T('rules.title')}</h2><p>{T('home.rules.text')}</p></div>
  <div class="grid-3">{principles}</div>
</div></section>

<section class="sec divider"><div class="wrap">
  <div class="sec-head"><p class="eyebrow">{T('home.blog.eyebrow')}</p><h2>{T('home.blog.title')}</h2><p>{T('home.blog.text')}</p></div>
  <div class="posts">{posts}</div>
  <div class="row" style="margin-top:28px"><a class="btn" href="{p}blog/">{T('home.blog.all')} {ARROW}</a></div>
</div></section>

<section class="sec-tight divider"><div class="wrap">
  <div class="sec-head"><p class="eyebrow">{T('home.lic.eyebrow')}</p><h2>{T('home.lic.title')}</h2></div>
  <div class="lic">
    <div class="tile a"><span class="k">{T('lic.a.k')}</span><h3>{T('home.lic.a.title')}</h3><p>{T('home.lic.a.text')}</p></div>
    <div class="tile b"><span class="k">{T('lic.b.k')}</span><h3>{T('home.lic.b.title')}</h3><p>{T('home.lic.b.text', repo=REPO)}</p></div>
  </div>
</div></section>
{cta(rel)}
'''
    return head(T("home.meta.title"), T("home.meta.desc"), rel, "") + nav(rel, "home", "") + body + footer(rel)

def page_feature(i):
    f = CTX.features[i]; rel = rel_for(2)
    prev = CTX.features[i - 1] if i > 0 else None
    nxt = CTX.features[i + 1] if i + 1 < len(CTX.features) else None
    ep = T("ep.label", f["n"])
    beats = []
    for j, (k, t, d, pts, c, url) in enumerate(f["beats"]):
        lis = "".join(f"<li>{E(p)}</li>" for p in pts)
        beats.append(f'''<section class="beat"><div class="wrap"><div class="split{' rev' if j % 2 else ''}">
<div class="txt rv"><span class="k">0{j+1} · {k}</span><h3 class="big">{E(t)}</h3><p>{E(d)}</p><ul>{lis}</ul></div>
<div class="rv">{clip(rel, c, url)}</div></div></div></section>''')
    if f.get("soon"):
        player = f'''<div class="player rv">{clip(rel, "knowledge-arch", f"{ep} · {f['name']} · {T('feature.silent')}", live=False)}<div class="player-meta"><span>{ep} · {f["name"]} · {T('feature.soon_meta')}</span><a href="../../videos/">{T('all_episodes')} &rarr;</a></div></div>'''
    else:
        player = f'''<div class="player rv"><div class="window"><div class="bar"><i></i><i></i><i></i><span class="url">{ep} · {f["name"]}</span></div><video controls playsinline preload="none" poster="{rel}assets/posters/{f["ep"]}.jpg"><source src="{vid(rel, "swarmery-" + f["ep"] + ".mp4")}" type="video/mp4"></video></div>
<div class="player-meta"><span>{ep} · {f["name"]} · {f["dur"]} · {T('feature.quality')}</span><a href="{vid(rel, "swarmery-" + f["ep"] + ".mp4")}" download>{T('dialog.download')}</a></div></div>'''
    pager = '<div class="pager">'
    pager += f'<a href="../{prev["slug"]}/"><span class="d">&larr; {T("pager.prev")} · 0{prev["n"]}</span><b>{prev["name"]}</b></a>' if prev else f'<a href="../"><span class="d">&larr; {T("pager.overview")}</span><b>{T("pager.all")}</b></a>'
    pager += f'<a class="next" href="../{nxt["slug"]}/"><span class="d">{T("pager.next")} · 0{nxt["n"]} &rarr;</span><b>{nxt["name"]}</b></a>' if nxt else f'<a class="next" href="../../install/"><span class="d">{T("pager.next")} &rarr;</span><b>{T("nav.install")}</b></a>'
    pager += '</div>'
    body = f'''
<section class="fhero"><div class="hexbg"></div><div class="wrap">
  <div class="crumbs"><a href="../">{T('nav.features')}</a><span>/</span><span>0{f["n"]} {f["name"]}</span></div>
  <p class="eyebrow">{E(f["kicker"])}</p>
  <h1>{f["title"]}</h1>
  <p class="lede">{E(f["lede"])}</p>
  <div class="row"><a class="btn btn-primary" href="{pg(rel)}install/">{T('cta.start')} {ARROW}</a><a class="btn" href="{REPO}">{GH} {T('feature.view_gh')}</a></div>
  {player}
</div></section>
{"".join(beats)}
<section class="sec divider"><div class="wrap">
  <div class="sec-head"><p class="eyebrow">{T('feature.shots.eyebrow')}</p><h2>{T('feature.shots.title', name=f["name"].lower())}</h2><p>{T('feature.shots.text')}</p></div>
  <div class="gallery">{shots(rel, f["shots"], f["name"])}</div>
</div></section>
<section class="sec-tight divider"><div class="wrap">{pager}</div></section>
{cta(rel)}
'''
    title = f"{f['name']} · Swarmery"
    path = f"features/{f['slug']}/"
    return head(title, f["lede"], rel, path) + nav(rel, "features", path) + body + footer(rel)

def page_features():
    rel = rel_for(1)
    cards = []
    for f in CTX.features:
        b = f["beats"][0]
        cards.append(f'''<a class="card rv span-3" href="{f["slug"]}/"><div class="media"><video data-clip muted loop playsinline preload="none" poster="{rel}assets/clips/{b[4]}.jpg" data-src="{rel}assets/clips/{b[4]}.mp4" aria-hidden="true"></video></div><div class="body"><span class="k">0{f["n"]} · {f["name"]} · {T("features.card.episode")} {f["dur"]}</span><h3>{E(f["kicker"])}</h3><p>{E(f["lede"])}</p><span class="go">{T("explore", name=f["name"].lower())} {ARROW}</span></div></a>''')
    pages = "".join(f'<div><b>{t}</b><p>{E(d)}</p></div>' for t, d in CTX.cp_pages)
    principles = "".join(f'<div class="tile rv"><span class="k">{k}</span><h3>{t}</h3><p>{d}</p></div>' for k, t, d in CTX.principles)
    body = f'''
<section class="fhero"><div class="hexbg"></div><div class="wrap">
  <p class="eyebrow">{T('features.eyebrow')}</p>
  <h1>{T('features.title')}</h1>
  <p class="lede">{T('features.lede')}</p>
</div></section>
<section class="sec-tight"><div class="wrap"><div class="bento">{"".join(cards)}</div></div></section>
<section class="sec divider"><div class="wrap">
  <div class="sec-head"><p class="eyebrow">{T('features.dash.eyebrow')}</p><h2>{T('features.dash.title')}</h2><p>{T('features.dash.text')}</p></div>
  <div class="pages rv">{pages}</div>
  <div class="gallery" style="margin-top:28px">{shots(rel, ["00-overview-1-today", "00-overview-2-sessions"], T("features.overview"))}</div>
</div></section>
<section class="sec divider"><div class="wrap">
  <div class="sec-head"><p class="eyebrow">{T('rules.eyebrow')}</p><h2>{T('rules.title')}</h2></div>
  <div class="grid-3">{principles}</div>
</div></section>
{cta(rel)}
'''
    return head(f"{T('nav.features')} · Swarmery", T("features.meta.desc"), rel, "features/") + nav(rel, "features", "features/") + body + footer(rel)

def page_videos():
    rel = rel_for(1)
    p = CTX.promo
    voice = TE("videos.voice_note")
    voice = " " + voice if voice else ""  # "" in English: the en lede is unchanged
    feat = f'''<button class="ep feature rv" type="button" {promo_attrs(rel)}><div class="th"><img src="{rel}assets/posters/promo.jpg" alt="" loading="lazy"><div class="play"><span>{PLAY}</span></div><span class="dur">{p["dur"]}</span></div>
<div class="meta"><div class="k">{T('videos.start')}</div><h3>{p["title"]}</h3><p>{E(p["text"])}</p></div></button>'''
    eps = "".join(ep_card(rel, f) for f in CTX.features)
    chapters = []
    for f in CTX.features:
        items = "".join(f'<li><button type="button" {ep_attrs(rel, f)} data-t="{t}"><span class="t">{t}</span><span>{E(c)}</span></button></li>' for t, c in f["chapters"]) if not f.get("soon") else "".join(f'<li><button type="button" disabled><span class="t">{t}</span><span>{E(c)}</span></button></li>' for t, c in f["chapters"])
        chapters.append(f'<div class="tile rv"><span class="k">{T("ep.label", f["n"])} · {f["dur"]}</span><h3><a href="{pg(rel)}features/{f["slug"]}/" style="text-decoration:none">{f["name"]}</a></h3><ul class="chapters">{items}</ul></div>')
    body = f'''
<section class="fhero"><div class="hexbg"></div><div class="wrap">
  <p class="eyebrow">{T('videos.eyebrow')}</p>
  <h1>{T('videos.title')}</h1>
  <p class="lede">{T('videos.lede')}{voice}</p>
</div></section>
<section class="sec-tight"><div class="wrap"><div class="eps">{feat}{eps}</div></div></section>
<section class="sec divider"><div class="wrap">
  <div class="sec-head"><p class="eyebrow">{T('videos.ch.eyebrow')}</p><h2>{T('videos.ch.title')}</h2><p>{T('videos.ch.text')}</p></div>
  <div class="grid-3">{"".join(chapters)}</div>
</div></section>
{cta(rel)}
'''
    return head(f"{T('nav.videos')} · Swarmery", T("videos.meta.desc"), rel, "videos/") + nav(rel, "videos", "videos/") + body + footer(rel)

def page_blog():
    rel = rel_for(1)
    lead = post_card(rel, CTX.posts[0], lead=True)
    rest = "".join(post_card(rel, p) for p in CTX.posts[1:])
    # the series is picked by the data.py tag, so a translated tag does not drop it
    origin = [p for p, src in zip(CTX.posts, POSTS) if src[5].startswith("origin")][::-1]
    series = "".join(f'<a href="{p[3]}"><span class="pt">{E(p[5])}</span><b>{E(p[1].split(": ", 1)[-1])}</b></a>' for p in origin)
    body = f'''
<section class="fhero"><div class="hexbg"></div><div class="wrap">
  <p class="eyebrow">{T('blog.eyebrow')}</p>
  <h1>{T('blog.title')}</h1>
  <p class="lede">{T('blog.lede')}</p>
  <div class="row"><a class="btn btn-primary" href="{SUBSTACK}/subscribe">{T('blog.subscribe')} {ARROW}</a><a class="btn" href="{X}">{T('blog.follow')}</a></div>
</div></section>
<section class="sec-tight"><div class="wrap"><div class="posts">{lead}</div></div></section>
<section class="sec-tight divider"><div class="wrap">
  <div class="sec-head"><p class="eyebrow">{T('blog.series.eyebrow')}</p><h2>{T('blog.series.title')}</h2><p>{T('blog.series.text')}</p></div>
  <div class="series rv">{series}</div>
</div></section>
<section class="sec-tight divider"><div class="wrap"><div class="sec-head"><p class="eyebrow">{T('blog.all.eyebrow')}</p><h2>{T('blog.all.title')}</h2></div><div class="posts">{rest}</div></div></section>
{cta(rel, T("blog.cta.title"), T("blog.cta.text"))}
'''
    return head(f"{T('nav.blog')} · Swarmery", T("blog.meta.desc"), rel, "blog/") + nav(rel, "blog", "blog/") + body + footer(rel)

def group_label(g):
    return T("plugins.group." + g.replace(" ", "_"))

def page_plugins(market):
    rel = rel_for(1)
    packs = [p for p in market["plugins"] if p["name"] != "core"]
    core = [p for p in market["plugins"] if p["name"] == "core"][0]
    groups = ["all", "domain", "code intelligence", "workflow", "agent engineering"]
    filt = "".join(f'<button type="button" data-g="{g}" aria-pressed="{str(g == "all").lower()}">{group_label(g).capitalize() if g != "all" else group_label(g)}</button>' for g in groups)
    cards = []
    for p in packs:
        g = PACK_GROUPS.get(p["name"], "domain")
        cmd = f'/plugin install {p["name"]}@swarmery'
        cards.append(f'''<div class="pack rv" data-g="{g}"><div class="top"><span class="name">{p["name"]}</span><span class="grp">{group_label(g)}</span></div><p>{E(p["description"])}</p><div class="inst"><span>{cmd}</span><button type="button" data-copy="{cmd}" hidden>{T("plugins.copy")}</button></div></div>''')
    inside = "".join(f'<li><button type="button" disabled><span class="t">{T(f"plugins.inside.{k}.t")}</span><span>{T(f"plugins.inside.{k}.d")}</span></button></li>' for k in ("agents", "skills", "hooks", "cli"))
    grad = "".join(f'''
    <div class="tile rv"><span class="k">{T(f"plugins.grad.{i}.k")}</span><h3>{T(f"plugins.grad.{i}.title")}</h3><p>{T(f"plugins.grad.{i}.text")}</p></div>''' for i in (1, 2, 3))
    body = f'''
<section class="fhero"><div class="hexbg"></div><div class="wrap">
  <p class="eyebrow">{T('plugins.eyebrow')}</p>
  <h1>{T('plugins.title')}</h1>
  <p class="lede">{T('plugins.lede')}</p>
  <div style="max-width:620px">{codeblock("/plugin marketplace add atretyak1985/swarmery")}</div>
</div></section>
<section class="sec-tight"><div class="wrap">
  <div class="core-card rv">
    <div><p class="eyebrow">core@swarmery</p><h2>{T('plugins.core.title')}</h2><p>{E(core["description"])}</p>{codeblock("/plugin install core@swarmery")}</div>
    <div class="tile" style="align-self:center"><span class="k">{T('plugins.inside')}</span><ul class="chapters" style="margin:0">{inside}</ul></div>
  </div>
</div></section>
<section class="sec-tight"><div class="wrap">
  <div class="sec-head"><p class="eyebrow">{T('plugins.packs.eyebrow')}</p><h2>{T('plugins.packs.title')}</h2></div>
  <div class="filters" role="group" aria-label="{TE('plugins.filter_aria')}">{filt}</div>
  <div class="packs">{"".join(cards)}</div>
</div></section>
<section class="sec divider"><div class="wrap">
  <div class="sec-head"><p class="eyebrow">{T('plugins.grad.eyebrow')}</p><h2>{T('plugins.grad.title')}</h2><p>{T('plugins.grad.text')}</p></div>
  <div class="grid-3">{grad}
  </div>
</div></section>
{cta(rel)}
'''
    return head(f"{T('nav.plugins')} · Swarmery", T("plugins.meta.desc"), rel, "plugins/") + nav(rel, "plugins", "plugins/") + body + footer(rel)

def page_install():
    rel = rel_for(1)
    steps = "".join(f'''
  <div class="step rv"><span class="num"></span><div><h3>{T(f"install.s{i}.title")}</h3><p>{T(f"install.s{i}.text")}</p>{codeblock(cmd)}</div></div>''' for i, cmd in ((1, INSTALL_1), (2, INSTALL_2), (3, INSTALL_3)))
    extras = "".join(f'''
    <div class="tile rv"><span class="k">{T(f"install.extras.{i}.k")}</span><h3>{T(f"install.extras.{i}.title")}</h3><p>{T(f"install.extras.{i}.text")}</p></div>''' for i in (1, 2, 3))
    body = f'''
<section class="fhero"><div class="hexbg"></div><div class="wrap">
  <p class="eyebrow">{T('install.eyebrow')}</p>
  <h1>{T('install.title')}</h1>
  <p class="lede">{T('install.lede')}</p>
</div></section>
<section class="sec-tight"><div class="wrap"><div class="steps">{steps}
</div></div></section>
<section class="sec divider"><div class="wrap">
  <div class="split">
    <div class="txt rv"><p class="eyebrow">{T('install.open.eyebrow')}</p><h2>127.0.0.1:7777</h2><p>{T('install.open.text')}</p>
    <ul><li>{T('install.open.li1')}</li><li>{T('install.open.li2')}</li><li>{T('install.open.li3')}</li></ul>
    <div class="row" style="margin-top:22px"><a class="btn" href="{REPO}/tree/main/docs">{T('install.open.docs')} {ARROW}</a><a class="btn" href="../videos/">{T('install.open.watch')}</a></div></div>
    <div class="rv">{clip(rel, "sessions-list", "127.0.0.1:7777/sessions")}</div>
  </div>
</div></section>
<section class="sec divider"><div class="wrap">
  <div class="sec-head"><p class="eyebrow">{T('install.extras.eyebrow')}</p><h2>{T('install.extras.title')}</h2></div>
  <div class="grid-3">{extras}
  </div>
</div></section>
<section class="sec-tight divider"><div class="wrap">
  <div class="lic">
    <div class="tile a"><span class="k">{T('lic.a.k')}</span><h3>{T('install.lic.a.title')}</h3><p>{T('install.lic.a.text')}</p></div>
    <div class="tile b"><span class="k">{T('lic.b.k')}</span><h3>{T('install.lic.b.title')}</h3><p>{T('install.lic.b.text', repo=REPO)}</p></div>
  </div>
</div></section>
{cta(rel, T("install.cta.title"), T("install.cta.text"))}
'''
    return head(f"{T('nav.install')} · Swarmery", T("install.meta.desc"), rel, "install/") + nav(rel, "install", "install/") + body + footer(rel)

def page_404():
    # English only (GitHub Pages serves one root 404.html); its language switch and the
    # "Українська версія" line lead to uk/. Links are absolute: Pages serves this page at
    # whatever depth the missing URL had, so a relative "uk/" would 404 again.
    rel = "/swarmery/"
    body = f'''<section class="fhero" style="min-height:60vh"><div class="hexbg"></div><div class="wrap"><p class="eyebrow">404</p><h1>{T('404.title')}</h1><p class="lede">{T('404.lede')}</p><div class="row"><a class="btn btn-primary" href="{rel}">{T('404.home')} {ARROW}</a><a class="btn" href="{rel}features/">{T('nav.features')}</a></div><p class="muted"><a href="{rel}uk/" hreflang="uk" lang="uk">Українська версія</a></p></div></section>'''
    return head(f"{T('404.meta.title')} · Swarmery", T("404.meta.desc"), rel, "404.html", alternates=False) + nav(rel, "", "") + body + footer(rel)

def write(path, s):
    p = os.path.join(OUT, path); os.makedirs(os.path.dirname(p) or ".", exist_ok=True)
    open(p, "w", encoding="utf-8").write(s)

def keep_counters(idx):
    # keep the counters apply-counts.sh last wrote, so a rebuild never rolls them back
    KEPT.clear()
    if os.path.exists(idx):
        cur = open(idx, encoding="utf-8").read()
        for name in ("stats-hero", "stats-control-plane"):
            m = re.search(r"[ ]*<!-- BEGIN generated:%s -->.*?<!-- END generated:%s -->" % (name, name), cur, re.S)
            if m: KEPT[name] = m.group(0)

if __name__ == "__main__":
    here = os.path.dirname(os.path.abspath(__file__))
    repo = os.path.abspath(os.path.join(here, "..", ".."))
    if not ARGS: OUT = os.path.join(repo, "site")
    CAPTIONS.update(SHOT_CAPTIONS)
    market = json.load(open(os.path.join(repo, ".claude-plugin", "marketplace.json")))
    for lang in LANGS:
        set_lang(lang)
        keep_counters(os.path.join(OUT, CTX.prefix, "index.html"))
        write(CTX.prefix + "index.html", page_home())
        write(CTX.prefix + "features/index.html", page_features())
        for i, f in enumerate(CTX.features): write(CTX.prefix + f"features/{f['slug']}/index.html", page_feature(i))
        write(CTX.prefix + "videos/index.html", page_videos())
        write(CTX.prefix + "blog/index.html", page_blog())
        write(CTX.prefix + "plugins/index.html", page_plugins(market))
        write(CTX.prefix + "install/index.html", page_install())
        if lang == "en": write("404.html", page_404())
    print("built", OUT, "·", ", ".join(LANGS), "· run scripts/docgen/apply-counts.sh to refresh the counters", flush=True)
    if MISSING:
        langs = sorted({k.split(":", 1)[0] for k in MISSING})
        if STRICT:
            print("\n".join(MISSING), file=sys.stderr)
            die(f"--strict: {len(MISSING)} untranslated or unknown key(s) in {', '.join(langs)}")
        print(f"warning: {len(MISSING)} string(s) fall back to English ({', '.join(langs)}); run with --strict to list them", file=sys.stderr)
