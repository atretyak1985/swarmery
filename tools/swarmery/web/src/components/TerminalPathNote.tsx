// The terminal half of account readiness (SC-9): a connected account governs
// what swarmery DISPATCHES — runs and the dashboard terminal — but a `claude`
// the operator starts by hand reads ~/.claude unless CLAUDE_CONFIG_DIR is set
// in that shell. This note names the CLI that closes the gap. The commands are
// the documented literals from cmd/swarmery/account.go's help text (there is
// no daemon endpoint serving them: which|use|clear|env|exec deliberately never
// contact the daemon, so the strings cannot be fetched, only quoted).

import type { MessageDescriptor } from '@lingui/core';
import { msg } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';

const LINES: readonly { cmd: string; what: MessageDescriptor }[] = [
  {
    cmd: 'swarmery account which', // i18n-ignore — a shell command, quoted verbatim
    what: msg`which account this project runs under, and why`,
  },
  {
    cmd: 'swarmery account exec -- claude', // i18n-ignore — a shell command, quoted verbatim
    what: msg`run claude under it, one-off`,
  },
  {
    cmd: 'eval "$(swarmery account env)"', // i18n-ignore — a shell command, quoted verbatim
    what: msg`or export it into the current shell`,
  },
];

export function TerminalPathNote(): JSX.Element {
  const { i18n } = useLingui();
  return (
    <div className="mt-2 rounded-lg border border-line bg-bg/40 px-2.5 py-2">
      <p className="font-mono text-[10px] leading-relaxed text-ink-dim">
        <Trans>
          Dispatched runs and the dashboard terminal now use this account. A terminal you open
          yourself still runs the machine default — from the project root:
        </Trans>
      </p>
      <dl className="mt-1.5 space-y-0.5">
        {LINES.map((l) => (
          <div key={l.cmd} className="flex flex-wrap items-baseline gap-x-2">
            <dt className="font-mono text-[10px] whitespace-nowrap text-ink-2">
              <code>{l.cmd}</code>
            </dt>
            <dd className="font-mono text-[9.5px] text-ink-faint">{i18n._(l.what)}</dd>
          </div>
        ))}
      </dl>
    </div>
  );
}
