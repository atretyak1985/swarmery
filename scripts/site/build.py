#!/usr/bin/env python3
"""Builds the static swarmery site (site/) from data.py. No dependencies.

Usage: python3 scripts/site/build.py [outdir]
Pages are generated; site/assets/site.css and site.js are edited by hand.
Short clips, posters and screenshots under site/assets/ are made by scripts/site/media.sh.
"""
import json, os, re, sys, html
from data import *

OUT = sys.argv[1] if len(sys.argv) > 1 else "out"
BASE = "https://atretyak1985.github.io/swarmery/"
E = html.escape

HEX = '<svg viewBox="0 0 32 32" aria-hidden="true"><path fill="currentColor" fill-rule="evenodd" d="M16 2.4 4.22 9.2v13.6L16 29.6l11.78-6.8V9.2ZM16 6.9 8.12 11.45v9.1L16 25.1l7.88-4.55v-9.1Z"/><path fill="currentColor" d="M16 11.5 12.1 13.75v4.5L16 20.5l3.9-2.25v-4.5Z"/></svg>'
GH = '<svg viewBox="0 0 16 16" aria-hidden="true"><path fill="currentColor" d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.013 8.013 0 0016 8c0-4.42-3.58-8-8-8z"/></svg>'
SUN = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" aria-hidden="true"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/></svg>'
MENU = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" aria-hidden="true"><path d="M4 7h16M4 12h16M4 17h16"/></svg>'
CHEV = '<svg viewBox="0 0 10 10" aria-hidden="true"><path d="M1.5 3.5 5 7l3.5-3.5" fill="none" stroke="currentColor" stroke-width="1.5"/></svg>'
PLAY = '<svg viewBox="0 0 20 20" aria-hidden="true"><path fill="currentColor" d="M5 3.5v13l11-6.5z"/></svg>'
ARROW = '<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" aria-hidden="true"><path d="M3 8h10M9 4l4 4-4 4"/></svg>'

def wordmark():
    return '<span>SW<span class="o">&#x2B21;</span>RMERY</span>'

def head(title, desc, rel, path):
    url = BASE + path
    return f'''<!doctype html>
<html lang="en" class="noscript">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="dark light">
<meta name="theme-color" content="#0A0B0D">
<title>{E(title)}</title>
<meta name="description" content="{E(desc)}">
<link rel="canonical" href="{url}">
<meta property="og:type" content="website">
<meta property="og:site_name" content="Swarmery">
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
<link rel="stylesheet" href="{rel}assets/site.css">
</head>
<body>
<a class="skip" href="#main">Skip to content</a>
'''

def nav(rel, cur):
    feats = "".join(f'<a href="{rel}features/{f["slug"]}/"><span class="n">0{f["n"]}</span><span><b>{f["name"]}</b><span>{E(f["menu"])}</span></span></a>' for f in FEATURES)
    def a(href, label, key):
        c = ' aria-current="page"' if cur == key else ''
        return f'<a href="{rel}{href}"{c}>{label}</a>'
    fcur = ' cur' if cur == 'features' else ''
    return f'''<header class="nav">
  <div class="wrap">
    <a class="brand" href="{rel}" aria-label="Swarmery home">{HEX}{wordmark()}</a>
    <nav class="menu" aria-label="Main">
      <div class="dd{fcur}"><button type="button" aria-expanded="false" aria-haspopup="true">Features {CHEV}</button>
        <div class="dd-panel">{feats}<a class="all" href="{rel}features/">All features and the six rules behind them &rarr;</a></div>
      </div>
      {a("videos/", "Videos", "videos")}
      {a("plugins/", "Plugins", "plugins")}
      {a("install/", "Get started", "install")}
      {a("blog/", "Blog", "blog")}
      <a href="{REPO}/tree/main/docs">Docs</a>
    </nav>
    <div class="nav-r">
      <button class="icon-btn" type="button" data-theme-toggle aria-label="Switch light or dark theme">{SUN}</button>
      <a class="gh" href="{REPO}">{GH}<span class="lbl">GitHub</span><span class="stars" data-stars></span></a>
      <button class="icon-btn burger" type="button" aria-label="Menu" aria-expanded="false">{MENU}</button>
    </div>
  </div>
</header>
<main id="main">
'''

