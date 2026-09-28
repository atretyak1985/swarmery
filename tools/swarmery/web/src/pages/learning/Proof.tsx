// Learning → Proof, "Because of you" (Canvas v3 phase 6, artboard 1f). The
// feed of the operator's decisions and their measured effect has no backend yet
// (decision D2), so this ships as the teaching empty state: what a row will be,
// and when it gets verified. It performs no fetch.

import { Link } from 'react-router-dom';

export function Proof({ inboxHref }: { inboxHref: string }): JSX.Element {
  return (
    <section aria-labelledby="proof-heading" className="max-w-[64ch]">
      <h2
        id="proof-heading"
        className="m-0 font-display text-[22px] leading-[1.15] font-medium tracking-[-0.01em] text-ink"
      >
        Because of you
      </h2>
      <p className="mt-[6px] text-[13px] text-ink-dim">
        Each row is something you accepted, what the system did with it, and whether the number it was
        meant to move actually moved. Verification needs a week of traffic and a 20 % change.
      </p>
      <div className="relative mt-5 pl-[22px]">
        <span aria-hidden className="absolute top-[6px] bottom-[6px] left-[5px] w-px bg-line" />
        <div className="relative rounded-xl border border-dashed border-line-strong px-[14px] py-3">
          <span
            aria-hidden
            className="absolute top-4 -left-[22px] size-[11px] rounded-full border-2 border-bg bg-line-strong"
          />
          <p className="text-[13.5px] font-medium text-ink-3">Nothing to show yet.</p>
          <p className="mt-[3px] text-[12.5px] leading-normal text-ink-faint">
            Each row will be something you accepted, what the system did with it, and whether the number
            it was meant to move actually moved — verification needs a week of traffic and a 20 % change.
          </p>
          <Link to={inboxHref} className="mt-2 inline-block font-mono text-[10.5px] text-brand hover:underline">
            decide something in the Inbox →
          </Link>
        </div>
      </div>
    </section>
  );
}
