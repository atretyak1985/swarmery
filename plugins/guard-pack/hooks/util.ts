/** Calls a display method typed void that may still hand back a promise, never letting it reject loudly. */
export const quietly = (call: () => unknown): void => {
  try {
    const r = call()
    if (r instanceof Promise) r.catch(() => undefined)
  } catch {
    // display only
  }
}
