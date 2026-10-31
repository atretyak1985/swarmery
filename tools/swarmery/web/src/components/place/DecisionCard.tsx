// The one decision (handoff §7), for Needs you and Inbox: eyebrow, title, the
// command, the body, a mono line of facts, a radio list of options, an optional
// note, and the actions. The main button REPEATS the picked option, so what is
// about to be sent is on the button.
//
// Permission tiers differ by SHAPE and not only by colour: destructive is a red
// rectangle, write a blue dashed pill, question an amber pill.
//
// Owning a pane, its eyebrow, title and command form the column's header band, so
// the pane puts no padding above it. `gate` is the same decision inside a session's
// detail (handoff §5): a card with the tier's border and a 14px headline, because
// there it sits among other blocks rather than owning the pane.

import type { MessageDescriptor } from '@lingui/core';
import { msg } from '@lingui/core/macro';
import { useLingui } from '@lingui/react/macro';
import type { ReactNode } from 'react';
import { DetailHeader } from './DetailHeader';

export type DecisionTier = 'destructive' | 'write' | 'question';

interface TierDef {
  glyph: string;
  label: MessageDescriptor;
  badge: string;
  primary: string;
  /** The gate card's border: the tier again, in shape and colour. */
  border: string;
}

const TIERS: Record<DecisionTier, TierDef> = {
  destructive: {
    glyph: '▲',
    label: msg`destructive`,
    badge: 'rounded-[3px] border border-red text-red',
    primary: 'border-red bg-red-bg text-red',
    border: 'border-red',
  },
  write: {
    glyph: '◆',
    label: msg`write`,
    badge: 'rounded-full border border-dashed border-blue text-blue',
    primary: 'border-green bg-green-bg text-green',
    border: 'border-blue',
  },
  question: {
    glyph: '?',
    label: msg`question`,
    badge: 'rounded-full border border-amber text-amber',
    primary: 'border-brand bg-brand-bg text-brand',
    border: 'border-brand',
  },
};

const PRIMARY_DEFAULT = 'border-brand bg-brand-bg text-brand';

export interface DecisionOption {
  id: string;
  label: string;
  /** What picking this one leads to, under the name. */
  consequence?: string;
}

export interface DecisionAction {
  label: string;
  onClick: () => void;
  disabled?: boolean;
}

export interface DecisionCardProps {
  /** Eyebrow type (`guard stop`), when the decision has no permission tier. */
  kind?: string;
  kindTone?: 'amber' | 'red' | 'default';
  tier?: DecisionTier;
  /** The grey half of the eyebrow. */
  meta?: string;
  /** Right edge of the eyebrow, e.g. `expires in 1:18`. */
  expires?: string;
  title: string;
  code?: string;
  body?: ReactNode;
  /** The mono facts line: `touched 2 protected files · contract broken: …`. */
  facts?: string;
  options?: readonly DecisionOption[];
  /** The picked option id. */
  picked?: string | null;
  onPick?: (id: string) => void;
  /** Long options: the main button repeats the pick's head, up to this many characters. */
  pickLimit?: number;
  note?: { value: string; onChange: (next: string) => void; placeholder: string };
  primary: DecisionAction;
  secondary?: DecisionAction;
  /** Extra button between the two, e.g. `always allow in <project>`. */
  extra?: DecisionAction;
  /** Right edge of the actions: `phase review →`. */
  link?: { label: string; onClick: () => void };
  /** Right edge of the actions when it is a note, not a way out. */
  foot?: string;
  /** The compact card in a session's detail. */
  gate?: boolean;
}

const KIND_TONE = { amber: 'text-amber', red: 'text-red', default: 'text-ink-dim' } as const;

/** The pick as the button says it: the part before ` ·`, cut at `limit`. */
function pickHead(label: string, limit: number | undefined): string {
  if (limit === undefined) return label;
  const head = (label.split(' ·')[0] ?? label).slice(0, limit);
  return label.length > limit ? `${head}…` : head;
}

const quietButton = (gate: boolean): string =>
  `rounded-[7px] border border-line-strong bg-transparent font-mono text-[11.5px] leading-[normal] text-ink-2 disabled:opacity-50 ${
    gate ? 'px-3 py-1.5' : 'px-[13px] py-[7px]'
  }`;