def footer(rel):
    feats = "".join(f'<li><a href="{rel}features/{f["slug"]}/">{f["name"]}</a></li>' for f in FEATURES)
    return f'''</main>
<footer>
  <div class="wrap">
    <div class="foot">
      <div>
        <a class="brand" href="{rel}" aria-label="Swarmery home">{HEX}{wordmark()}</a>
        <p>A local-first control plane and plugin marketplace for Claude Code. Built in public from Lviv.</p>
      </div>
      <div><h4>Product</h4><ul>{feats}</ul></div>
      <div><h4>Learn</h4><ul>
        <li><a href="{rel}install/">Get started</a></li>
        <li><a href="{rel}videos/">Videos</a></li>
        <li><a href="{rel}plugins/">Plugins</a></li>
        <li><a href="{REPO}/tree/main/docs">Docs</a></li>
        <li><a href="{REPO}/blob/main/CHANGELOG.md">Changelog</a></li>
      </ul></div>
      <div><h4>Follow</h4><ul>
        <li><a href="{REPO}">GitHub</a></li>
        <li><a href="{rel}blog/">Blog</a></li>
        <li><a href="{SUBSTACK}">Substack</a></li>
        <li><a href="{X}">X · @SwarmeryDev</a></li>
      </ul></div>
    </div>
    <div class="foot-b">
      <span>Framework: Apache-2.0 · Control plane: PolyForm Noncommercial 1.0.0</span>
      <span>Not affiliated with Anthropic. Claude Code is a product of Anthropic.</span>
    </div>
  </div>
</footer>
<dialog class="modal" id="player" aria-label="Video player">
  <div class="modal-wrap">
    <button class="modal-close" type="button" aria-label="Close">&times;</button>
    <div class="modal-inner">
      <div class="modal-body"><video controls playsinline preload="none"></video><div class="side"></div></div>
      <div class="modal-foot"><span data-title></span><a data-dl class="muted" href="#" download>Download mp4</a></div>
    </div>
  </div>
</dialog>
<dialog class="modal" id="lightbox" aria-label="Screenshot">
  <div class="modal-wrap">
    <button class="modal-close" type="button" aria-label="Close">&times;</button>
    <div class="modal-inner"><img alt=""><div class="modal-foot"><span data-cap></span><span class="muted">Fictional data: TrailMap and Ledgerly API</span></div></div>
  </div>
</dialog>
<script src="{rel}assets/site.js" defer></script>
</body>
</html>
'''

def clip(rel, name, url, cls="", live=True):
    lv = '<span class="live">LIVE</span>' if live else ''
    return f'''<div class="frame {cls}">{lv}<video data-clip muted loop playsinline preload="none" poster="{rel}assets/clips/{name}.jpg" data-src="{rel}assets/clips/{name}.mp4" aria-label="{E(url)}"></video></div>'''

def ep_attrs(rel, f):
    ch = json.dumps(f["chapters"])
    return f'data-play="{rel}video/swarmery-{f["ep"]}.mp4" data-poster="{rel}assets/posters/{f["ep"]}.jpg" data-title="Episode {f["n"]} · {f["name"]} · {f["dur"]}" data-chapters=\'{E(ch, quote=True)}\''

def promo_attrs(rel):
    ch = json.dumps(PROMO["chapters"])
    return f'data-play="{rel}video/{PROMO["file"]}" data-poster="{rel}assets/posters/promo.jpg" data-title="{PROMO["title"]} · {PROMO["dur"]}" data-chapters=\'{E(ch, quote=True)}\''

def ep_card(rel, f, k=None):
    if f.get("soon"):
        return f'''<a class="ep" href="{rel}features/{f["slug"]}/"><div class="th"><img src="{rel}assets/posters/{f["ep"]}.jpg" alt="" loading="lazy"><span class="soon">Voiceover soon</span><div class="play"><span>{PLAY}</span></div></div>
<div class="meta"><div class="k">{k or "Episode %d" % f["n"]}</div><h3>{f["name"]}</h3><p>{E(f["kicker"])}. Silent preview on the feature page.</p></div></a>'''
    return f'''<button class="ep" type="button" {ep_attrs(rel, f)}><div class="th"><img src="{rel}assets/posters/{f["ep"]}.jpg" alt="" loading="lazy"><div class="play"><span>{PLAY}</span></div><span class="dur">{f["dur"]}</span></div>
<div class="meta"><div class="k">{k or "Episode %d" % f["n"]}</div><h3>{f["name"]}</h3><p>{E(f["kicker"])}.</p></div></button>'''

def post_card(rel, p, lead=False):
    d, t, sub, url, img, tag = p
    from datetime import date
    dd = date.fromisoformat(d).strftime("%b %-d, %Y")
    sp = f'<p>{E(sub)}</p>' if sub else ''
    return f'''<a class="post{' lead' if lead else ''}" href="{url}"><div class="cv"><img src="{cover(img, 1100 if lead else 720)}" alt="" loading="lazy" referrerpolicy="no-referrer" onerror="this.remove()"></div>
<div class="meta"><div class="d"><span class="s">{E(tag)}</span><span>{dd}</span></div><h3>{E(t)}</h3>{sp}<span class="go">Read on Substack &rarr;</span></div></a>'''

def shots(rel, names, k):
    out = []
    for n in names:
        cap = CAPTIONS.get(n, "")
        out.append(f'''<figure class="shot rv"><button type="button" data-zoom="{rel}assets/img/{n}-lg.webp" data-alt="{E(cap)}"><img src="{rel}assets/img/{n}.webp" srcset="{rel}assets/img/{n}-sm.webp 800w, {rel}assets/img/{n}.webp 1600w" sizes="(max-width:760px) 100vw, 600px" alt="{E(cap)}" loading="lazy" width="1600" height="900"></button><figcaption><b>{k}</b>{E(cap)}</figcaption></figure>''')
    return "\n".join(out)

def cta(rel, title="Run your agents like a fleet.", text="Free on GitHub. One binary on your machine, no cloud, no account. Your sessions show up the moment it starts."):
    return f'''<section class="cta"><div class="glow"></div><div class="wrap">
<h2>{title}</h2><p>{text}</p>
<div class="row"><a class="btn btn-primary" href="{rel}install/">Get started {ARROW}</a><a class="btn" href="{REPO}">{GH} Star on GitHub</a></div>
</div></section>'''

