// Surfaces parked until they are worth showing again (2026-10-02, the operator's
// call). Parking hides only the visible ways in: the Plans place tab strip, the
// project overview's "open board" link and Agent Hub's "Run now". The pages, their
// routes and a direct `?tab=board|playbooks` link keep working, and the dispatcher
// keeps running board cards. Remove an id here to bring the surface back.
export const PARKED_PLANS_TABS: ReadonlySet<string> = new Set(['board', 'playbooks']);

/** True while the Board surface is parked (its tab, overview link and Run now). */
export const boardParked = (): boolean => PARKED_PLANS_TABS.has('board');