export function DecisionCard({
  kind,
  kindTone = 'amber',
  tier,
  meta,
  expires,
  title,
  code,
  body,
  facts,
  options,
  picked = null,
  onPick,
  pickLimit,
  note,
  primary,
  secondary,
  extra,
  link,
  foot,
  gate = false,
}: DecisionCardProps): JSX.Element {
  const { i18n } = useLingui();
  const tierDef = tier === undefined ? undefined : TIERS[tier];
  const chosen = options?.find((o) => o.id === picked);
  // `reply: <pick>` — the button says exactly what will go out.
  const primaryLabel = chosen === undefined ? primary.label : `${primary.label}: ${pickHead(chosen.label, pickLimit)}`;
  // The tier's colour is the gate's: a whole pane speaks in the one accent.
  const primaryTone = gate ? (tierDef?.primary ?? PRIMARY_DEFAULT) : PRIMARY_DEFAULT;

  const head = (
    <>
      <div className={`flex min-w-0 items-center font-mono text-[10.5px] leading-[normal] ${gate ? 'gap-[10px]' : 'gap-2'}`}>
        {tierDef !== undefined ? (
          <span className={`shrink-0 px-[7px] py-px font-bold whitespace-nowrap ${tierDef.badge} ${gate ? '' : '-my-0.5'}`}>
            {`${tierDef.glyph} ${i18n._(tierDef.label)}`}
          </span>
        ) : (
          kind !== undefined && (
            <span className={`shrink-0 font-bold whitespace-nowrap ${KIND_TONE[kindTone]}`}>{kind}</span>
          )
        )}
        {meta !== undefined && (
          <span className={`min-w-0 truncate ${gate ? 'text-ink-3' : 'text-ink-faint'}`}>{gate ? meta : `· ${meta}`}</span>
        )}
        {expires !== undefined && <span className="ml-auto shrink-0 text-brand">{expires}</span>}
      </div>

      {gate ? (
        <h3 className="mt-[10px] text-[14px] leading-[1.4] font-medium text-balance text-ink">{title}</h3>
      ) : (
        <h2 className="mt-[10px] text-[20px] leading-[1.3] font-semibold tracking-[-0.015em] text-balance text-ink">
          {title}
        </h2>
      )}

      {code !== undefined && (
        <code
          className={`block rounded-[6px] border border-line px-[10px] py-2 font-mono text-[12px] whitespace-pre-wrap [overflow-wrap:anywhere] text-ink-2 ${
            gate ? 'mt-2 bg-bg' : 'mt-[10px] max-w-[642px] bg-surface leading-[normal]'
          }`}
        >
          {code}
        </code>
      )}
    </>
  );

  return (
    <div
      className={
        gate
          ? `mt-[18px] rounded-[10px] border bg-bg px-4 py-[14px] ${tierDef?.border ?? 'border-line-strong'}`
          : 'flex min-h-full flex-col'
      }
    >
      {gate ? (
        head
      ) : (
        <DetailHeader bleed>{head}</DetailHeader>
      )}

      {body !== undefined && (
        <div
          className={`max-w-[70ch] ${
            gate ? 'mt-[10px] text-[13px] leading-[1.55] text-ink-3' : 'mt-4 text-[13.5px] leading-[1.6] text-ink-2'
          }`}
        >
          {body}
        </div>
      )}

      {facts !== undefined && (
        <div className="mt-2 font-mono text-[10.5px] leading-[1.6] text-ink-faint">{facts}</div>
      )}

      {options !== undefined && options.length > 0 && (
        <div
          role="radiogroup"
          aria-label={title}
          className={`flex flex-col gap-1 ${gate ? 'mt-3 max-w-[620px]' : 'mt-[14px] max-w-[640px]'}`}
        >
          {options.map((o) => {
            const on = o.id === picked;
            return (
              <label
                key={o.id}
                className={`grid cursor-pointer grid-cols-[14px_minmax(0,1fr)] items-start gap-[10px] rounded-[8px] border px-3 py-[9px] ${
                  on ? 'border-brand bg-brand-bg' : 'border-line bg-transparent'
                }`}
              >
                <input
                  type="radio"
                  name={title}
                  checked={on}
                  onChange={() => onPick?.(o.id)}
                  className="sr-only"
                />
                <span
                  aria-hidden="true"
                  className={`mt-[3px] inline-block h-3 w-3 rounded-full ${
                    on ? 'border-4 border-brand' : 'border-[1.5px] border-line-strong'
                  }`}
                />
                <span className="block min-w-0">
                  <span className="block text-[13px] leading-[1.4] font-medium text-ink">{o.label}</span>
                  {o.consequence !== undefined && (
                    <span className="mt-0.5 block text-[12px] leading-[1.45] text-ink-dim">{o.consequence}</span>
                  )}
                </span>
              </label>
            );
          })}
        </div>
      )}

      {note !== undefined && (
        <input
          value={note.value}
          onChange={(e) => note.onChange(e.target.value)}
          placeholder={note.placeholder}
          aria-label={note.placeholder}
          className="mt-[10px] w-full max-w-[640px] rounded-[7px] border border-line bg-bg px-[10px] py-[7px] text-[12.5px] leading-[normal] text-ink outline-none"
        />
      )}

      <div className={`flex flex-wrap items-center gap-2 ${gate ? 'mt-[14px]' : 'mt-auto pt-[22px]'}`}>
        <button
          type="button"
          onClick={primary.onClick}
          disabled={primary.disabled}
          className={`max-w-[460px] truncate rounded-[7px] border font-mono text-[11.5px] leading-[normal] font-bold disabled:opacity-50 ${
            gate ? 'px-3 py-1.5' : 'px-[13px] py-[7px]'
          } ${primaryTone}`}
        >
          {primaryLabel} <span className="font-normal opacity-55">e</span>
        </button>
        {extra !== undefined && (
          <button type="button" onClick={extra.onClick} disabled={extra.disabled} className={quietButton(gate)}>
            {extra.label}
          </button>
        )}
        {secondary !== undefined && (
          <button type="button" onClick={secondary.onClick} disabled={secondary.disabled} className={quietButton(gate)}>
            {secondary.label} <span className="opacity-55">x</span>
          </button>
        )}
        {link !== undefined && (
          <button
            type="button"
            onClick={link.onClick}
            className="ml-auto font-mono text-[10.5px] leading-[normal] text-ink-faint transition-colors hover:text-brand"
          >
            {link.label}
          </button>
        )}
        {foot !== undefined && (
          <span className="ml-auto font-mono text-[10.5px] leading-[normal] text-ink-faint">{foot}</span>
        )}
      </div>
    </div>
  );
}
