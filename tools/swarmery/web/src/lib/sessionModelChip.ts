// The "fell back to X" decision for a SESSION row.
//
// `modelLast` and `modelChanged` have been on the session DTO
// (internal/api/session_model.go) and in api/types.ts since the model-drift
// work landed, and until now nothing rendered them: the daemon computed the one
// fact that explains a weaker answer and threw it away at the edge. The phase
// RUN already shows this (Plans.tsx RunModelChip); this is the same claim for
// the session that produced it, so the two read as one feature.
//
// Pure on purpose — the chip is markup, the decision is testable.

/** The short name behind a full model ID (`claude-opus-5-5` → `opus`).
 *
 * The `[1m]` marker is stripped for the LABEL only; callers keep the full
 * string for the tooltip, because that suffix is the whole difference between a
 * 200k run and a 1M one. An ID we do not know is shown verbatim rather than
 * guessed at. Mirrors Plans.tsx's phaseModelShortName; the two copies are
 * deliberate for now (see the note in the phase report) — consolidating every
 * short-name table in the app is a separate change from rendering this chip. */
export function modelShortName(id: string): string {
  const base = id.replace(/\[[^\]]*\]$/, '');
  return MODEL_SHORT_NAMES[base] ?? id;
}

const MODEL_SHORT_NAMES: Record<string, string> = {
  'claude-opus-5-5': 'opus',
  // Kept past the 5.5 cutover: sessions recorded before it still carry this id,
  // and a historical row must keep rendering as "opus".
  'claude-opus-5': 'opus',
  'claude-sonnet-5': 'sonnet',
  'claude-fable-5-1': 'fable',
};

/** The two ends of a session's model move, or null when there is nothing to
 * claim.
 *
 * `modelChanged` is the daemon's judgement and this function does not
 * second-guess it: it is already family/generation-aware server-side
 * (modelid.SameTier), which is what keeps `claude-opus-5-5` →
 * `claude-opus-5-5[1m]` — one model, two spellings — from wearing the chip. All
 * that is left here is refusing to render a half-known move. */
export function sessionModelFallback(session: {
  model: string | null;
  modelLast: string | null;
  modelChanged: boolean;
  modelFellBack: boolean;
}): { from: string; to: string; fellBack: boolean } | null {
  if (!session.modelChanged) return null;
  const { model, modelLast } = session;
  if (model === null || modelLast === null || model === '' || modelLast === '') return null;
  // Equal strings cannot be a move, whatever the flag says.
  if (model === modelLast) return null;
  // DIRECTION comes from the daemon (modelid.IsFallback), never from a tier
  // table re-derived here. An operator escalating sonnet -> opus changed model
  // but did not fall back, and saying otherwise in amber is the crying-wolf
  // this phase removed from the hooks — it must not reappear in the UI.
  return { from: model, to: modelLast, fellBack: session.modelFellBack };
}
