// Whether a project list includes projects that were never onboarded onto
// swarmery. `managed` (enabledPlugins["core@swarmery"] === true in
// .claude/settings.json) is necessary but NOT sufficient: a multi-repo
// umbrella project (Skygor: onboarded once at its own root) has that same
// settings.json copied verbatim into every sub-repo it declares (sk-next,
// scripts, dk-infrastructure, …) — real live example, caught 2026-09-24. Each
// sub-repo reads back `managed: true` even though nobody ran onboarding on it
// specifically; it inherited the umbrella's file.
//
// isOnboarded() therefore also demotes a managed project whose path is
// nested under another managed project's path — treating it as "covered by
// the umbrella" rather than independently onboarded. This is a PROXY, not the
// real signal: the semantically correct test is whether onboarding claimed
// THIS path as its own identity (its own `project.json` name, its own
// `~/swarmery-workspace/<name>/` namespace) — visible on disk, but not
// exposed by the API today. Nesting happens to produce the right answer for
// every case seen so far; a project genuinely and separately onboarded
// underneath another one would be wrongly demoted by this proxy. The clean
// fix is a small backend addition (surface project.json's own name, or an
// "independently onboarded" bit) so the client can stop guessing from paths.
//
// The onboarded-only preference is persisted in localStorage under
// `swarmery.onboardedOnly` so it survives a reload — same guarded try/catch
// idiom as lib/lastProject.ts. It is read by the Projects list and
// ProjectSwitcher; components/ProjectDropdown.tsx (the header scope switcher
// / Sessions filter) is a third project picker that does NOT consult it —
// whether it should is a product call, left open deliberately rather than
// silently decided here.
//
// Default is true (narrow to onboarded projects on first load), REVERSING the
// deleted lib/showUnmanaged.ts's documented default of false ("so the
// checkbox is an opt-in narrowing, not a surprise disappearance of projects").
// That earlier reasoning was about the managed/unmanaged split; it doesn't
// carry over here; onboarded-vs-not is the split the operator actually
// wants filtered by default (see task T-rfbjjj), and starting unfiltered
// would reintroduce the exact clutter (copied-config sub-repos, telemetry-only
// discoveries) this feature exists to hide.

const KEY = 'swarmery.onboardedOnly';

/** Defaults to true: hide never-onboarded projects until the operator opts
 * into seeing everything. */
export function loadOnboardedOnly(): boolean {
  try {
    const v = window.localStorage.getItem(KEY);
    return v !== 'false';
  } catch {
    return true;
  }
}

export function saveOnboardedOnly(value: boolean): void {
  try {
    window.localStorage.setItem(KEY, String(value));
  } catch {
    // storage disabled — the in-memory choice still applies this session
  }
}

interface OnboardCheck {
  id: number;
  path: string;
  isSystem: boolean;
  archived: boolean;
  plugin: { managed: boolean } | null;
}

function isManaged(p: OnboardCheck): boolean {
  return p.plugin?.managed === true;
}

/** all is the full project list (archived rows included or not — both call
 * sites are consistent about that today, see Projects.tsx and
 * ProjectSwitcher.tsx) — needed to check whether p's path is nested under
 * another managed project.
 *
 * `other` is excluded as a candidate ancestor when it's archived (an archived
 * row shouldn't demote a live one just by sharing a path prefix) or when its
 * own path, once trailing slashes are stripped, is empty — an empty
 * `otherRoot` would make `root.startsWith(otherRoot + '/')` true for every
 * absolute path, collapsing the whole list. Neither is known to occur live
 * today; both are cheap to guard against regardless. */
export function isOnboarded(p: OnboardCheck, all: readonly OnboardCheck[]): boolean {
  if (!isManaged(p)) return false;
  const root = p.path.replace(/\/+$/, '');
  const nestedUnderAnother = all.some((other) => {
    if (other.id === p.id || other.isSystem || other.archived || !isManaged(other)) return false;
    const otherRoot = other.path.replace(/\/+$/, '');
    if (otherRoot === '') return false;
    return root.startsWith(otherRoot + '/');
  });
  return !nestedUnderAnother;
}