STATS_HERO = '''        <!-- BEGIN generated:stats-hero -->
        <div class="stat"><div class="n">14</div><div class="l">plugin packs</div></div>
        <div class="stat"><div class="n">26</div><div class="l">agents</div></div>
        <div class="stat"><div class="n">70</div><div class="l">skills</div></div>
        <div class="stat hot"><div class="n">:7777</div><div class="l">local control plane</div></div>
        <!-- END generated:stats-hero -->'''
STATS_CP = '''            <!-- BEGIN generated:stats-control-plane -->
            <div class="stat"><div class="n">101</div><div class="l">Go packages</div></div>
            <div class="stat"><div class="n">222</div><div class="l">REST routes</div></div>
            <div class="stat hot"><div class="n">:7777</div><div class="l">dashboard port</div></div>
            <div class="stat"><div class="n sm">127.0.0.1</div><div class="l">the only interface it binds</div></div>
            <!-- END generated:stats-control-plane -->'''

INSTALL_1 = "/plugin marketplace add atretyak1985/swarmery\n/plugin install core@swarmery"
INSTALL_2 = "bash scripts/init.sh"
INSTALL_3 = "curl -fsSL https://raw.githubusercontent.com/atretyak1985/swarmery/main/scripts/install.sh | bash\nswarmery serve"

def codeblock(txt):
    return f'<div class="codeblock"><pre><code>{E(txt)}</code></pre><button class="copy" type="button" data-copy="{E(txt)}" hidden>Copy</button></div>'

# ---------------------------------------------------------------- pages

