// The open session's state, in the one sentence its header says it (artboards
// 2b–2d): a bold lead in the state's colour, the rest in grey. It builds on the
// list's stateSentence and says more where the detail knows more:
//
//   working      Working · last action 2m ago — <the action>.
//   permission   Waiting for your permission for 3m · Bash: npm test. Expires in 4:12.
//   answer       Waiting for your answer for 3m · …
//   quiet        Quiet for 40m · …
//   finished     Finished in 38m · 4 files changed, marked success.
//   stopped      Stopped by you.
//
// A prompt cache that went cold (the daemon's cacheCold flag) is a clause of the
// sentence instead of a chip beside it: the next message, a resume included,
// re-writes the whole context. `⋯ → continue from handoff` is its action.

import { plural, t } from '@lingui/core/macro';
import type { NeedsYouItem, PermissionRequest, SessionDetail } from '../../api/types';
import { fmtTime } from '../../lib/format';
import { compactSpan, stateSentence, type RowState, type StateSentence } from '../../lib/sessionsView';

/** Seconds left until `expiresAt` as m:ss, never below 0:00. */
export function countdown(expiresAt: string, nowMs: number): string {
  const left = Math.max(0, Math.round((new Date(expiresAt).getTime() - nowMs) / 1000));
  return `${String(Math.floor(left / 60))}:${String(left % 60).padStart(2, '0')}`;
}

function isPermission(gate: NeedsYouItem | undefined): gate is NeedsYouItem {
  return gate?.kind === 'approval' || gate?.kind === 'prod_deploy_local';
}

function finishedRest(detail: SessionDetail, nowMs: number): string {
  const elapsed = compactSpan(new Date(detail.endedAt ?? nowMs).getTime() - new Date(detail.startedAt).getTime());
  const count = new Set(detail.fileChanges.map((c) => c.filePath)).size;
  const files =
    count === 0
      ? t`no files changed`
      : plural(count, {
          one: '# file changed',
          few: '# files changed',
          many: '# files changed',
          other: '# files changed',
        });
  if (detail.outcome === 'success') {
    const result = t`success`;
    return t`in ${elapsed} · ${files}, marked ${result}.`;
  }
  return t`in ${elapsed} · ${files}.`;
}

export interface HeaderSentenceInput {
  detail: SessionDetail;
  state: RowState;
  nowMs: number;
  /** The newest step's time: "last action N ago". */
  lastEventAt: string | null;
  /** The live action line from the list's WS feed. */
  now: string | null;
  gate: NeedsYouItem | undefined;
  /** The pending request behind a permission gate, once read: its expiresAt. */
  request: PermissionRequest | null;
}

export function headerSentence({
  detail,
  state,
  nowMs,
  lastEventAt,
  now,
  gate,
  request,
}: HeaderSentenceInput): StateSentence {
  const base = stateSentence(detail, state, nowMs, lastEventAt, now, gate);
  let sentence = base;
  if (state === 'needs' && isPermission(gate)) {
    const waited = compactSpan(nowMs - new Date(gate.blockingSince).getTime());
    const tool = gate.toolName !== '' ? gate.toolName : (request?.toolName ?? '');
    const command = gate.preview;
    const head =
      tool === ''
        ? t`for ${waited}`
        : command === ''
          ? t`for ${waited} · ${tool}`
          : t`for ${waited} · ${tool}: ${command}`;
    if (request === null) {
      sentence = { lead: base.lead, rest: `${head}.` };
    } else {
      const clock = countdown(request.expiresAt, nowMs);
      sentence = { lead: base.lead, rest: `${head}. ${t`Expires in ${clock}.`}` };
    }
  } else if (state === 'working' && now !== null) {
    const ago = compactSpan(nowMs - new Date(lastEventAt ?? detail.endedAt ?? detail.startedAt).getTime());
    sentence = { lead: base.lead, rest: t`· last action ${ago} ago — ${now}.` };
  } else if (state === 'done') {
    sentence = { lead: base.lead, rest: finishedRest(detail, nowMs) };
  }
  const cold = detail.cacheCold;
  if (cold != null) {
    const at = fmtTime(cold.since);
    sentence = { lead: sentence.lead, rest: `${sentence.rest} ${t`Prompt cache cold since ${at}.`}` };
  }
  return sentence;
}
