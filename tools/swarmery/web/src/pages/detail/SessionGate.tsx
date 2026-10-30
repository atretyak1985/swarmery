// The gate card of a session's detail (handoff §5, §7): the one thing it waits on,
// answered in place with the same calls the Needs you page makes.
//
//   approval           allow once / deny, the note goes out as the deny reason
//   prod deploy        deny only: the daemon refuses a remote approve with 403, so
//                      the confirmation is the session's own terminal
//   question           one single-select question as a radio list; anything richer
//                      (several questions, multi-select, a free answer) keeps the
//                      Approvals form, which already handles it
//   awaiting reply     the session's question, its suggested replies when the
//                      daemon extracted them, and the composer otherwise
//
// The tier has no field of its own: a prod-deploy request is `destructive`, the
// rest `write`. `always allow in <project>` needs the rules form, so the card links
// to Needs you for it rather than growing a second one.

import { useLingui } from '@lingui/react/macro';
import { useCallback, useEffect, useMemo, useState, type MutableRefObject } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { fetchApprovals, resolveApproval, sendSessionMessage, type ApprovalAction } from '../../api';
import type { NeedsYouItem, PermissionRequest, SessionDetail } from '../../api/types';
import { DecisionCard, type DecisionTier } from '../../components/place/DecisionCard';
import { buildAnswers, EMPTY_DRAFT, fmtClock, questionsOf, type AnswerMap } from '../../lib/approvals';
import { useNowMs } from '../../lib/sessionState';
import { PendingCard } from '../Approvals';
import type { PaneActions } from './SessionPane';

export function gateTier(item: NeedsYouItem, request: PermissionRequest | null): DecisionTier {
  if (item.kind === 'prod_deploy_local' || request?.riskClass === 'prod-deploy') return 'destructive';
  if (item.kind === 'question' || item.kind === 'awaiting_reply') return 'question';
  return 'write';
}

export function SessionGate({
  item,
  session,
  onChanged,
  onOpenComposer,
  onSent,
  actions,
}: {
  item: NeedsYouItem;
  session: SessionDetail;
  /** The queue moved: reload it, so the card and the row settle. */
  onChanged: () => void;
  onOpenComposer: () => void;
  /** Echoes a reply the card sent as a pending bubble. */
  onSent: (text: string) => void;
  actions: MutableRefObject<PaneActions>;
}): JSX.Element {
  const { t } = useLingui();
  const navigate = useNavigate();
  const { slug } = useParams<{ slug?: string }>();
  const nowMs = useNowMs(1000);
  const [request, setRequest] = useState<PermissionRequest | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [reason, setReason] = useState('');
  const [pick, setPick] = useState<number>(0);

  const requestId = item.requestId;
  useEffect(() => {
    if (requestId === null) return undefined;
    let live = true;
    fetchApprovals('pending', session.projectSlug)
      .then((rows) => {
        if (live) setRequest(rows.find((r) => r.id === requestId) ?? null);
      })
      .catch(() => undefined); // the card falls back to the queue's preview
    return () => {
      live = false;
    };
  }, [requestId, session.projectSlug]);

  const resolve = useCallback(
    (action: ApprovalAction, note?: string, answers?: AnswerMap): void => {
      if (requestId === null) return;
      setBusy(true);
      setError(null);
      resolveApproval(requestId, action, note, answers)
        .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
        .finally(() => {
          setBusy(false);
          onChanged();
        });
    },
    [requestId, onChanged],
  );

  const reply = useCallback(
    (text: string): void => {
      setBusy(true);
      setError(null);
      onSent(text);
      sendSessionMessage(session.id, text)
        .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
        .finally(() => {
          setBusy(false);
          onChanged();
        });
    },
    [session.id, onSent, onChanged],
  );

  const questions = useMemo(
    () => (request?.toolName === 'AskUserQuestion' ? questionsOf(request) : null),
    [request],
  );
  const single = questions?.length === 1 && questions[0]?.multiSelect === false ? questions[0] : null;
  const suggested = item.kind === 'awaiting_reply' ? (item.suggestion?.options ?? []) : [];
  const options = single !== null ? single.options.map((o) => o.label) : suggested;
  const pickedLabel = options[pick] ?? null;

  const tier = gateTier(item, request);
  const asks = tier === 'question';
  const localOnly = item.kind === 'prod_deploy_local' || request?.riskClass === 'prod-deploy';

  const primary = (): void => {
    if (busy) return;
    if (single !== null && questions !== null && pickedLabel !== null) {
      resolve('answer', undefined, buildAnswers(questions, [{ ...EMPTY_DRAFT, selected: [pickedLabel] }]) ?? undefined);
    } else if (asks && pickedLabel !== null) {
      reply(pickedLabel);
    } else if (asks) {
      onOpenComposer();
    } else if (localOnly) {
      resolve('deny', reason.trim() === '' ? undefined : reason.trim());
    } else {
      resolve('approve');
    }
  };
  const secondary = (): void => {
    if (busy) return;
    if (asks) onOpenComposer();
    else if (!localOnly) resolve('deny', reason.trim() === '' ? undefined : reason.trim());
  };

  // `e` and `x` act on what the buttons show; read through a ref by the list.
  useEffect(() => {
    actions.current = { primary, secondary };
  });

  const richForm = request !== null && questions !== null && single === null;
  if (richForm) {
    return (
      <div className="mt-[18px]">
        <PendingCard
          request={request}
          session={null}
          nowMs={nowMs}
          busy={busy}
          onResolve={(action, note, answers) => resolve(action, note, answers)}
        />
      </div>
    );
  }

  const tool = item.toolName;
  const steps = String(session.events.length);
  const meta = asks ? t`asked at step ${steps}` : t`${tool} · asks once per scope`;
  const clock = request === null ? null : fmtClock((new Date(request.expiresAt).getTime() - nowMs) / 1000);
  const headline = asks
    ? item.question !== ''
      ? item.question
      : item.preview
    : localOnly
      ? t`This is a production deploy: confirm it in the session's own terminal`
      : t`The session asks to run ${tool}`;

  return (
    <>
      <DecisionCard
        gate
        tier={tier}
        meta={tier === 'destructive' ? t`${tool} · asks every time · no always-allow` : meta}
        {...(clock === null ? {} : { expires: t`expires in ${clock}` })}
        title={headline}
        {...(!asks && item.preview !== '' ? { code: item.preview } : {})}
        options={options.map((label, i) => ({ id: String(i), label }))}
        picked={options.length > 0 ? String(pick) : null}
        onPick={(picked) => setPick(Number(picked))}
        {...(asks || localOnly
          ? {}
          : { note: { value: reason, onChange: setReason, placeholder: t`why, if you deny` } })}
        primary={{
          label: asks ? t`reply` : localOnly ? t`deny` : t`allow once`,
          onClick: primary,
          disabled: busy || (asks && single === null && request !== null),
        }}
        {...(localOnly
          ? {}
          : { secondary: { label: asks ? t`write my own` : t`deny`, onClick: secondary, disabled: busy } })}
        link={{
          label: t`open in Needs you →`,
          onClick: () => navigate(slug === undefined ? '/needs-you' : `/p/${slug}/needs-you`),
        }}
      />
      {error !== null && (
        <div role="alert" className="mt-2 font-mono text-[11px] text-red">
          {error}
        </div>
      )}
    </>
  );
}
