# Brief: where the tokens went

Outlet: Substack. Language: English. Length: 1,500 to 2,000 words.
Interview held 2026-10-06. Status: outline approved, draft written and revised
after one cold read; waiting for the author's read.

## Thesis

My weekly limit was not eaten by the hundreds of background agent sessions I
suspected. It was eaten by a few long sessions, because every answer re-reads
the whole conversation, and the handoff I built to end those sessions goes
unused exactly when the task is big.

## Reader

A developer who runs Claude Code on a subscription, has hit the weekly limit,
and has read that subagents are what burns the quota. They do not know
Swarmery: one paragraph says it is a local program that reads every Claude Code
session record into a database, which is why these numbers can be counted.

## Outline

1. **The limit ran out.** Two Max 20x subscriptions, the weekly limit gone in
   the middle of the week, the paid API to keep working.
   Material: interview answers.
2. **The ruler.** Dollars are API list price, not a bill, and how the limit
   weighs tokens is not visible.
3. **The first place I looked.** 27 July: 305 background sessions, $10.23; 28
   project sessions, $534.57. The day was inflated by a bug in the judge.
   Material: daily rollups; the judge fix.
4. **Eight weeks.** 10 August to 4 October: 1,947 sessions, $16,378. The 1,323
   background sessions cost $76 and are set aside. Of the 624 working sessions,
   120 passed 300k tokens of context: 19% of sessions, 79% of cost.
5. **Why.** Two things multiply: the number of answers (727 against 19), and
   the price of each answer, which rises from about 8 cents under 150k to 27.5
   cents above 300k. Re-reading the cache is about 72% of the cost.
6. **Back to the ruler.** Cached tokens are 98% of all tokens by count, so if
   the limit counts them in full, long sessions look even worse.
7. **Subagents.** In the 51 orchestration sessions subagents are 70% of the
   cost, which agrees with the Substack post's "up to 85% of a heavy session".
   Over everything they are 35%, because 69 big sessions have almost none.
8. **Compaction.** Five compactions found in the 120 big sessions.
9. **What I built.** The context badge and the handoff note.
10. **What happened after the notes.** 546 notes for 267 sessions; 116 went on
    for more than a hundred answers. Answers given above 150k cost $12,151; at
    the fresh-session rate they would have cost $5,400. A ceiling, not a
    promise.
11. **Why, and what next.** "When the task is big, it is a pity to lose the
    context." A handoff at the end of each plan phase; not built.
12. **For the reader.** Look for the sessions that lived longest; the biggest
    files in `~/.claude/projects` are the place to start.

Images, drawn by `evidence/charts.py`: `assets/sessions-vs-cost.png`,
`assets/july-27.png`, `assets/cost-per-answer.png`,
`assets/after-the-handoff.png`.

## Landscape

Searched 2026-10-06, Substack only. Most of what the search returned is older
than a month; little from the last 30 days surfaced.