def page_home():
    rel = ""
    reel_order = [("plan", FEATURES[0], 0), ("run", FEATURES[3], 1), ("decide", FEATURES[1], 0), ("measure", FEATURES[2], 0), ("learn", FEATURES[4], 0), ("remember", FEATURES[5], 1)]
    reel_lines = {
      "plan": "An interview turns your idea into a phased plan with criteria and forecasts.",
      "run": "Every session across every repo, live: chat, timeline, diffs.",
      "decide": "Approvals, questions and proposals wait in one queue, not in ten terminals.",
      "measure": "Scorecards, friction and cost tell you what to change next.",
      "learn": "Runs that miss their forecast become lessons you accept or reject.",
      "remember": "Memory, an architecture map and a code graph, so sessions do not start from zero.",
    }
    tabs = []
    for i, (k, f, bi) in enumerate(reel_order):
        b = f["beats"][bi]
        tabs.append(f'''<button class="reel-tab" type="button" role="tab" data-clip="assets/clips/{b[4]}.mp4" data-poster="assets/clips/{b[4]}.jpg" data-cap="{E(b[1])}" data-url="{E(b[5])}" data-href="features/{f["slug"]}/"><span class="k"><b>0{i+1}</b>{k}</span><h3>{f["name"]}</h3><p>{E(reel_lines[k])}</p><span class="prog"></span></button>''')
    first = reel_order[0][1]["beats"][0]
    bento = []
    spans = ["span-3", "span-3", "span-2", "span-2", "span-2", "span-6 wide"]
    for f, sp in zip(FEATURES, spans):
        b = f["beats"][0]
        bento.append(f'''<a class="card rv {sp}" href="features/{f["slug"]}/"><div class="media"><video data-clip muted loop playsinline preload="none" poster="assets/clips/{b[4]}.jpg" data-src="assets/clips/{b[4]}.mp4" aria-hidden="true"></video></div><div class="body"><span class="k">0{f["n"]} · {f["name"]}</span><h3>{E(f["kicker"])}</h3><p>{E(f["menu"])}.</p><span class="go">Explore {f["name"].lower()} {ARROW}</span></div></a>''')
    # the last bento card (knowledge) gets the side-by-side layout on wide screens via span-6; keep it simple
    eps = "".join(ep_card(rel, f) for f in FEATURES[:3])
    posts = "".join(post_card(rel, p) for p in POSTS[:3])
    principles = "".join(f'<div class="tile rv"><span class="k">{k}</span><h3>{t}</h3><p>{d}</p></div>' for k, t, d in PRINCIPLES)
    body = f'''
<section class="hero"><div class="hexbg"></div>
<div class="wrap">
  <div class="badge-row"><span class="pill"><span class="dot"></span>open source · local-first</span><span class="pill amber"><span class="dot"></span>new: feature episodes</span></div>
  <h1>Run your Claude Code agents <em>like a fleet</em>.</h1>
  <p class="lede">One local control plane for every Claude Code session: plan the work through an interview, run it in isolated worktrees, answer every waiting agent from one queue, and see what it cost. One Go binary, no cloud, no account.</p>
  <div class="row">
    <a class="btn btn-primary" href="install/">Get started {ARROW}</a>
    <button class="btn" type="button" {promo_attrs(rel)}>{PLAY} Watch the 75-second tour</button>
  </div>
  <div class="row" style="margin-top:18px"><div class="cmd"><span class="p">&gt;</span><code>/plugin marketplace add atretyak1985/swarmery</code><button class="copy" type="button" data-copy="/plugin marketplace add atretyak1985/swarmery" hidden>Copy</button></div></div>
  <div class="hero-shot" style="margin-top:64px">
    <div class="glow"></div>
    {clip(rel, "hero", "127.0.0.1:7777/sessions")}
    <div class="float-chip fc-1"><span class="ic">3</span><span>agents waiting on you<small>inbox · oldest 7m</small></span></div>
    <div class="float-chip fc-2"><span class="ic">$</span><span>cost before the bill<small>per session · project · model</small></span></div>
  </div>
  <div class="stats rv" style="margin-top:56px">
{STATS_HERO}
  </div>
</div></section>

<section class="sec-tight divider"><div class="wrap">
  <div class="pains">
    <div class="pain rv"><span class="tag">before</span><div class="q">Alt-tab. Copy. <s>Which agent is stuck?</s></div><p>Ten terminals, three repos, and a permission prompt you missed an hour ago. Swarmery shows every session live and puts every waiting decision in one queue.</p></div>
    <div class="pain rv"><span class="tag">before</span><div class="q">The agent said <s>"done"</s>.</div><p>A process exiting 0 is not a finished phase. Work is checked against its acceptance criteria from the outside, and the report says what actually shipped.</p></div>
    <div class="pain rv"><span class="tag">before</span><div class="q">Same mistake, <s>every week</s>.</div><p>Runs that miss their forecast become lessons with evidence. You accept them, they reach future runs, and they are re-measured until they stop helping.</p></div>
  </div>
</div></section>

<section class="sec divider" id="loop"><div class="wrap">
  <div class="sec-head"><p class="eyebrow">the loop</p><h2>Plan, run, decide, measure, learn. One loop, one dashboard.</h2><p>Every stage feeds the next, and amber always means something there is waiting on you. Click a stage, or let it play.</p></div>
  <div class="reel" data-reel-root>
    <div class="reel-tabs" role="tablist" aria-label="The loop">{"".join(tabs)}</div>
    <div class="reel-stage">
      <div class="frame"><span class="live">LIVE</span><video data-reel muted playsinline preload="none" aria-label="Product clip"></video></div>
      <div class="cap"><span data-reel-cap></span><a data-reel-link href="features/planning/">Open the feature &rarr;</a></div>
    </div>
  </div>
</div></section>

<section class="sec divider"><div class="wrap">
  <div class="sec-head"><p class="eyebrow">features</p><h2>Six pages that replace ten terminals.</h2><p>Each one has its own episode, real screenshots and short loops. Everything below is the actual dashboard running on fictional projects.</p></div>
  <div class="bento">{"".join(bento)}</div>
</div></section>

<section class="sec divider"><div class="wrap">
  <div class="split">
    <div class="txt rv">
      <p class="eyebrow">control plane</p>
      <h2>One binary that watches the whole swarm.</h2>
      <p>A single Go daemon with an embedded React dashboard. It reads the session transcripts Claude Code already writes, indexes them into local SQLite, and serves everything on one port that never leaves the loopback interface. Nothing to install in your projects.</p>
      <ul><li>Sessions you already ran show up at once</li><li>Approvals reach your phone, so a waiting run is unblocked away from the desk</li><li>launchd on macOS, systemd on Linux</li></ul>
    </div>
    <div class="term rv"><div class="bar"><i></i><i></i><i></i><span>~ zsh</span></div><pre><span class="p">$</span> swarmery serve
<span class="c">swarmery · single binary</span>
<span class="g">✓</span> backfilling sessions from ~/.claude/projects
<span class="g">✓</span> listening on <span class="b">127.0.0.1:7777</span>
<span class="g">✓</span> ready · nothing leaves this machine</pre></div>
  </div>
  <div class="stats rv" style="margin-top:48px">
{STATS_CP}
  </div>
</div></section>

<section class="sec divider"><div class="wrap">
  <div class="split rev">
    <div class="txt rv">
      <p class="eyebrow">plugin marketplace</p>
      <h2>One set of agents. Every project.</h2>
      <p>Stop copy-pasting agents between repos. A vendor-neutral core carries what is true for every project, opt-in packs add the vocabulary of one kind of work, and each project brings its own flavor at runtime through <code>project.json</code>.</p>
      <div class="row" style="margin-top:22px"><a class="btn" href="plugins/">Browse the packs {ARROW}</a></div>
    </div>
    <div class="rv">{clip(rel, "promo-fleet", "core@swarmery → every project", live=False)}</div>
  </div>
</div></section>

<section class="sec divider"><div class="wrap">
  <div class="sec-head"><p class="eyebrow">watch</p><h2>A series, one feature per episode.</h2><p>Under two minutes each, with chapters you can jump between.</p></div>
  <div class="eps">{eps}</div>
  <div class="row" style="margin-top:28px"><a class="btn" href="videos/">All episodes {ARROW}</a></div>
</div></section>

<section class="sec divider"><div class="wrap">
  <div class="sec-head"><p class="eyebrow">why it holds together</p><h2>Six rules, each enforced by something you can run.</h2><p>A scan, a gate, a worktree, a verdict. None of them is a convention you have to remember.</p></div>
  <div class="grid-3">{principles}</div>
</div></section>

<section class="sec divider"><div class="wrap">
  <div class="sec-head"><p class="eyebrow">build log</p><h2>Built in public.</h2><p>How it came to be, what broke, and what the numbers said. Written as it happens on Substack.</p></div>
  <div class="posts">{posts}</div>
  <div class="row" style="margin-top:28px"><a class="btn" href="blog/">All posts {ARROW}</a></div>
</div></section>

<section class="sec-tight divider"><div class="wrap">
  <div class="sec-head"><p class="eyebrow">license</p><h2>Two licenses, and honest about both.</h2></div>
  <div class="lic">
    <div class="tile a"><span class="k">Apache-2.0 · the framework</span><h3>Every agent pack, skill, script and overlay</h3><p>Readable, forkable and yours to build on anywhere, including inside a company's paid workflow, at no cost.</p></div>
    <div class="tile b"><span class="k">PolyForm Noncommercial · the control plane</span><h3>The Go binary under <code>tools/swarmery/</code></h3><p>Free for personal projects, learning, research and other open-source work. Commercial use is the one thing it does not grant: <a href="{REPO}/blob/main/tools/swarmery/LICENSE">read the terms</a> and get in touch.</p></div>
  </div>
</div></section>
{cta(rel)}
'''
    return head("Swarmery: run your Claude Code agents like a fleet", "A local-first control plane for Claude Code: plan by interview, run in worktrees, one queue for every waiting agent, cost and lessons from every run. One Go binary, no cloud.", rel, "") + nav(rel, "home") + body + footer(rel)

