// Plans page tests: a phase's actions live on its own page, a click from the ledger.

import { fireEvent, screen, waitFor } from './render';

/** The accessible name of the phase page's Run. */
export const RUN_PHASE = /run this phase now/;

/** A ledger line's link, found by where it goes: names differ per fixture. */
export async function phaseLink(seq = 1): Promise<HTMLAnchorElement> {
  return waitFor(() => {
    const link = document.querySelector<HTMLAnchorElement>(`li a[href*="/phase/${String(seq)}"]`);
    if (link === null) throw new Error(`no ledger line for phase ${String(seq)}`);
    return link;
  });
}

/** Opens a phase from the ledger, on the Runs tab that holds its run pickers. */
export async function openPhaseRuns(seq = 1): Promise<void> {
  fireEvent.click(await phaseLink(seq));
  fireEvent.click(await screen.findByRole('tab', { name: /^Runs/ }));
}
