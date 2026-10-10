// The i18n literal budget (plan D3): how many user-visible strings may still
// bypass Lingui. src/i18n/literals.test.ts holds scripts/i18n-literals.mjs to
// it; extraction legs read it to see what is left. It only goes down — lower
// it to the new count whenever wrapping strings reduces it, until it is 0.
//
// Measured 2026-10-10 after the phase-0 scanner fixes (data-tip and label
// props visible, macros recognised by import, .ts view models counted).

/** Unwrapped literals allowed today; the extraction phase takes it to 0. */
export const I18N_LITERALS_BUDGET = 3357;
