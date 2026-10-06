# Articles

Long-form writing about swarmery. One directory per article, grouped by the
outlet the article is written for.

```
articles/
├── README.md            this file
├── STYLE.md             the author's voice and standing facts (bio, links);
│                        written by the first /write-article interview
├── substack/
│   ├── PUBLICATION      one line, https://<name>.substack.com;
│   │                    written on the first /publish-substack run
│   └── <slug>/
│       ├── article.md   the article as published: front matter + body
│       ├── article.uk.md  the same article in the author's working language
│       ├── brief.md     thesis, outline, fact sources, interview record
│       ├── assets/      images and screenshots the article embeds
│       ├── links.txt    where it went live, one `outlet = url` per line
│       └── build/       generated paste-ready HTML (gitignored)
├── medium/
│   └── <slug>/          same shape
└── devto/
    └── <slug>/          same shape
```

`<slug>` is short lowercase kebab-case. An article lives under the outlet it is
published on first. A version adapted for a second outlet gets its own
directory under that outlet, with `canonical_url` in its front matter pointing
at the first publication.

This repository is public: everything here, `brief.md` and unpublished drafts
included, is readable by anyone from the moment it is pushed.

## Workflow

| Step | Command | Ends with |
| --- | --- | --- |
| Write | `/write-article <topic direction> [substack\|medium\|devto]` | `brief.md` + `article.md`, after an interview |
| Publish | `/publish-substack <slug>` | a Substack **draft**; goes live only on an explicit yes |

The skills live in `.claude/skills/`.

## `article.md`

```yaml
---
title: "The headline"
description: "The subtitle, at most 255 characters."
tags: [agents, claude-code]
language: en
published: false
---
```

- `description` is the subtitle. Keep it one line and at most 255 characters:
  above that Substack refuses to save the whole draft, body included.
- `published` flips to `true` once the article is live; the URL goes into
  `links.txt`.
- The body has no `# ` heading, because the title is its own field. Sections
  are `##`, subsections `###`, nothing deeper.
- Images are relative paths into `assets/` and carry alt text.

## What each outlet does to an article

Substack and Medium have no publishing API: the body is pasted into a web
editor, and the editor discards what it cannot hold without reporting an error.
dev.to takes the markdown file as it is, front matter included, and has rules of
its own. Write around these from the first draft.

**Substack**

- No tables. Use a list, or an image of the table.
- A link whose text is inline code loses the link and keeps the code. Put the
  code outside the link text.
- Images are fetched from their public GitHub URL at paste time, so they must
  be committed and pushed before publishing.
- Code blocks, image alt text and plain links survive.

**Medium**

- No tables.
- Multi-line code blocks are flattened to one line on import.
- Image alt text is blanked on paste.
- Two heading sizes only.

**dev.to**

- Tables and multi-line code blocks render as written.
- A line break inside a paragraph is shown as a line break. Write each
  paragraph on one line.
- `article.md` is pasted whole, so its front matter follows dev.to's format:
  `tags` is a comma-separated line (`tags: agents, claudecode`) of at most four
  tags, letters and digits only, and a fifth is dropped without a warning.
- Images need absolute URLs: the public GitHub URL of a committed and pushed
  file, or an upload through dev.to's editor.
- `cover_image` is cropped to about 2.38:1, for example 1000×420.
- `canonical_url` in the front matter marks a cross-post.

The editor and rendering behaviours above were measured between August and
October 2026 by the author of
[publishing-kit](https://github.com/xbill9/publishing-kit), not by this
project, and an outlet can change. `/publish-substack` reads back the draft
Substack saved and compares it with the source, which is what catches a change.
