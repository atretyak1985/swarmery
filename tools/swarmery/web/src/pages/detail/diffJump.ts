// Jump the Diffs tab to one file's group. Shared by the tab-switch effect in
// SessionDetail (rail click from another tab) and the direct rail click while
// the Diffs tab is already open — that second path used to be a no-op, because
// setTab('diffs') on the open tab changes nothing and the effect never re-ran.

/**
 * Scrolls the diff group rendered for `path` into view inside `panel`.
 * Returns false when no group for that path is rendered (nothing scrolled).
 * Matches on the dataset value rather than an attribute selector so a path
 * with quotes or backslashes needs no CSS escaping.
 */
export function scrollToDiffGroup(panel: HTMLElement, path: string): boolean {
  for (const el of panel.querySelectorAll<HTMLElement>('[data-diff-path]')) {
    if (el.dataset.diffPath === path) {
      el.scrollIntoView({ block: 'start' });
      return true;
    }
  }
  return false;
}
