// The strip of numbers at the top of a detail (handoff §5): a mono 17px value, a
// 9.5px label under it, and on the numbers that have a limit a 64px bar. It stands
// in for the chips the old screens scattered around a title.

export type NumberTone = 'default' | 'faint' | 'brand' | 'amber' | 'green' | 'red';

const VALUE_TONE: Record<NumberTone, string> = {
  default: 'text-ink',
  faint: 'text-ink-faint',
  brand: 'text-brand',
  amber: 'text-amber',
  green: 'text-green',
  red: 'text-red',
};

const BAR_TONE: Record<NumberTone, string> = {
  default: 'bg-ink-dim',
  faint: 'bg-ink-faint',
  brand: 'bg-brand',
  amber: 'bg-amber',
  green: 'bg-green',
  red: 'bg-red',
};

export interface NumberItem {
  label: string;
  value: string;
  tone?: NumberTone;
  /** 0–100 draws the bar; absent means no bar. */
  barPercent?: number;
}

export function NumbersStrip({ items }: { items: readonly NumberItem[] }): JSX.Element {
  return (
    <div className="flex flex-wrap items-end gap-[26px]">
      {items.map((item) => {
        const tone = item.tone ?? 'default';
        return (
          <div key={item.label} className="min-w-16">
            <div className={`font-mono text-[17px] leading-none font-medium ${VALUE_TONE[tone]}`}>{item.value}</div>
            <div className="mt-1 font-mono text-[9.5px] leading-[normal] tracking-[0.06em] text-ink-faint uppercase">
              {item.label}
            </div>
            {item.barPercent !== undefined && (
              <div className="mt-[5px] h-[3px] w-16 overflow-hidden rounded-[2px] bg-line">
                <div
                  className={`h-full ${BAR_TONE[tone]}`}
                  style={{ width: `${String(Math.min(Math.max(item.barPercent, 0), 100))}%` }}
                />
              </div>
            )}
          </div>
        );
      })}
    </div>
  );
}
