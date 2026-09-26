// Whether a project list includes projects that were never onboarded onto
// swarmery. The signal is the backend's `Project.onboarded` bit
// (internal/api/project_onboarded.go): `managed` alone is not enough, because
// a multi-repo umbrella copies its settings.json into every sub-repo, and
// because `~` reads back managed whenever core is enabled at user scope. The
// daemon demotes a managed row nested under another managed row, but never
// lets "/", $HOME, the System dir or an onboarding root act as that umbrella —
// exclusions the browser cannot know, which is why the rule lives server-side.
//
// The onboarded-only preference is persisted in localStorage under
// `swarmery.onboardedOnly` so it survives a reload — same guarded try/catch
// idiom as lib/lastProject.ts. It is read by the Projects list and
// ProjectSwitcher; components/ProjectDropdown.tsx (the header scope switcher
// / Sessions filter) is a third project picker that does NOT consult it —
// whether it should is a product call, left open deliberately.
//
// Default is true (narrow to onboarded projects on first load): onboarded-vs-
// not is the split the operator wants filtered by default, and starting
// unfiltered would reintroduce the clutter (copied-config sub-repos,
// telemetry-only discoveries) this feature exists to hide. The Projects page
// names how many rows the filter hides when it hides them all.

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

/** True when the daemon reports p as independently onboarded. */
export function isOnboarded(p: { onboarded?: boolean }): boolean {
  return p.onboarded === true;
}
