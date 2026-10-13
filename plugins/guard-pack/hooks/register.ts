// guard-pack: project isolation, secret redaction, the production-deploy
// guard and what the model is offered. Every guard reads its policy through
// `$` alone (no daemon), returns fast, and falls through to `next` when it
// fails: a guard that cannot judge never blocks the session. A userConfig
// option turned off registers none of its hooks.

import type { Register } from 'claude-code'
import { registerIsolation } from './isolation'
import { registerAgentOffer, registerHeavyToolDeferral } from './offer'
import { registerProdGuard } from './prodguard'
import { registerRedaction } from './redaction'
import { createContextCache, parseOptions } from './roots'

export const register: Register = (on, rawOptions) => {
  const options = parseOptions(rawOptions)
  const cache = createContextCache(options)

  // Roots and project.json are read once the session is ready, so the first
  // tool call does not pay for them; a hook that runs earlier reads them itself.
  on('session.start', async ($, e, next) => {
    const result = await next(e)
    await cache.refresh($).catch(() => undefined)
    return result
  })

  if (options.isolation) registerIsolation(on, cache)
  if (options.redaction) registerRedaction(on)
  if (options.prodGuard) registerProdGuard(on, cache)
  if (options.agentOffer) registerAgentOffer(on, cache)
  if (options.deferHeavyTools) registerHeavyToolDeferral(on)
}
