import { projectLabel } from '../lib/format';
import { useProjectColor } from '../lib/projectColors';

/**
 * Project name rendered in the project's stable accent color. The color comes
 * from the app-wide `ProjectColorProvider`, which assigns guaranteed-distinct
 * hues across the whole project list — so every surface (session lists, detail
 * header, spine, tables) shows a given project in the same, non-colliding hue.
 *
 * `className` carries typography/layout (font, size, truncate); the color is
 * applied inline and wins over any text-color utility.
 */
export function ProjectName({
  name,
  slug,
  className,
}: {
  name: string | null | undefined;
  slug: string;
  className?: string;
}): JSX.Element {
  const colorFor = useProjectColor();
  return (
    <span className={className} style={{ color: colorFor(slug) }}>
      {projectLabel(name, slug)}
    </span>
  );
}
