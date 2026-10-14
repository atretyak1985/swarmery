import type { On } from 'claude-code'
import { type Engine, expect, test } from 'claude-code/testing'
import { decide, parseCommand } from './lib/control'
import { MAX_ENTRY_BYTES, absolutePath, addClaim, claimPath, claimsKey, clip, conflictLines, withBeneath } from './lib/swarm'
import { COMPLETE, CWD, END, SESSION_ID, START, TOKEN, TURN, type PollReply, type World, sent, world } from './testkit'

const ON = { options: { swarm: true } } as const
const BOTH = { options: { swarm: true, control: true } } as const
const NOW = Date.parse('2026-10-13T10:00:00.000Z')
const OTHER = '7c1d2e3f-4a5b-4c6d-8e9f-0a1b2c3d4e5f'
const THIRD = '9e8d7c6b-5a4f-4e3d-8c2b-1a0f9e8d7c6b'
const FILE = `${CWD}/src/a.ts`
const ACK_URL = (id: number): string => `http://127.0.0.1:7777/api/link/commands/${id}/ack`
const SWARM_KINDS = ['session.send', 'session.receive', 'claim', 'release', 'conflict']

type Events = ReadonlyArray<Readonly<Record<string, unknown>>>

const payloads = (events: Events, kind: string): readonly unknown[] => events.filter(e => e.kind === kind).map(e => e.payload)

const begin = async ($: Engine, on: On, w: World = {}): Promise<ReturnType<typeof world>> => {
  const recorded = world(on, { tokens: [TOKEN], ...w })
  await $.session.start(START)
  return recorded
}

const edit = (filePath: string) => ({ tool: 'Edit', file_path: filePath, old_string: 'a', new_string: 'b' }) as const

const message = (id: number, to: string, text: string): PollReply => ({ command: { id, kind: 'message', payload: { to, text } } })

/** Another session's entry in the shared store, `ageMs` old. */
const foreign = (sessionId: string, ageMs: number, paths: readonly string[]) => ({
  [claimsKey(sessionId)]: { sessionId, cwd: '/elsewhere', updatedAt: NOW - ageMs, paths: paths.map(path => ({ path, at: NOW - ageMs })) },
})

test('swarm off: no store access, no swarm event, no message hook, and a message command is acked failed', { options: { control: true } }, async ($, on) => {
  const w = await begin($, on, { polls: [message(30, OTHER, 'hi'), 'hang'] })
  await $.turn.start(TURN)
  await w.clock.settle()
  await $.tool.call(edit(FILE))
  await $.session.send({ to: 'researcher', text: 'hello', origin: { kind: 'model' } })
  await $.session.receive({ origin: { kind: 'peer' }, text: 'ping' })
  const r = await $.turn.complete(COMPLETE)
  await $.session.end(END)
  expect(r.text).toBe('hi')
  expect(w.storeOps).toEqual([])
  expect(sent(w.requests).some(e => SWARM_KINDS.includes(String(e.kind)))).toBe(false)
  expect(w.logs.some(l => l.includes('from peer'))).toBe(false)
  // the model's own send reached the engine; the dashboard's never did
  expect(w.sends).toHaveLength(1)
  expect(w.acks).toEqual([{ url: ACK_URL(30), body: { result: 'failed', message: 'swarm option is off' } }])
})

test('session.send: logged after it settles with to, isDelivered and origin; reason only when not delivered', ON, async ($, on) => {
  const w = await begin($, on, { sendReplies: [{ isDelivered: true }, { isDelivered: false, reason: 'no agent named ghost' }] })
  const delivered = await $.session.send({ to: 'researcher', text: 'hello', origin: { kind: 'model' } })
  const refused = await $.session.send({ to: 'ghost', text: 'boo', origin: { kind: 'model' } })
  await $.session.end(END)
  expect(delivered).toEqual({ isDelivered: true })
  expect(refused).toEqual({ isDelivered: false, reason: 'no agent named ghost' })
  expect(payloads(sent(w.requests), 'session.send')).toEqual([
    { to: 'researcher', text: 'hello', truncated: false, isDelivered: true, origin: 'model' },
    { to: 'ghost', text: 'boo', truncated: false, isDelivered: false, reason: 'no agent named ghost', origin: 'model' },
  ])
})