def page_feature(i):
    f = FEATURES[i]; rel = "../../"
    prev = FEATURES[i - 1] if i > 0 else None
    nxt = FEATURES[i + 1] if i + 1 < len(FEATURES) else None
    beats = []
    for j, (k, t, d, pts, c, url) in enumerate(f["beats"]):
        lis = "".join(f"<li>{E(p)}</li>" for p in pts)
        beats.append(f'''<section class="beat"><div class="wrap"><div class="split{' rev' if j % 2 else ''}">
<div class="txt rv"><span class="k">0{j+1} · {k}</span><h3 class="big">{E(t)}</h3><p>{E(d)}</p><ul>{lis}</ul></div>
<div class="rv">{clip(rel, c, url)}</div></div></div></section>''')
    if f.get("soon"):
        player = f'''<div class="player rv">{clip(rel, "knowledge-arch", "Episode 6 · Knowledge · silent preview", live=False)}<div class="player-meta"><span>Episode 6 · Knowledge · the voiced episode is on its way</span><a href="../../videos/">All episodes &rarr;</a></div></div>'''
    else:
        player = f'''<div class="player rv"><div class="window"><div class="bar"><i></i><i></i><i></i><span class="url">Episode {f["n"]} · {f["name"]}</span></div><video controls playsinline preload="none" poster="{rel}assets/posters/{f["ep"]}.jpg"><source src="{rel}video/swarmery-{f["ep"]}.mp4" type="video/mp4"></video></div>
<div class="player-meta"><span>Episode {f["n"]} · {f["name"]} · {f["dur"]} · 1080p with voiceover</span><a href="{rel}video/swarmery-{f["ep"]}.mp4" download>Download mp4</a></div></div>'''
    pager = '<div class="pager">'
    pager += f'<a href="../{prev["slug"]}/"><span class="d">&larr; Previous · 0{prev["n"]}</span><b>{prev["name"]}</b></a>' if prev else f'<a href="../"><span class="d">&larr; Overview</span><b>All features</b></a>'
    pager += f'<a class="next" href="../{nxt["slug"]}/"><span class="d">Next · 0{nxt["n"]} &rarr;</span><b>{nxt["name"]}</b></a>' if nxt else f'<a class="next" href="../../install/"><span class="d">Next &rarr;</span><b>Get started</b></a>'
    pager += '</div>'
    body = f'''
<section class="fhero"><div class="hexbg"></div><div class="wrap">
  <div class="crumbs"><a href="../">Features</a><span>/</span><span>0{f["n"]} {f["name"]}</span></div>
  <p class="eyebrow">{E(f["kicker"])}</p>
  <h1>{f["title"]}</h1>
  <p class="lede">{E(f["lede"])}</p>
  <div class="row"><a class="btn btn-primary" href="{rel}install/">Get started {ARROW}</a><a class="btn" href="{REPO}">{GH} View on GitHub</a></div>
  {player}
</div></section>
{"".join(beats)}
<section class="sec divider"><div class="wrap">
  <div class="sec-head"><p class="eyebrow">screenshots</p><h2>The {f["name"].lower()} pages, up close.</h2><p>Click any screenshot to open it full size. The projects are fictional.</p></div>
  <div class="gallery">{shots(rel, f["shots"], f["name"])}</div>
</div></section>
<section class="sec-tight divider"><div class="wrap">{pager}</div></section>
{cta(rel)}
'''
    title = f"{f['name']} · Swarmery"
    return head(title, f["lede"], rel, f"features/{f['slug']}/") + nav(rel, "features") + body + footer(rel)

