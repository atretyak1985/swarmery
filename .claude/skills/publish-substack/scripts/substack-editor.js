// Vendored unmodified from https://github.com/xbill9/publishing-kit
// (skills/publishing/scripts/browser/substack-editor.js at commit
// dfd02fff48f8db39d1a71ec995160bd6fddc7f21, 2026-10-05), Apache-2.0.
// "references/substack.md" below is that repository's file. Only this header
// was added; to pick up upstream fixes, replace everything under it.
//
// Substack editor helpers, for evaluation in the page on
// <pub>.substack.com/publish/post/<id>.
//
// MEASURED 2026-10-05 pasting this kit's own article. Why each helper exists is in
// references/substack.md. Short version: the body is a tiptap/ProseMirror editor
// that takes a synthetic HTML paste, but its `code` mark excludes every other
// mark, so a link around inline code is dropped with no error; and the stored
// draft is readable as JSON from the same origin, so audit that, not the DOM.
//
//   (on 127.0.0.1, same tab)  window.name = await (await fetch(location.href)).text()
//   (navigate to the editor)
//   ss.prepare()                               -> {html, codeLinksUnwrapped, anchors}
//   await ss.paste()                           -> {pasted:true, chars}
//   ss.setTitle(TITLE, SUBTITLE)               -> {title, subtitle}
//   await ss.audit()                           -> counts from the SAVED draft
//   await ss.clear(<draft id>)                 only to replace a body you pasted
//
// Publishing, ONLY when the author said to publish in this session and chose
// whether subscribers get the email:
//   await ss.openPublish()                     -> {open:true, settings}  read them out
//   await ss.publish({email:true, audience:"everyone"})   refuses on any mismatch
//   await ss.published(<draft id>)             -> {slug, url, email_sent_at}
// Updating a published post: clear, prepare, paste and audit as above, then
//   await ss.openPublish()                     -> settings.button "Update now", email null
//   await ss.publish({update: true})           no email is sent; email_sent_at unchanged
//
// Every helper refuses ({refused: ...}) instead of acting when its precondition
// fails; a guard inside one script is a real guard, unlike one inside a batch.

