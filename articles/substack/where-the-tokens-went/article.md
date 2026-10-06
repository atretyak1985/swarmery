---
title: "I Blamed 305 Background Sessions. They Cost $10."
description: "My weekly Claude limit kept running out in the middle of the week. I counted eight weeks of sessions to find out why, and the answer was not the agents I suspected."
tags: [claude-code, ai-agents, tokens]
language: en
published: false
---

![Bar chart of 624 working sessions. Sessions that stayed under 150k tokens of context are 54.0% of sessions and 3.7% of cost. Sessions that passed 300k are 19.2% of sessions and 79.0% of cost.](assets/sessions-vs-cost.png)

A fifth of my sessions made four fifths of the cost.

I have two Claude Max 20x subscriptions, and both have a weekly limit. For me, expensive is when the limit runs out in the middle of the week. And at some point I saw that my weekly tokens were running out fast. When they ran out, I kept working through the paid API. So I started to investigate why.

For those who have not read the earlier posts: [Swarmery](https://github.com/atretyak1985/swarmery) is a program that runs on my computer next to Claude Code. Claude Code saves a full record of every session to disk, and Swarmery reads those records into a local database: every answer of the model, how many tokens it took, which agent it was. So I don't have to guess where the tokens go, I can count them.

One note before the numbers. I cannot see how the subscription counts tokens against the weekly limit. What I can do is price every token the way the API price list does. So all the dollar amounts below are what the same work would cost at the API list price. This is not my bill. It is a ruler, and it is the only one I have.

The first place I looked was the background. Swarmery starts some sessions by itself: a judge that grades how the agents worked, a writer of handoff notes, and so on. These are not the subagents that people usually argue about, I will get to those later. These are small sessions that nobody asked for by hand, and on the dashboard there were hundreds of them. On 27 July there were 305, against 28 sessions in my projects. The day before there were 30 and the day after 55, so that day was not a normal one. There was a bug: the judge was grading its own grading sessions.

I fixed the bug. But then I looked at what all of this cost. The 305 background sessions together cost $10.23. The 28 sessions in my projects cost $534.57.

![Two small bar charts for 27 July. Sessions: 305 background, 28 in projects. Cost: $10.23 background, $534.57 in projects.](assets/july-27.png)

Three hundred and five sessions for ten dollars, and twenty-eight for five hundred.

So the number of sessions told me nothing about the cost. To see what does, I took eight full weeks, from 10 August to 4 October. July is not in there: Swarmery keeps the detailed record of every answer for sixty days, and after that only the daily totals stay.

In those eight weeks there were 1,947 sessions, and they cost $16,378. Of them, 1,323 were background sessions, and all of those together cost $76. So I put them aside and looked at the other 624, the sessions where real work happened.

I sorted them by how big their context got. Context is everything the model has to read before it answers: the conversation so far, the files, the results of commands. The models I use accept up to a million tokens of it, so a session can keep growing for a long time before anything stops it.

Sessions that never passed 150 thousand tokens are 54% of the working sessions and 3.7% of the cost. On average such a session is 19 answers from the model and $1.78.

Sessions that passed 300 thousand tokens are 120, which is 19% of the working sessions, and they made 79% of the cost. On average such a session is 727 answers, fourteen hours on the clock between the first answer and the last, pauses included, and $107. The ten most expensive sessions alone are more than 22% of everything.

Why does a long session cost so much? There are two things, and they multiply.

The first is simply the number of answers: 727 against 19.

The second is that every answer gets more expensive as the session fills up. The model remembers nothing between answers. Every time, it reads the whole conversation again from the beginning. Claude Code keeps that text in a cache, and a token read from the cache costs a tenth of a new one or even less, so one re-reading is cheap. But it happens before every answer, and the text keeps growing.

I measured it. An answer given while the context was under 150 thousand tokens cost about 8 cents on average. Between 150 and 300 thousand it cost 13.5 cents. Above 300 thousand it cost 27.5 cents.

![Bar chart of the average cost of one model answer by context size: 7.7 cents under 50k tokens, 8.2 cents from 50k to 150k, 13.5 cents from 150k to 300k, 27.5 cents at 300k and over.](assets/cost-per-answer.png)

The same answer costs three times more in a full session than in a fresh one.

Imagine a meeting where, before anyone can say a new sentence, the secretary reads the whole transcript aloud from the first minute. In the first hour nobody notices. By the evening nobody is working anymore, everybody is listening to the transcript.

When I split the cost by the kind of token, re-reading the cache turned out to be about 72% of it. Writing into the cache is 19%, and the model's own answers are 9%. New input, which is the part I actually type, is one tenth of a percent.

And here I have to come back to the ruler. If I count tokens and not dollars, the cached ones are 98% of all the tokens that passed through. I don't know how the weekly limit weighs them. If it counts them cheaply, like the price list does, the picture is the one above. If it counts every one of them in full, the long sessions look even worse.

Now about subagents. A post on Substack, [Why Claude Code Subagents Burn So Many Tokens](https://youcanbuildthings.substack.com/p/why-claude-code-subagents-burn-so), says that subagents cost up to 85% of a heavy session. For that kind of session my numbers agree. Of my 120 big sessions, 51 are orchestrations, where a lead agent hands out work to subagents for hours. In those 51 the subagents are 70% of the cost, and in one of them 97%.

But over all eight weeks, subagents are 35% of the cost and the main conversation is 65%. The reason is the other 69 big sessions. There are almost no subagents in them, just me and one long conversation, and together they cost $6,226 against $6,660 for the orchestrations. One orchestration is more expensive than one long conversation, $131 against $90. But both are on the list for the same reason.

With subagents or without them, the expensive session is the long one.

Claude Code can compact a conversation, which means replacing the old part with a short summary. In the data a compaction looks like the context suddenly falling by more than half. In my 120 big sessions I found five.

I did not only look at all this, I also built things against it. Every session card in Swarmery now has a context badge: amber from 150 thousand tokens, red from 300 thousand. And the main one is the handoff. When a session passes 150 thousand tokens, Swarmery writes a short note by itself: the goal, the current state, the files that were touched, the decisions that were made and the next step. If the session grows by another 75 thousand, it writes a fresh one. It writes from its own database and not from the transcript, because reading a transcript of 400 thousand tokens in order to save tokens would make the fix as expensive as the disease. The idea was simple: I take the note, clear the session and continue in a new one with a small context.

And now the honest part. I checked what happened to the sessions after those notes.

In these eight weeks Swarmery wrote 546 notes for 267 sessions. 79 of those sessions had no answer after the note. I cannot tell from the data whether I used the note there or the work simply ended. 72 went on for up to a hundred more answers. And 116 went on for more than a hundred answers after the note was ready.

![One bar of 267 sessions that received a handoff note: 79 had no answer after the note, 72 had up to 100 more answers, 116 had more than 100 more answers.](assets/after-the-handoff.png)

The note was ready, and the session went on without it.

What did that cost? All the answers given above 150 thousand tokens, in all sessions, cost $12,151, which is 74% of everything. If each of them had cost what an answer costs between 50 and 150 thousand tokens, which is 8.2 cents, they would have cost $5,400. The difference is $6,750, or 41% of the whole eight weeks. To be fair, this is a ceiling and not a promise: a new session has to read some of the files again, and I cannot count that from here.

I have to say honestly that the note is only an offer. It shows up on the session card, and nothing stops the session. And I know the reason about myself: when the task is big, it is a pity to lose the context.

What I want to try next is to do the handoff not when the context gets big, but when a phase is finished. In Swarmery a big task is a plan split into phases, and each phase is a separate document with its own checkboxes. The end of a phase looks to me like the natural place to stop and continue in a new session. This is not built yet. For now it is only an idea.

If your limit also runs out in the middle of the week, I would not start by counting agents. I would look for the few sessions that lived the longest. You don't need Swarmery for the first look: Claude Code keeps one file per session in `~/.claude/projects`, and the biggest files are the place to start.

The post about lessons and forecasts that I promised last time is still coming, it will be the next one. Swarmery's code is open on [GitHub](https://github.com/atretyak1985/swarmery), and the script that counts every number in this post lies [next to the article](https://github.com/atretyak1985/swarmery/tree/main/articles/substack/where-the-tokens-went/evidence).

Previous post: [Why I Don't Believe an Agent When It Says "Done"](https://swarmery.substack.com/p/why-i-dont-believe-an-agent-when)
