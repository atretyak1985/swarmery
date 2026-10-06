---
name: write-article
description: Use when the author wants a new article, post or essay about this project for Substack, Medium or another outlet and gives a topic direction to start from — "напиши статтю про…", "стаття для сабстеку", "write an article about…", "/write-article <direction>". Not for docs, READMEs, changelogs or release notes, and not for publishing an article that is already written (publish-substack).
argument-hint: "<topic direction> [substack|medium]"
---

# Write an article

An article about this project needs two kinds of material. The author holds one:
why something was built, what went wrong on the way, what they now believe. The
repository holds the other: the numbers, names and dates the author would
misremember. This skill gets the first by interviewing the author and the second
by reading, and puts nothing into the article that came from neither.

The layout, the front matter and what each outlet's editor drops are in
`articles/README.md`. The author's voice and standing facts are in
`articles/STYLE.md` once the first interview has written it.

## 1. Read before asking

- `articles/README.md`, `articles/STYLE.md`, and the front matter of every
  existing article, so the new one neither repeats an old one nor misses a
  chance to link to it.
- The direction itself, in the repository: code, `docs/`, `CHANGELOG.md`, ADRs,
  `git log`, merged PRs. Keep a fact sheet as you go, one line per fact with
  where it comes from (`path:line`, commit, PR number).

The research decides the quality of the interview. It removes every question
the repository can answer, and it turns the rest from generic ("what challenges
did you face?") into specific ("PR #355 fixed an 11-hour hang; is that the story
here, or a footnote?").

## 2. Interview

Hold the interview in the language the author writes to you in, whatever
language the article will be in. Ask in rounds of at most four questions, and
write each round after reading the answers to the one before.

**First round: the frame.** Offer two to four angles you found in the research,
each as a working title plus the one claim that article would defend. The author
picks one, merges two, or redirects. Then settle whatever the invocation and
`STYLE.md` have not: the outlet, the reader (who they are, what they already
know, what they should do after reading), the article's language, the rough
length. These are choices, so `AskUserQuestion` suits them.

**Later rounds: the substance.** These answers are stories, so ask them as plain
numbered questions and end your turn. Draw on the ones this angle needs:

- the episode: what happened, on which day, that made this worth building or
  changing
- what was tried first, and why it failed
- what surprised them
- the claim a skeptical reader will reject, and the author's answer to it
- what they are not claiming, and what is still unsolved
- what the reader should do next

Then follow the answer that is most interesting or least precise down to an
instance: which session, how many, measured how, what the screen showed.

**When to stop.** You can state the thesis in one sentence the author agrees
with, and every section of the outline has something concrete to carry it: an
episode, a number, an example, a quoted line. That usually takes two to four
rounds. If the author ends the interview sooner, a section still without
material is cut from the outline.

**First article only**, when `articles/STYLE.md` does not exist: add a round on
voice. Ask for one or two pieces whose tone they want (their own or someone
else's), whether it is "I" or "we", how blunt they want to sound, their bio line
and the links every article should carry. Write the answers to `STYLE.md`.

## 3. Brief, then approval

Create `articles/<outlet>/<slug>/` and write `brief.md`:

- **Thesis**: one sentence.
- **Reader**: who, and what they know coming in.
- **Outline**: each section's point and the material that carries it.
- **Facts**: each claim about the project with its source.
- **Interview**: the questions and the author's answers, verbatim.
- **Open**: what is still unverified or undecided.

Show the thesis and the outline in chat. Draft once the author approves them.

## 4. Draft `article.md`

- **Every sentence has a source.** A fact about the project comes from the fact
  sheet. An experience, motive, opinion or reaction comes from an interview
  answer. When a paragraph wants a detail you do not have, ask for it or drop
  the paragraph.
- **The author's words are the raw material.** Reuse their phrasing. When the
  interview was in another language, translate the bluntness and the jokes along
  with the meaning. Write in the first person, as `STYLE.md` sets it.
- **The reader arrives cold.** They have never seen this repository. Introduce
  each project term where it first appears, and link the thing you name.
- **Open on the episode or on the claim.** The reader came for what happened
  here; a general sentence about agents or software stays only when the next
  sentence needs it.
- **Code and commands are copied from the repository** and run before they are
  shown.
- **Write within the outlet's limits** from `articles/README.md`: for Substack
  and Medium that means no tables and no `# ` heading in the body.
- **Images**: list each one the article needs and what it must show, for the
  author to capture into `assets/`. Reference only files that exist.

## 5. Check the draft

1. Re-read every fact against its source as the repository stands now.
2. Give the article to one fresh subagent with nothing but its text and the
   reader's description. Ask it for the thesis in one sentence, the place it
   lost the thread, the claim it did not believe, and what it would cut. A
   thesis that differs from the brief's means the structure is hiding the point;
   fix the structure.
3. Read it once for the shapes that mark prose nobody decided on: three-item
   lists by reflex, "it's not X, it's Y" turns, a paragraph that ends by
   restating itself, a heading phrased as a question. Each one marks a spot
   where a specific fact belongs. Put the fact there, or cut the sentence.

## 6. Hand over

Report the path, the word count, three title options with a subtitle of at most
255 characters, what is still unverified, and the images the author has to
supply. When the author edits the draft or comments on it, write the part that
will hold for the next article into `articles/STYLE.md`.

Publishing is a separate step: `publish-substack`.