def page_features():
    rel = "../"
    cards = []
    for f in FEATURES:
        b = f["beats"][0]
        cards.append(f'''<a class="card rv span-3" href="{f["slug"]}/"><div class="media"><video data-clip muted loop playsinline preload="none" poster="{rel}assets/clips/{b[4]}.jpg" data-src="{rel}assets/clips/{b[4]}.mp4" aria-hidden="true"></video></div><div class="body"><span class="k">0{f["n"]} · {f["name"]} · episode {f["dur"]}</span><h3>{E(f["kicker"])}</h3><p>{E(f["lede"])}</p><span class="go">Explore {f["name"].lower()} {ARROW}</span></div></a>''')
    pages = "".join(f'<div><b>{t}</b><p>{E(d)}</p></div>' for t, d in CP_PAGES)
    principles = "".join(f'<div class="tile rv"><span class="k">{k}</span><h3>{t}</h3><p>{d}</p></div>' for k, t, d in PRINCIPLES)
    body = f'''
<section class="fhero"><div class="hexbg"></div><div class="wrap">
  <p class="eyebrow">features</p>
  <h1>Everything your agents do, <em>on one screen</em>.</h1>
  <p class="lede">Six areas, one dashboard on <code>127.0.0.1:7777</code>. Each area has its own page with an episode, short loops and screenshots.</p>
</div></section>
<section class="sec-tight"><div class="wrap"><div class="bento">{"".join(cards)}</div></div></section>
<section class="sec divider"><div class="wrap">
  <div class="sec-head"><p class="eyebrow">the whole dashboard</p><h2>Nine pages in the sidebar.</h2><p>What each one is for, in one line.</p></div>
  <div class="pages rv">{pages}</div>
  <div class="gallery" style="margin-top:28px">{shots(rel, ["00-overview-1-today", "00-overview-2-sessions"], "Overview")}</div>
</div></section>
<section class="sec divider"><div class="wrap">
  <div class="sec-head"><p class="eyebrow">why it holds together</p><h2>Six rules, each enforced by something you can run.</h2></div>
  <div class="grid-3">{principles}</div>
</div></section>
{cta(rel)}
'''
    return head("Features · Swarmery", "Planning, Inbox, Health, Sessions, Learning and Knowledge: every page of the Swarmery control plane for Claude Code.", rel, "features/") + nav(rel, "features") + body + footer(rel)

def page_videos():
    rel = "../"
    feat = f'''<button class="ep feature rv" type="button" {promo_attrs(rel)}><div class="th"><img src="{rel}assets/posters/promo.jpg" alt="" loading="lazy"><div class="play"><span>{PLAY}</span></div><span class="dur">{PROMO["dur"]}</span></div>
<div class="meta"><div class="k">Start here · the tour</div><h3>{PROMO["title"]}</h3><p>{E(PROMO["text"])}</p></div></button>'''
    eps = "".join(ep_card(rel, f) for f in FEATURES)
    chapters = []
    for f in FEATURES:
        items = "".join(f'<li><button type="button" {ep_attrs(rel, f)} data-t="{t}"><span class="t">{t}</span><span>{E(c)}</span></button></li>' for t, c in f["chapters"]) if not f.get("soon") else "".join(f'<li><button type="button" disabled><span class="t">{t}</span><span>{E(c)}</span></button></li>' for t, c in f["chapters"])
        chapters.append(f'<div class="tile rv"><span class="k">Episode {f["n"]} · {f["dur"]}</span><h3><a href="{rel}features/{f["slug"]}/" style="text-decoration:none">{f["name"]}</a></h3><ul class="chapters">{items}</ul></div>')
    body = f'''
<section class="fhero"><div class="hexbg"></div><div class="wrap">
  <p class="eyebrow">videos</p>
  <h1>Watch it <em>work</em>.</h1>
  <p class="lede">A 75-second tour, then one episode per feature. Each is under two minutes, voiced, in 1080p, with chapters. New episodes land here first.</p>
</div></section>
<section class="sec-tight"><div class="wrap"><div class="eps">{feat}{eps}</div></div></section>
<section class="sec divider"><div class="wrap">
  <div class="sec-head"><p class="eyebrow">chapters</p><h2>Jump straight to the part you need.</h2><p>Every chapter opens the episode at that moment.</p></div>
  <div class="grid-3">{"".join(chapters)}</div>
</div></section>
{cta(rel)}
'''
    return head("Videos · Swarmery", "The Swarmery tour and the feature episodes: planning, inbox, health, sessions, learning and knowledge.", rel, "videos/") + nav(rel, "videos") + body + footer(rel)

def page_blog():
    rel = "../"
    lead = post_card(rel, POSTS[0], lead=True)
    rest = "".join(post_card(rel, p) for p in POSTS[1:])
    origin = [p for p in POSTS if p[5].startswith("origin")][::-1]
    series = "".join(f'<a href="{p[3]}"><span class="pt">{E(p[5])}</span><b>{E(p[1].split(": ", 1)[-1])}</b></a>' for p in origin)
    body = f'''
<section class="fhero"><div class="hexbg"></div><div class="wrap">
  <p class="eyebrow">blog</p>
  <h1>The build log.</h1>
  <p class="lede">Swarmery is built in public. These posts tell how it came to be, what broke, and what the numbers said, in plain words. They live on Substack, where you can subscribe.</p>
  <div class="row"><a class="btn btn-primary" href="{SUBSTACK}/subscribe">Subscribe on Substack {ARROW}</a><a class="btn" href="{X}">Follow @SwarmeryDev</a></div>
</div></section>
<section class="sec-tight"><div class="wrap"><div class="posts">{lead}</div></div></section>
<section class="sec-tight divider"><div class="wrap">
  <div class="sec-head"><p class="eyebrow">series</p><h2>How Swarmery came to be.</h2><p>Three parts: from a chat window to a control plane.</p></div>
  <div class="series rv">{series}</div>
</div></section>
<section class="sec-tight divider"><div class="wrap"><div class="sec-head"><p class="eyebrow">all posts</p><h2>Everything so far.</h2></div><div class="posts">{rest}</div></div></section>
{cta(rel, "Read it, then run it.", "Every post is about something you can try on your own machine.")}
'''
    return head("Blog · Swarmery", "The Swarmery build log: how it came to be, what broke, and what the numbers said.", rel, "blog/") + nav(rel, "blog") + body + footer(rel)

