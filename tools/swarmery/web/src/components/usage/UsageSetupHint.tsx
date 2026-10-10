// The "not connected yet" block on a usage provider card.
//
// It replaces what used to be a red error line for every credential-shaped
// outcome (`Claude token refresh failed — run \`claude\` to re-login`). Nothing
// is broken in those cases, so nothing should look broken: the daemon sends a
// UsageHint (internal/usage.Hint) and this renders it as guidance — what is
// missing, the command that supplies it, where the credential is read from, why
// the dashboard needs it, and how it is handled once it exists.
//
// Amber, not red: the app's semantic set reads amber as "waiting on you".
// All copy comes from the daemon so the wording lives in one place.

import { Trans, useLingui } from '@lingui/react/macro';
import { useState } from 'react';
import type { UsageHint } from '../../api/types';

function CommandRow({ cmd }: { cmd: string }): JSX.Element {
  const { t } = useLingui();
  const [copied, setCopied] = useState(false);
  return (
    <div className="mt-1.5 flex items-center gap-2 rounded-lg border border-line bg-bg/40 px-2 py-1.5">
      <span aria-hidden="true" className="shrink-0 font-mono text-[10px] text-ink-faint">
        $
      </span>
      <code className="min-w-0 flex-1 font-mono text-[10.5px] break-all text-ink-2">{cmd}</code>
      <button
        type="button"
        aria-label={t`copy: ${cmd}`}
        onClick={() => {
          // navigator.clipboard is undefined on non-secure origins (plain-HTTP
          // LAN) — optional-chain to a no-op instead of throwing; the command
          // stays visible and selectable either way.
          void navigator.clipboard
            ?.writeText(cmd)
            .then(() => {
              setCopied(true);
              setTimeout(() => setCopied(false), 1500);
            })
            .catch(() => {});
        }}
        className="shrink-0 rounded border border-line-strong px-1.5 py-0.5 font-mono text-[9.5px] text-ink-dim transition-colors hover:text-ink focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-brand"
      >
        {copied ? t`copied` : t`copy`}
      </button>
    </div>
  );
}

/** One labelled explanation line — "why it's needed", "how it's used". */
function Note({ label, children }: { label: string; children: string }): JSX.Element {
  return (
    <p className="mt-1.5 font-mono text-[10px] leading-relaxed text-ink-dim">
      <span className="text-ink-faint">{label} — </span>
      {children}
    </p>
  );
}

export function UsageSetupHint({ hint }: { hint: UsageHint }): JSX.Element {
  const { t } = useLingui();
  const sources = hint.sources ?? [];
  const sourceCount = sources.length;
  return (
    <div
      className="mt-2 border-l-2 border-amber bg-amber/8 px-2 py-2"
      data-hint={hint.kind}
      role="status"
    >
      <p className="font-mono text-[11px] font-semibold text-ink">{hint.title}</p>
      <p className="mt-1 font-mono text-[10.5px] leading-snug break-words text-ink-2">
        {hint.detail}
      </p>

      {hint.command !== undefined && hint.command !== '' && <CommandRow cmd={hint.command} />}

      <Note label={t`why it's needed`}>{hint.why}</Note>
      <Note label={t`how it's used`}>{hint.handling}</Note>

      {sources.length > 0 && (
        // Collapsed by default: the paths matter when the login exists but is not
        // being found, and are noise otherwise.
        <details className="mt-1.5">
          <summary className="cursor-pointer font-mono text-[10px] text-ink-faint transition-colors hover:text-ink-dim">
            <Trans>looked in ({sourceCount})</Trans>
          </summary>
          <ul className="mt-1 flex flex-col gap-0.5">
            {sources.map((s) => (
              <li key={s} className="font-mono text-[9.5px] break-all text-ink-dim">
                {s}
              </li>
            ))}
          </ul>
        </details>
      )}
    </div>
  );
}