- [Claude Code Token Optimization Guide](https://buildtolaunch.substack.com/p/claude-code-token-optimization),
  Build to Launch, 2026-04-14, 62 likes, 28 comments. Setup changes and session
  habits that keep costs from compounding.
- [Why Claude Code Subagents Burn So Many Tokens](https://youcanbuildthings.substack.com/p/why-claude-code-subagents-burn-so),
  2026-06-12. Its subtitle: "Claude code subagents cost up to 85% of a heavy
  session." The article quotes this claim, links it, and agrees with it for
  that kind of session.
- [A Claude Pro Capacity Cut, a Self-Auditing Claude Code Loop, and a New Multi-Agent Orchestrator](https://claudescorner.substack.com/p/a-claude-pro-capacity-cut-a-self),
  Claude's Corner, 2026-08-30. Pro capacity drops on 14 September and builders
  are budgeting tokens.
- [Claude Code agent teams: when and how to go multi-agent](https://joseparreogarcia.substack.com/p/claude-code-agent-teams),
  2026-09-11, 8 likes, 6 comments. Four patterns, eight failure modes.
- [How I run up to 10 Claude Code agents in parallel without losing my mind](https://alexdevdunlop.substack.com/p/how-i-run-up-to-10-claude-code-agents),
  2026-06-22. The setup and "the honest cost picture".

They agree that tokens are the constraint and that more agents means more
tokens. They give advice and rules of thumb. None of them shows a measured
distribution of cost across the author's own sessions, and none reports that a
fix the author built went unused. This article adds both.

## Facts

Every database figure is produced by `evidence/measure.py` (read-only) in one
run and saved in `evidence/results-2026-10-06.json`. The window is eight whole
weeks, 2026-08-10 up to, not including, 2026-10-05. Dollars are the daemon's API
list-price computation (`tools/swarmery/config/pricing.json`,
`tools/swarmery/internal/cost/cost.go`). The daemon prunes turns older than 60
days every day, so a later run sees less of the window.

| Claim in the article | Key in the results file, or source |
| --- | --- |
| 1,947 sessions, $16,378 | `total` |
| 1,323 background sessions, $76, none over 300k | `background_sessions`; `sessions.cwd` is the daemon's own directory |
| 624 working sessions | `working_sessions` |
| Under 150k: 54% of working sessions, 3.7% of cost, 19 answers, $1.78 | `working_sessions_by_peak_context.under_150k` |
| 300k and over: 120 sessions, 19%, 79% of cost, 727 answers, 14 hours, $107 | `working_sessions_by_peak_context.300k_and_over` |
| Ten most expensive sessions: 22.5% | `concentration` |
| An answer costs 7.7, 8.2, 13.5, 27.5 cents by context | `cost_per_answer_by_context` |
| Answers above 150k: $12,151, 74%; at 8.2 cents each $5,400; difference $6,750, 41% | `cost_per_answer_by_context.answers_given_above_150k` |
| Cache read 72% of cost, cache write 19%, output 9%, new input 0.1%; cache read 98% of raw tokens | `cost_by_token_type_estimate`; re-priced total is 95.4% of the stored one, so "about" |
| A cached token costs a tenth of a new one or less | `pricing.json`: cache_read is 0.1 of input for Opus 5, Fable 5, Sonnet 5; 0.05 for Opus 5.5 |
| Subagents 35%, main conversation 65% | `threads` |
| 51 orchestrations, $6,660, $131 each, subagents 70% of their cost, 97% in one; 69 others, $6,226, $90 each | `big_sessions_by_shape` |
| Five compactions in 120 big sessions | `compactions_in_big_sessions`; a compaction is counted when the main conversation's context falls by more than half from 150k or more |
| 546 notes for 267 sessions; 79 with no answer after, 72 with up to 100, 116 with more | `handoff_notes` |
| 27 July: 305 background sessions, $10.23; 28 project sessions, $534.57; 30 the day before, 55 the day after | `late_july_daily_rollups` |
| The judge was grading its own grading sessions, and it was fixed | commit 1416b99e (2026-09-02), "the judge was scoring its own scoring runs" |
| Turn-level data is kept 60 days, then only daily totals | `tools/swarmery/internal/prune/schedule.go:25` |
| Context badge: amber at 150,000, red at 300,000 | `tools/swarmery/web/src/components/SessionCard.tsx:18-19` |
| Handoff note at 150,000 tokens, a fresh one after another 75,000; goal, state, files, decisions, next step; from the database, not the transcript; shown on the session card | `tools/swarmery/internal/handoff/handoff.go:1-34` |
| A plan is split into phases, each a document with checkboxes | `CLAUDE.md`, "Work artifacts" |
| Claude Code keeps one file per session in `~/.claude/projects` | `CLAUDE.md`, the `tools/swarmery` note |

## Interview

**Round 1, the frame.** Four angles were offered. The author chose: «Куди пішли
гроші».

**Round 2.**

1. Наприкінці липня ти вирішив, що гроші палять фонові System-сесії. Що саме ти
   тоді побачив? І в який момент зрозумів, що справа не в них?
   — «я побачив що тижневі токени швидко закінчуються тому почав досліджувати
   чому»
2. Як ти платиш насправді: підписка чи API? Що для тебе означає «дорого»?
   — «у мене підиска і тижневі ліміти і дорого це коли серед тижня закінчуються
   ліміти»
3. Чому ти все одно лишаєшся в довгій сесії? — без відповіді в цьому раунді.
4. Голос. — без відповіді в цьому раунді.

**Round 3, choices.**

- Підписка: «Max 20x, два акаунти».
- Які цифри можна публікувати: «Усе».
- Голос: «Як у постах».
- Як писав попередні пости: «Українською, потім переклад».

**Round 3, open.**

1. На який день тижня тоді закінчився ліміт і що ти робив до його скидання?
   — «використовував палтну апі»
2. Спершу ти грішив на System-сесії судді. Що ти подумав, коли з'ясувалось, що
   вони коштують $8,63 на день? — «непамятаю»
3. Що тебе тримає від `/clear`? І що ти порадив би читачу?
   — «коли велика задача шкода втратити контекст було б добре якоссь після
   завершення фази ити хендоф»

**After the outline.** Графіки: «Роби». Рядок про обіцяний пост про уроки й
прогнози: «пиши».

## Cold read

One fresh reader, given only the draft and the reader's description. Its
one-sentence thesis matched this brief's. What it changed:

- "6% of sessions" was inflated by background sessions; the article now counts
  the 624 working sessions and says 19%.
- The comparison with "up to 85%" was not like for like; the article now gives
  the subagent share inside orchestration sessions (70%) and agrees.
- "77% of the cost came after the note" restated the context split; the article
  now uses the count of sessions that went on, and the price of an answer above
  150k against the fresh-session price.
- The link from dollars to the limit is now stated as unknown, twice.
- Added: the million-token window, the three time windows explained, what a
  second note per session means, compaction, how to look without Swarmery.
- Cut: the advisor rule, the quota floor, the code comment, the weekly range.

## Open

Questions only the author can answer; each would add a sentence or two:

- Does the limit still run out in the middle of the week?
- What did the paid API cost in the week it was used, and is that work inside
  the numbers?
- Why were the big sessions almost never compacted?
- The day of the week the limit ran out.

For the author to confirm or replace with his own words:

- The meeting-and-transcript comparison.
- "It is a ruler, and it is the only one I have."
- The closing advice and the tip about `~/.claude/projects`.
- "The first place I looked was the background" and that 27 July was inflated
  by the judge bug. This comes from notes written that day, not from the
  interview; the fix in git is dated 2 September.
- The promise line stands at the end of the article, not at the top as in the
  previous post.

Limits of the numbers:

- The token-type split is re-priced from today's price table and lands at
  95.4% of the stored total.
- "No answer after the note" does not show whether the note was used.
- The $6,750 difference is an upper bound: it prices every answer above 150k
  at the 50k-to-150k rate and ignores what a new session must re-read.
- Compactions are inferred from the context falling by more than half.
- The link to the evidence directory resolves only once this branch is merged
  to `main` and pushed.