test('session.send: a subagent\'s send is not logged; text over 4096 characters is truncated', ON, async ($, on) => {
  const w = await begin($, on)
  await $.session.send({ to: 'lead', text: 'from a subagent', origin: { kind: 'model' }, agentId: 'agent-7' })
  await $.session.send({ to: 'researcher', text: 'x'.repeat(5000), origin: { kind: 'model' } })
  await $.session.end(END)
  const logged = payloads(sent(w.requests), 'session.send') as ReadonlyArray<Readonly<Record<string, unknown>>>
  expect(logged).toHaveLength(1)
  expect(logged[0]?.to).toBe('researcher')
  expect(String(logged[0]?.text)).toHaveLength(4096)
  expect(logged[0]?.truncated).toBe(true)
  expect(w.sends).toHaveLength(2)
})

test('session.receive: logged with origin kind and plugin, one line on screen; a subagent\'s is not', ON, async ($, on) => {
  const w = await begin($, on)
  const r = await $.session.receive({ origin: { kind: 'peer', plugin: 'other-mod' }, text: 'please rebase\n  onto main' })
  await $.session.receive({ origin: { kind: 'coordinator' }, text: 'for the agent', agentId: 'agent-7' })
  await $.session.receive({ origin: { kind: 'task-notification' }, text: 'y'.repeat(200) })
  await $.session.end(END)
  expect(r).toEqual({ text: 'please rebase\n  onto main' })
  expect(payloads(sent(w.requests), 'session.receive')).toEqual([
    { text: 'please rebase\n  onto main', truncated: false, originKind: 'peer', fromPlugin: 'other-mod' },
    { text: 'y'.repeat(200), truncated: false, originKind: 'task-notification' },
  ])
  expect(w.shown).toContain('swarmery-link: from peer other-mod: please rebase onto main')
  expect(w.shown).toContain(`swarmery-link: from task-notification: ${'y'.repeat(80)}`)
  expect(w.shown.some(l => l.includes('for the agent'))).toBe(false)
})

test('an Edit claims its path under swarmery-link/claims/<sid> and queues one claim; a second Edit of it adds none', ON, async ($, on) => {
  const w = await begin($, on)
  await $.turn.start(TURN)
  await $.tool.call(edit(FILE))
  await $.tool.call(edit(FILE))
  await $.tool.call({ tool: 'Write', file_path: 'src/./b.ts', content: 'x' })
  await $.tool.call({ tool: 'Bash', command: 'ls' })
  expect(w.store.get(claimsKey(SESSION_ID))).toEqual({
    sessionId: SESSION_ID,
    cwd: CWD,
    updatedAt: NOW,
    paths: [
      { path: FILE, at: NOW },
      { path: `${CWD}/src/b.ts`, at: NOW },
    ],
  })
  expect([...w.store.keys()]).toEqual([claimsKey(SESSION_ID)])
  await $.session.end(END)
  expect(payloads(sent(w.requests), 'claim')).toEqual([{ path: FILE }, { path: `${CWD}/src/b.ts` }])
  expect(payloads(sent(w.requests), 'conflict')).toEqual([])
})

test('another session\'s live claim on the same path: a conflict event and one line beneath the answer', ON, async ($, on) => {
  const w = await begin($, on, { store: { ...foreign(OTHER, 60_000, [FILE, `${CWD}/src/z.ts`]) } })
  await $.turn.start(TURN)
  await $.tool.call(edit(FILE))
  const r = await $.turn.complete(COMPLETE)
  await $.session.end(END)
  expect(r.text).toBe(`⚠ ${FILE} is also being edited by session ${OTHER.slice(0, 8)}`)
  expect(payloads(sent(w.requests), 'conflict')).toEqual([{ path: FILE, otherSession: OTHER }])
  // the other session's key is read, never written
  expect(w.storeOps.filter(op => op.includes(OTHER))).toEqual([`get ${claimsKey(OTHER)}`])
  expect(w.store.has(claimsKey(OTHER))).toBe(true)
})

test('several conflicts: the first path and (+N more)', ON, async ($, on) => {
  const w = await begin($, on, { store: { ...foreign(OTHER, 1_000, [FILE]), ...foreign(THIRD, 1_000, [FILE, `${CWD}/src/c.ts`]) } })
  await $.turn.start(TURN)
  await $.tool.call(edit(FILE))
  await $.tool.call(edit(`${CWD}/src/c.ts`))
  const r = await $.turn.complete(COMPLETE)
  await $.session.end(END)
  expect(r.text).toBe(`⚠ ${FILE} is also being edited by session ${OTHER.slice(0, 8)} (+2 more)`)
  expect(payloads(sent(w.requests), 'conflict')).toHaveLength(3)
})