window.ss = (() => {
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
  const editor = () => document.querySelector('.ProseMirror[contenteditable="true"]');
  const draftId = () => (location.pathname.match(/\/publish\/post\/(\d+)/) || [])[1] || null;
  let prepared = null;

  // window.name holds the -hosted.html from make-medium.py. Title and subtitle are
  // their own fields, so the header goes. <a><code>x</code></a> becomes <a>x</a>:
  // the schema's code mark has excludes "_", and the paste keeps the code and
  // drops the link. make-medium.py demotes sections to <h4> because Medium has two
  // heading sizes; Substack has six, and h4 is 21px against 19px body text, so
  // sections go back up to `sectionHeading`.
  function prepare(src = window.name, { sectionHeading = "h3" } = {}) {
    if (!src || src.length < 200) return { refused: "window.name is empty; load the hosted HTML from 127.0.0.1 in this tab first" };
    const doc = new DOMParser().parseFromString(src, "text/html");
    doc.querySelectorAll("header, h1.title, style, script, link, meta").forEach((e) => e.remove());
    let codeLinksUnwrapped = 0;
    for (const a of doc.querySelectorAll("a[href]"))
      for (const c of a.querySelectorAll("code")) { c.replaceWith(...c.childNodes); codeLinksUnwrapped++; }
    for (const h of doc.querySelectorAll("h4")) {
      const n = doc.createElement(sectionHeading);
      n.append(...h.childNodes); h.replaceWith(n);
    }
    // MEASURED 2026-10-05: <pre class="text"> (a ```text fence) pastes as a
    // latex_block, Substack's maths element, while shell/js/json fences paste as
    // code_block. The saved code_block keeps no language, so drop the classes.
    let preClassesCleared = 0;
    for (const e of doc.querySelectorAll("pre, pre code"))
      if (e.hasAttribute("class")) { e.removeAttribute("class"); if (e.tagName === "PRE") preClassesCleared++; }
    const anchors = [...doc.querySelectorAll("a[href]")].filter((a) => /^https?:/.test(a.getAttribute("href"))).length;
    prepared = { html: doc.body.innerHTML, text: doc.body.innerText };
    return { html: prepared.html.length, codeLinksUnwrapped, preClassesCleared, anchors, images: doc.querySelectorAll("img").length, pres: doc.querySelectorAll("pre").length };
  }

  async function paste() {
    const ed = editor();
    if (!ed) return { refused: "no editor; open <pub>.substack.com/publish/post/<id>" };
    if (!prepared) return { refused: "call ss.prepare() first" };
    if (ed.innerText.trim().length > 0) return { refused: `editor holds ${ed.innerText.trim().length} chars; refusing to paste on top (ss.clear(<id>) first)` };
    ed.focus();
    const dt = new DataTransfer();
    dt.setData("text/html", prepared.html);
    dt.setData("text/plain", prepared.text);
    // dispatchEvent returns false here: that is preventDefault, not refusal.
    ed.dispatchEvent(new ClipboardEvent("paste", { clipboardData: dt, bubbles: true, cancelable: true }));
    await sleep(3000);
    return { pasted: true, chars: ed.innerText.length };
  }

  // The post's Title and subtitle are textareas in the editor, placeholders "Title"
  // and "Add a subtitle…". The "Add a title..." input and "Add a description..."
  // textarea are the SEO settings, a different pair.
  // MEASURED 2026-10-05: a 256-character subtitle is refused ("Draft not saved:
  // Subtitle is too long") and NOTHING saves, body included; 255 saves. The
  // banner stays up after a later save succeeds, so judge by ss.audit().
  const SUBTITLE_MAX = 255;
  function setTitle(title, subtitle) {
    if (subtitle != null && subtitle.length > SUBTITLE_MAX)
      return { refused: `subtitle is ${subtitle.length} chars; Substack saves at most ${SUBTITLE_MAX}, and refuses the whole draft above that` };
    const t = [...document.querySelectorAll("textarea")].find((e) => e.placeholder === "Title");
    const s = [...document.querySelectorAll("textarea")].find((e) => /^Add a subtitle/.test(e.placeholder));
    if (!t || !s) return { refused: "post title/subtitle textareas not found" };
    const set = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value").set;
    for (const [el, v] of [[t, title], [s, subtitle]]) {
      if (v == null) continue;
      el.focus(); set.call(el, v);
      el.dispatchEvent(new Event("input", { bubbles: true }));
      el.dispatchEvent(new Event("change", { bubbles: true }));
      el.blur();
    }
    return { title: t.value, subtitle: s.value };
  }

  // Read the draft as Substack stored it. Wait for the autosave first.
  async function audit(id = draftId()) {
    if (!id) return { refused: "no draft id in the URL" };
    await sleep(3000);
    const r = await fetch(`/api/v1/drafts/${id}`, { credentials: "include" });
    if (!r.ok) return { refused: `GET /api/v1/drafts/${id} -> ${r.status}` };
    const j = await r.json();
    const body = JSON.parse(j.draft_body || '{"type":"doc"}');
    const types = {}, links = new Set(), codeBlocks = [], images = [];
    const text = (n) => n.text || (n.content || []).map(text).join("");
    const walk = (n) => {
      types[n.type] = (types[n.type] || 0) + 1;
      for (const m of n.marks || []) { types["mark:" + m.type] = (types["mark:" + m.type] || 0) + 1; if (m.type === "link") links.add(m.attrs.href); }
      if (n.type === "code_block") codeBlocks.push(text(n).split("\n").length);
      if (n.type === "heading") types["h" + n.attrs.level] = (types["h" + n.attrs.level] || 0) + 1;
      if (n.type === "image2") images.push({ rehosted: /substack-post-media/.test(n.attrs.src), alt: n.attrs.alt, w: n.attrs.width });
      (n.content || []).forEach(walk);
    };
    walk(body);
    return {
      id, title: j.draft_title, subtitle: j.draft_subtitle, updated: j.draft_updated_at,
      types, links: [...links], codeBlockLines: codeBlocks, images,
      first: text(body.content?.[0] || {}).slice(0, 80), last: text(body.content?.at(-1) || {}).slice(0, 80),
    };
  }

  // Replacing a body: clear through the editor's own command, never by pasting
  // over a selection. Takes the draft id so it cannot clear the wrong draft.
  async function clear(id) {
    if (!id || String(id) !== draftId()) return { refused: `pass this draft's id (${draftId()}) to confirm` };
    const ed = editor();
    if (!ed?.editor) return { refused: "no tiptap editor instance on the ProseMirror element" };
    ed.editor.commands.clearContent(true);
    await sleep(1500);
    return { cleared: true, chars: ed.innerText.trim().length };
  }

  // Continue opens a Publish modal on the same URL. Read it before anything else:
  // MEASURED 2026-10-05 its defaults are audience Everyone, comments Everyone,
  // "Send via email and the Substack app" ON, scheduling off, and the button
  // reads "Send to everyone now".
  const publishDialog = () => [...document.querySelectorAll("[role=dialog]")].find((d) => /^Publish\b/.test(d.innerText)) || null;
  const labelOf = (c) => ((c.closest("label") || c.parentElement?.parentElement)?.innerText || "").split("\n")[0].trim();
  function settings() {
    const d = publishDialog();
    if (!d) return null;
    const radio = (name) => d.querySelector(`input[name=${name}]:checked`)?.value || null;
    const box = (re) => [...d.querySelectorAll("input[type=checkbox]")].find((c) => re.test(labelOf(c)))?.checked ?? null;
    // Single-line labels only: the social-preview card is also a <button>, and its
    // text starts with the post title, which can itself start with "Publish".
    const send = [...d.querySelectorAll("button")].map((b) => b.innerText.trim())
      .find((t) => !t.includes("\n") && /^(Send|Publish|Update)\b/.test(t) && t !== "Publish") || null;
    return { audience: radio("audience"), comments: radio("commentLevel"), email: box(/^Send via email/), scheduled: box(/^Schedule time/), button: send };
  }

  async function openPublish() {
    if (publishDialog()) return { open: true, already: true, settings: settings() };
    // A published post whose draft differs from the live copy shows "Update"
    // where a draft shows "Continue" (MEASURED 2026-10-05).
    const b = [...document.querySelectorAll("button")].find((x) => ["Continue", "Update"].includes(x.innerText.trim()));
    if (!b) return { refused: "no Continue or Update button; not on a draft in the editor, or nothing changed since publishing" };
    b.click();
    for (let i = 0; i < 10 && !publishDialog(); i++) await sleep(500);
    return publishDialog() ? { open: true, settings: settings() } : { refused: "Continue did not open the Publish modal" };
  }

  // Publishing sends the email when `email` is on, and an email cannot be unsent,
  // so the caller states what it expects and the helper refuses on any mismatch.
  // Then a second modal, "Add subscribe buttons to your post", holds the publish
  // at "Publishing..." until answered; reading only the first dialog misses it.
  // `subscribeButtons: false` answers "Publish without buttons".
  // Returns before the page moves to /publish/posts/detail/<id>/share-center;
  // confirm with ss.published().
  async function publish(expect, { subscribeButtons = false } = {}) {
    const s = settings();
    if (!s) return { refused: "Publish modal not open; await ss.openPublish() and read its settings first" };
    // MEASURED 2026-10-05: updating a published post opens the same modal with no
    // Delivery section (settings().email is null) and "Update now"; email_sent_at
    // stayed unchanged, so an update does not send a second email.
    if (s.email === null && s.button === "Update now") {
      if (!expect?.update) return { refused: "this updates a published post; pass {update: true}", settings: s };
    } else if (!expect || typeof expect.email !== "boolean") return { refused: "pass {email: true|false, audience}; the email cannot be unsent", settings: s };
    for (const k of ["email", "audience", "comments", "scheduled"])
      if (k in expect && k !== "update" && expect[k] !== s[k]) return { refused: `${k} is ${s[k]}, expected ${expect[k]}; change it in the modal first`, settings: s };
    const go = [...publishDialog().querySelectorAll("button")].find((b) => b.innerText.trim() === s.button);
    if (!go) return { refused: "no send/publish button in the modal", settings: s };
    // An update moves the tab to share-center?alreadyPublished=true at once, which
    // kills a script still running in the page; click after returning.
    if (s.button === "Update now") { setTimeout(() => go.click(), 100); return { clicked: s.button, settings: s }; }
    go.click();
    const want = subscribeButtons ? "Add subscribe buttons" : "Publish without buttons";
    for (let i = 0; i < 20; i++) {
      await sleep(500);
      const b = [...document.querySelectorAll("button")].find((x) => x.innerText.trim() === want);
      if (b) { setTimeout(() => b.click(), 100); return { clicked: s.button, answered: want, settings: s }; }
      if (!publishDialog()) break;
    }
    return { clicked: s.button, answered: null, settings: s };
  }

  // The published list is the confirmation; the slug comes from it, not the title.
  async function published(id = draftId(), tries = 10) {
    if (!id) return { refused: "no draft id; pass it" };
    for (let i = 0; i < tries; i++) {
      const j = await (await fetch(`${location.origin}/api/v1/post_management/published?offset=0&limit=25&order_by=post_date&order_direction=desc`, { credentials: "include" })).json();
      const p = j.posts.find((x) => String(x.id) === String(id));
      if (p) return { id: p.id, slug: p.slug, url: `${location.origin}/p/${p.slug}`, post_date: p.post_date, email_sent_at: p.email_sent_at, audience: p.audience };
      await sleep(3000);
    }
    return { published: false };
  }

  return { prepare, paste, setTitle, audit, clear, openPublish, settings, publish, published };
})();
