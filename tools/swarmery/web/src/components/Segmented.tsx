// A small segmented control: one pressed option out of a few. Lifted out of
// pages/Analytics.tsx so the Insights cost-source toggle (pages/health/
// ModCostPanel.tsx) uses the same control as the page's other toggles;
// aria-pressed was added on the way so a screen reader hears which is on.

export function Segmented<T extends string>({
  options,
  value,
  onChange,
  label,
}: {
  options: { v: T; label: string }[];
  value: T;
  onChange: (v: T) => void;
  /** Accessible name of the group; omit when a visible heading already names it. */
  label?: string;
}): JSX.Element {
  return (
    <div
      role="group"
      aria-label={label}
      className="inline-flex overflow-hidden rounded-[9px] border border-line-strong bg-field"
    >
      {options.map((o, i) => (
        <button
          key={o.v}
          type="button"
          aria-pressed={value === o.v}
          onClick={() => onChange(o.v)}
          className={`px-3 py-[5px] font-mono text-[11px] transition-colors ${i > 0 ? 'border-l border-line-strong' : ''} ${
            value === o.v ? 'bg-surface2 text-brand' : 'text-ink-dim hover:text-ink'
          }`}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}