test('a foreign entry older than 30 min is stale: no conflict, the answer stands alone', ON, async ($, on) => {
  const w = await begin($, on, { store: { ...foreign(OTHER, 30 * 60_000 + 1, [FILE]) } })
  await $.turn.start(TURN)
  await $.tool.call(edit(FILE))
  const r = await $.turn.complete(COMPLETE)
  await $.session.end(END)
  expect(r.text).toBe('hi')
  expect(payloads(sent(w.requests), 'conflict')).toEqual([])
  expect(w.store.has(claimsKey(OTHER))).toBe(true)
})

test('the main loop\'s turn.complete deletes this session\'s key and queues release with the turn\'s paths', ON, async ($, on) => {
  const w = await begin($, on)
  await $.turn.start(TURN)
  await $.tool.call(edit(FILE))
  // a subagent's turn ending releases nothing
  await $.turn.complete({ ...COMPLETE, turnId: 'sub-1', agentId: 'agent-7' })
  expect(w.store.has(claimsKey(SESSION_ID))).toBe(true)
  await $.turn.complete(COMPLETE)
  expect(w.store.has(claimsKey(SESSION_ID))).toBe(false)
  // the next turn claims the same path afresh
  await $.turn.start({ text: 'again', turnId: 'turn-2' })
  await $.tool.call(edit(FILE))
  await $.turn.complete({ ...COMPLETE, turnId: 'turn-2' })
  await $.session.end(END)
  const events = sent(w.requests)
  expect(payloads(events, 'release')).toEqual([{ paths: [FILE] }, { paths: [FILE] }, { all: true }])
  expect(payloads(events, 'claim')).toEqual([{ path: FILE }, { path: FILE }])
  expect(w.store.size).toBe(0)
})

test('session.end deletes the key and queues release {all:true}, before session.end itself', ON, async ($, on) => {
  const w = await begin($, on)
  await $.turn.start(TURN)
  await $.tool.call(edit(FILE))
  await $.session.end(END)
  expect(w.store.has(claimsKey(SESSION_ID))).toBe(false)
  const kinds = sent(w.requests).map(e => e.kind)
  expect(kinds.indexOf('release')).toBeLessThan(kinds.indexOf('session.end'))
  expect(payloads(sent(w.requests), 'release')).toEqual([{ all: true }])
})

test('/clear: the old key goes at its session.end, the new session id claims under a fresh key', ON, async ($, on) => {
  const cleared = '0b6f1c2e-7d4a-4c1e-9a55-3f0e2b7c9d10'
  const w = await begin($, on)
  await $.turn.start(TURN)
  await $.tool.call(edit(FILE))
  await $.session.end({ reason: 'clear', sessionId: SESSION_ID, resume: { sessionId: SESSION_ID } as never })
  expect(w.store.size).toBe(0)
  w.moveTo(cleared)
  await $.turn.start({ text: 'after clear', turnId: 'turn-2' })
  await $.tool.call(edit(FILE))
  expect([...w.store.keys()]).toEqual([claimsKey(cleared)])
  await $.session.end({ reason: 'other', sessionId: cleared, resume: { sessionId: cleared } as never })
  const claims = sent(w.requests).filter(e => e.kind === 'claim')
  expect(claims.map(e => e.sessionId)).toEqual([SESSION_ID, cleared])
})

test('no token: the swarm stays silent too, the store untouched', ON, async ($, on) => {
  const w = world(on, {})
  await $.session.start(START)
  await $.turn.start(TURN)
  await $.tool.call(edit(FILE))
  await $.session.receive({ origin: { kind: 'peer' }, text: 'ping' })
  await $.turn.complete(COMPLETE)
  await $.session.end(END)
  expect(w.storeOps).toEqual([])
  expect(w.requests).toHaveLength(0)
})

test('message command: $.session.send to {sessionId}, acked done, logged with the command id', BOTH, async ($, on) => {
  const w = await begin($, on, { polls: [message(30, OTHER, 'please pull main'), 'hang'] })
  await $.turn.start(TURN)
  await w.clock.settle()
  // `{ sessionId }` reaches the engine spelled as the bare id, sent as this plugin
  expect(w.sends).toEqual([{ to: OTHER, text: 'please pull main', origin: { kind: 'plugin', name: 'swarmery-link' }, agentId: undefined }])
  // sent while a turn runs: a message lands in the other session's queue, not this one
  expect(w.acks).toEqual([{ url: ACK_URL(30), body: { result: 'done' } }])
  await $.session.end(END)
  const logged = payloads(sent(w.requests), 'session.send') as ReadonlyArray<Readonly<Record<string, unknown>>>
  expect(logged).toHaveLength(1)
  expect(logged[0]).toEqual({ to: OTHER, text: 'please pull main', truncated: false, isDelivered: true, origin: 'plugin', commandId: '30' })
})

