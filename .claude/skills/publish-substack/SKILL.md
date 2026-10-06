---
name: publish-substack
description: Use when an article under articles/substack/ has to reach Substack — "publish to Substack", "put the draft on Substack", "опублікуй на сабстек", "/publish-substack <slug>" — or when a post already live on Substack needs its body replaced from the repository. Not for writing the article (write-article) and not for other outlets.
argument-hint: "<slug>"
---

# Publish to Substack

Substack has no publishing API. The body is pasted into its web editor in the
author's own signed-in Chrome, and that editor discards what its schema cannot
hold without reporting anything: a table, a link around inline code, a subtitle
one character too long that stops the whole draft saving. So nothing here trusts
the screen. The build refuses what is known to be dropped, and after the paste
the draft Substack **saved** is read back and compared with what went in.

A run ends at a **draft**. Publishing emails every subscriber and cannot be
undone, so it happens only when the author, having seen the draft, says so in
this session and says whether the email goes out.

## Before starting

- `pandoc` is installed.
- Chrome with the Claude extension is signed in to Substack. Drive it with the
  `claude-in-chrome` tools; the Playwright browser is a separate profile with no
  session.
- The article's images are committed and pushed. Substack fetches them from
  GitHub at paste time.
- The publication address is the one line in `articles/substack/PUBLICATION`
  (`https://<name>.substack.com`). When the file is missing, ask the author and
  write it.

## 1. Build

```bash
python3 .claude/skills/publish-substack/scripts/build-substack.py articles/substack/<slug>
```

It prints a manifest (title, subtitle, link targets, code blocks with their line
counts, image count) and writes `build/substack.html`, `build/manifest.json` and
a copy of the editor helper. When it lists problems, fix each one in
`article.md` and build again.

## 2. Carry the article into the editor tab

The editor page's security policy blocks any request to the local machine, and
HTML retyped into a tool call gets corrupted without a trace. `window.name`
survives a navigation in the same tab, so the bytes travel there.

1. Serve the build directory, in the background:
   `python3 -m http.server 8901 --bind 127.0.0.1 --directory articles/substack/<slug>/build`
2. Open `http://127.0.0.1:8901/substack.html` in a new tab and run in the page:

   ```js
   (async () => {
     const get = async (path) => (await fetch(path)).text();
     window.name = JSON.stringify({
       html: await get('/substack.html'),
       js: await get('/substack-editor.js'),
       meta: JSON.parse(await get('/manifest.json')),
     });
     return window.name.length;
   })()
   ```

3. Navigate **the same tab** to `<publication>/publish/post?type=newsletter`.
   That address creates a new draft and redirects to `/publish/post/<id>`, so
   open it once per article and keep the id. If it lands on a sign-in page,
   stop and ask the author to sign in.

## 3. Paste, then read back what was saved

Run in the editor page, one call at a time, reading each result. Each call is a
single expression, because a top-level `const` declared in one call makes the
next call that declares it fail:

```js
(() => { const p = JSON.parse(window.name); (0, eval)(p.js); return JSON.stringify(ss.prepare(p.html)); })()
```

```js
ss.paste().then(JSON.stringify)
```

```js
(() => { const m = JSON.parse(window.name).meta; return JSON.stringify(ss.setTitle(m.title, m.subtitle)); })()
```

```js
ss.audit().then(JSON.stringify)
```

A result of `{refused: …}` is a guard that fired. Read the reason and fix the
condition it names.

Compare the audit with the manifest:

- every manifest `links` target is in the audit's `links`
- `codeBlockLines` are equal, in order
- the image count is equal and every image has `rehosted: true`
- `types` holds no `latex_block`
- `title` and `subtitle` are equal to the manifest's

On a difference, find its cause in `article.md` and fix it there. Then run
`await ss.clear(<id>)`, rebuild, and repeat steps 2 and 3, returning to
`<publication>/publish/post/<id>` this time, since the create address would
start a second draft.

## 4. Stop at the draft

Stop the local server. Give the author the draft's address and the comparison,
line by line. The author reads the draft in the editor.

## 5. Publish, on the author's word

Before the click, the author has said in this session to publish this article
and has answered whether subscribers get the email.

1. `await ss.openPublish()` and read its `settings` back to the author:
   audience, comments, email, scheduling, and the button's label.
2. `await ss.publish({email: <their answer>, audience: "everyone"})`. It refuses
   when any setting differs from what it was given.
3. `await ss.published(<id>)`. Take the address from this response: Substack
   cuts the slug short, so it cannot be derived from the title.
4. Write `substack = <url>` to the article's `links.txt` and set
   `published: true` in its front matter.

## Updating a post that is live

Open `<publication>/publish/post/<id>`, run `await ss.clear(<id>)`, then steps
2 and 3 against that address. `ss.openPublish()` then shows the button
`Update now` with no email setting; `await ss.publish({update: true})` replaces
the live body and sends nothing to subscribers.

## Where the helper comes from

`scripts/substack-editor.js` is vendored unmodified from
[publishing-kit](https://github.com/xbill9/publishing-kit) (Apache-2.0). What it
does was measured by its author against Substack's editor on 2026-10-05. When a
helper refuses, or the audit differs, in a way `article.md` does not explain,
the editor has probably changed: read upstream's
`skills/publishing/references/substack.md` and the current helper before
changing anything here.