def page_plugins(market):
    rel = "../"
    packs = [p for p in market["plugins"] if p["name"] != "core"]
    core = [p for p in market["plugins"] if p["name"] == "core"][0]
    groups = ["all", "domain", "code intelligence", "workflow", "agent engineering"]
    filt = "".join(f'<button type="button" data-g="{g}" aria-pressed="{str(g == "all").lower()}">{g.capitalize() if g != "all" else "All packs"}</button>' for g in groups)
    cards = []
    for p in packs:
        g = PACK_GROUPS.get(p["name"], "domain")
        cmd = f'/plugin install {p["name"]}@swarmery'
        cards.append(f'''<div class="pack rv" data-g="{g}"><div class="top"><span class="name">{p["name"]}</span><span class="grp">{g}</span></div><p>{E(p["description"])}</p><div class="inst"><span>{cmd}</span><button type="button" data-copy="{cmd}" hidden>copy</button></div></div>''')
    body = f'''
<section class="fhero"><div class="hexbg"></div><div class="wrap">
  <p class="eyebrow">plugin marketplace</p>
  <h1>One core, <em>opt-in packs</em>.</h1>
  <p class="lede">A Claude Code plugin marketplace: the core carries everything that is true for every project, and domain packs add the vocabulary and tooling of one kind of work. A project enables only what it uses, and its own flavor arrives at runtime from <code>.claude/project.json</code>.</p>
  <div style="max-width:620px">{codeblock("/plugin marketplace add atretyak1985/swarmery")}</div>
</div></section>
<section class="sec-tight"><div class="wrap">
  <div class="core-card rv">
    <div><p class="eyebrow">core@swarmery</p><h2>The vendor-neutral core.</h2><p>{E(core["description"])}</p>{codeblock("/plugin install core@swarmery")}</div>
    <div class="tile" style="align-self:center"><span class="k">what is inside</span><ul class="chapters" style="margin:0"><li><button type="button" disabled><span class="t">agents</span><span>orchestration, planning, design, execution, review</span></button></li><li><button type="button" disabled><span class="t">skills</span><span>progressively disclosed, loaded when a task needs them</span></button></li><li><button type="button" disabled><span class="t">hooks</span><span>guard rails such as protecting sensitive files</span></button></li><li><button type="button" disabled><span class="t">cli</span><span>agent-work: the project-aware workspace for plans, sessions and tasks</span></button></li></ul></div>
  </div>
</div></section>
<section class="sec-tight"><div class="wrap">
  <div class="sec-head"><p class="eyebrow">packs</p><h2>Enable what the project needs.</h2></div>
  <div class="filters" role="group" aria-label="Filter packs">{filt}</div>
  <div class="packs">{"".join(cards)}</div>
</div></section>
<section class="sec divider"><div class="wrap">
  <div class="sec-head"><p class="eyebrow">graduation</p><h2>Flow goes up only.</h2><p>How a component ends up in a pack, and why nothing is ever copied back down into a project.</p></div>
  <div class="grid-3">
    <div class="tile rv"><span class="k">1 · born local</span><h3>In one project's <code>.claude/</code></h3><p>New agents and skills start where they are needed. A project-local component overrides the plugin's on a name collision: that is the override mechanism, not a fork.</p></div>
    <div class="tile rv"><span class="k">2 · a second project</span><h3>Promoted to a domain pack</h3><p>De-flavored, moved into the pack, the pack's version bumped, and the donor's local copy deleted.</p></div>
    <div class="tile rv"><span class="k">3 · every project</span><h3>Promoted to the core</h3><p>Only when every project needs it. A neutrality scan keeps brand names out of <code>plugins/</code>, and the count stays at zero.</p></div>
  </div>
</div></section>
{cta(rel)}
'''
    return head("Plugins · Swarmery", "The Swarmery Claude Code plugin marketplace: a vendor-neutral core and opt-in domain packs.", rel, "plugins/") + nav(rel, "plugins") + body + footer(rel)