test('message command not delivered: acked failed "undeliverable: <reason>"', BOTH, async ($, on) => {
  const w = await begin($, on, { polls: [message(31, OTHER, 'hello?'), 'hang'], sendReplies: [{ isDelivered: false, reason: 'session is not running' }] })
  await w.clock.settle()
  expect(w.acks).toEqual([{ url: ACK_URL(31), body: { result: 'failed', message: 'undeliverable: session is not running' } }])
  await $.session.end(END)
  expect(payloads(sent(w.requests), 'session.send')).toEqual([
    expect.objectContaining({ isDelivered: false, reason: 'session is not running', commandId: '31' }),
  ])
})

test('message commands: parsed with a recipient and text, refused without; run only with swarm on', () => {
  expect(parseCommand(JSON.stringify({ id: 4, kind: 'message', payload: { to: OTHER, text: 'hi' } }))).toEqual({ id: '4', kind: 'message', to: OTHER, text: 'hi' })
  expect(parseCommand(JSON.stringify({ id: 5, kind: 'message', payload: { text: 'hi' } }))).toEqual({ id: '5', kind: 'invalid', reason: 'message without a recipient' })
  expect(parseCommand(JSON.stringify({ id: 6, kind: 'message', payload: { to: OTHER, text: ' ' } }))).toEqual({ id: '6', kind: 'invalid', reason: 'message without text' })
  const command = { id: '4', kind: 'message', to: OTHER, text: 'hi' } as const
  expect(decide(command, new Map(), 'turn-1', true)).toEqual({ do: 'message', to: OTHER, text: 'hi' })
  expect(decide(command, new Map(), undefined, false)).toEqual({ do: 'ack', ack: { result: 'failed', message: 'swarm option is off' } })
})

test('the size cap drops the oldest paths first and keeps the entry under 64 KiB', () => {
  let entry = addClaim(undefined, SESSION_ID, CWD, '/a/first.ts', 1, 300)
  for (let i = 0; i < 10; i += 1) entry = addClaim(entry, SESSION_ID, CWD, `/a/file-${i}.ts`, 2 + i, 300)
  expect(new TextEncoder().encode(JSON.stringify(entry)).length).toBeLessThanOrEqual(300)
  expect(entry.paths.some(p => p.path === '/a/first.ts')).toBe(false)
  expect(entry.paths.at(-1)).toEqual({ path: '/a/file-9.ts', at: 11 })
  let big = addClaim(undefined, SESSION_ID, CWD, '/b/0', 0)
  for (let i = 1; i < 1_000; i += 1) big = addClaim(big, SESSION_ID, CWD, `/b/${'d'.repeat(100)}/${i}.ts`, i)
  expect(new TextEncoder().encode(JSON.stringify(big)).length).toBeLessThanOrEqual(MAX_ENTRY_BYTES)
  expect(big.paths[0]?.path).not.toBe('/b/0')
  expect(big.paths.at(-1)?.at).toBe(999)
})

test('paths: relative ones resolve against the cwd; only file-writing tools claim', () => {
  expect(absolutePath('../x/./y.ts', '/w/app')).toBe('/w/x/y.ts')
  expect(absolutePath('/w//app/z.ts/', '/ignored')).toBe('/w/app/z.ts')
  expect(claimPath('NotebookEdit', { notebook_path: 'n.ipynb' }, '/w')).toBe('/w/n.ipynb')
  expect(claimPath('MultiEdit', { file_path: '/w/m.ts' }, '/w')).toBe('/w/m.ts')
  expect(claimPath('Read', { file_path: '/w/r.ts' }, '/w')).toBeUndefined()
  expect(claimPath('Edit', { file_path: 'rel.ts' }, '')).toBeUndefined()
})

test('the beneath line: none without a conflict; a text another hook put beneath is kept', () => {
  expect(conflictLines([])).toEqual([])
  const result = { text: 'the answer' }
  expect(withBeneath(result, 'the answer', [])).toBe(result)
  expect(withBeneath(result, 'the answer', ['⚠ one'])).toEqual({ text: '⚠ one' })
  expect(withBeneath({ text: 'TL;DR: done' }, 'the answer', ['⚠ one', 'drift'])).toEqual({ text: 'TL;DR: done\n⚠ one\ndrift' })
  expect(clip(`${'a'.repeat(4095)}😀`).text).toHaveLength(4095)
})
