// «Skills to extend first» (verify-not-trust phase 18, D16/D35): a one-line
// hint above the Agents roster. Before anyone proposes a new agent, it points at
// the weekly skill-audit report's "Before a new agent, extend one of these"
// list, which the Insights tab's Skill audit section renders. Advice only — no
// action here creates, deletes or merges anything.

import { Trans } from '@lingui/react/macro';
import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import type { SystemSkillAuditReportBody } from '../../api/types';
import { fetchSkillAuditReport } from '../../api/system';

export function SkillsToExtendHint({ insightsHref }: { insightsHref: string }): JSX.Element {
  // undefined = loading, null = no report yet (or the endpoint is unreachable).
  const [report, setReport] = useState<SystemSkillAuditReportBody | null | undefined>(undefined);

  useEffect(() => {
    let cancelled = false;
    fetchSkillAuditReport()
      .then((r) => {
        if (!cancelled) setReport(r);
      })
      .catch(() => {
        if (!cancelled) setReport(null);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const date = report?.date ?? '';
  return (
    <div
      role="note"
      className="flex flex-wrap items-center gap-x-2 gap-y-1 border-b border-line bg-surface px-4 py-2 text-[12.5px]"
    >
      <span className="text-ink-dim">
        <Trans>Before proposing a new agent, extend an existing skill.</Trans>
      </span>
      {report === undefined ? null : report === null ? (
        <span className="font-mono text-[11px] text-ink-faint">
          <Trans>No skill-audit report yet — seed config/routines/skill-audit.json.</Trans>
        </span>
      ) : (
        <Link to={insightsHref} className="font-mono text-[11px] text-blue hover:underline">
          <Trans>Skills to extend first — skill audit {date}</Trans>
        </Link>
      )}
    </div>
  );
}