def page_install():
    rel = "../"
    body = f'''
<section class="fhero"><div class="hexbg"></div><div class="wrap">
  <p class="eyebrow">get started</p>
  <h1>Three commands to <em>a running swarm</em>.</h1>
  <p class="lede">Nothing to sign up for and nothing to host. The marketplace installs into Claude Code, the bootstrap script writes a project overlay, and the control plane runs on your own machine.</p>
</div></section>
<section class="sec-tight"><div class="wrap"><div class="steps">
  <div class="step rv"><span class="num"></span><div><h3>Add the marketplace and the core</h3><p>Inside Claude Code. Domain packs are opt-in on top, see <a href="../plugins/">Plugins</a>.</p>{codeblock(INSTALL_1)}</div></div>
  <div class="step rv"><span class="num"></span><div><h3>Bootstrap a project</h3><p>From a clone of the repo, in the project you want to wire up: it writes <code>settings.json</code>, a <code>project.json</code> skeleton and the project's private workspace namespace.</p>{codeblock(INSTALL_2)}</div></div>
  <div class="step rv"><span class="num"></span><div><h3>Install and run the control plane</h3><p>The script fetches the release binary for your platform and verifies it against <code>SHA256SUMS</code>. The daemon listens on <code>127.0.0.1:7777</code>, and the sessions you already ran show up at once.</p>{codeblock(INSTALL_3)}</div></div>
</div></div></section>
<section class="sec divider"><div class="wrap">
  <div class="split">
    <div class="txt rv"><p class="eyebrow">then open</p><h2>127.0.0.1:7777</h2><p>Today shows what waits on you, what runs now, and this week's loop. Start a plan from <b>Plans → New plan</b>, or just watch your existing sessions arrive.</p>
    <ul><li>Binds only to the loopback interface</li><li>Data stays in local SQLite</li><li>Runs as a service: launchd on macOS, systemd <code>--user</code> on Linux</li></ul>
    <div class="row" style="margin-top:22px"><a class="btn" href="{REPO}/tree/main/docs">Read the docs {ARROW}</a><a class="btn" href="../videos/">Watch the episodes</a></div></div>
    <div class="rv">{clip(rel, "sessions-list", "127.0.0.1:7777/sessions")}</div>
  </div>
</div></section>
<section class="sec divider"><div class="wrap">
  <div class="sec-head"><p class="eyebrow">optional</p><h2>Extras.</h2></div>
  <div class="grid-3">
    <div class="tile rv"><span class="k">macOS companion</span><h3>Notch</h3><p>A small tab docked to the screen edge that expands into a side panel on its own when an operator is needed: a pending approval or a failed session. Native, zero dependencies.</p></div>
    <div class="tile rv"><span class="k">from source</span><h3>Build it yourself</h3><p>Go and React, one <code>make build</code>: docs snapshot, Vite bundle, <code>go:embed</code>, one binary. <code>make install</code> swaps the service-managed binary and restarts it.</p></div>
    <div class="tile rv"><span class="k">packs</span><h3>Add a domain pack</h3><p>UAV, IoT, web, infra, code graphs, Jira, PR review, design handoff and more. Enable only what a project uses.</p></div>
  </div>
</div></section>
<section class="sec-tight divider"><div class="wrap">
  <div class="lic">
    <div class="tile a"><span class="k">Apache-2.0 · the framework</span><h3>Plugins, skills, scripts, overlays</h3><p>Free to use and build on anywhere, including commercial work.</p></div>
    <div class="tile b"><span class="k">PolyForm Noncommercial · the control plane</span><h3>The Go binary</h3><p>Free for personal, learning, research and open-source use. <a href="{REPO}/blob/main/tools/swarmery/LICENSE">Read the terms</a> for anything commercial.</p></div>
  </div>
</div></section>
{cta(rel, "Stuck somewhere?", "Open an issue on GitHub. Questions and pull requests are welcome.")}
'''
    return head("Get started · Swarmery", "Install the Swarmery marketplace and control plane for Claude Code in three commands.", rel, "install/") + nav(rel, "install") + body + footer(rel)

def page_404():
    rel = "/swarmery/"
    body = f'''<section class="fhero" style="min-height:60vh"><div class="hexbg"></div><div class="wrap"><p class="eyebrow">404</p><h1>This agent went <em>off plan</em>.</h1><p class="lede">The page you asked for is not here. It may have moved when the site was rebuilt.</p><div class="row"><a class="btn btn-primary" href="{rel}">Home {ARROW}</a><a class="btn" href="{rel}features/">Features</a></div></div></section>'''
    return head("Not found · Swarmery", "Page not found.", rel, "404.html") + nav(rel, "") + body + footer(rel)

def write(path, s):
    p = os.path.join(OUT, path); os.makedirs(os.path.dirname(p) or ".", exist_ok=True)
    open(p, "w", encoding="utf-8").write(s)

if __name__ == "__main__":
    here = os.path.dirname(os.path.abspath(__file__))
    repo = os.path.abspath(os.path.join(here, "..", ".."))
    if len(sys.argv) <= 1: OUT = os.path.join(repo, "site")
    CAPTIONS.update(SHOT_CAPTIONS)
    market = json.load(open(os.path.join(repo, ".claude-plugin", "marketplace.json")))
    # keep the counters apply-counts.sh last wrote, so a rebuild never rolls them back
    idx = os.path.join(OUT, "index.html")
    if os.path.exists(idx):
        cur = open(idx, encoding="utf-8").read()
        for name, var in (("stats-hero", "STATS_HERO"), ("stats-control-plane", "STATS_CP")):
            m = re.search(r"[ ]*<!-- BEGIN generated:%s -->.*?<!-- END generated:%s -->" % (name, name), cur, re.S)
            if m: globals()[var] = m.group(0)
    write("index.html", page_home())
    write("features/index.html", page_features())
    for i, f in enumerate(FEATURES): write(f"features/{f['slug']}/index.html", page_feature(i))
    write("videos/index.html", page_videos())
    write("blog/index.html", page_blog())
    write("plugins/index.html", page_plugins(market))
    write("install/index.html", page_install())
    write("404.html", page_404())
    print("built", OUT, "· run scripts/docgen/apply-counts.sh to refresh the counters")
