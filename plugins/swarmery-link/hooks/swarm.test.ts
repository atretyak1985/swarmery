import type { On } from 'claude-code'
import { type Engine, expect, test } from 'claude-code/testing'
import { decide, parseCommand } from './lib/control'
import {
  MAX_ENTRY_BYTES,
  MAX_RELEASE_PATHS,
  type PendingSend,
  type ModeBook,
  absolutePath,
  addClaim,
  claimConflictsOf,
  claimPath,
  claimsKey,
  clip,
  conflictLines,
  conflictVerdict,
  isAmbiguousSend,
  modeOf,
  noteMode,
  pendingFor,
  releasePayload,
  sendPayload,
  senderPid,
  unwrapEnvelope,
  wireCommandId,
  withBeneath,
} from './lib/swarm'
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

/** A peer message as the engine (Claude Code 2.1.303) hands it to the receiver. */
const ENVELOPED = '<cross-session-message from="uds:/tmp/cc-socks/41881.sock" from-name="proj-10" from-mode="bypass">\nping-from-A\n</cross-session-message>'

test('session.receive: the engine\'s cross-session envelope is taken off; the sender\'s pid goes as fromPid', ON, async ($, on) => {
  const w = await begin($, on)
  const peer = { kind: 'peer', from: 'uds:/tmp/cc-socks/41881.sock', verifiedPeerPid: 41881, name: 'proj-10', fromMode: 'bypass', body: 'ping-from-A' }
  const r = await $.session.receive({ origin: peer, text: ENVELOPED })
  await $.session.receive({ origin: { kind: 'peer' }, text: ENVELOPED.replace('41881', '41882') })
  await $.session.end(END)
  expect(r).toEqual({ text: ENVELOPED })
  expect(payloads(sent(w.requests), 'session.receive')).toEqual([
    { text: 'ping-from-A', truncated: false, originKind: 'peer', fromPid: 41881 },
    { text: 'ping-from-A', truncated: false, originKind: 'peer', fromPid: 41882 },
  ])
  expect(w.shown).toContain('swarmery-link: from peer: ping-from-A')
  expect(w.shown.some(l => l.includes('cross-session-message'))).toBe(false)
})

test('unwrapEnvelope: any attribute order, extra attributes, whitespace; anything else is not an envelope', () => {
  expect(unwrapEnvelope(ENVELOPED)).toEqual({ body: 'ping-from-A', from: 'uds:/tmp/cc-socks/41881.sock' })
  expect(unwrapEnvelope(" \r\n<cross-session-message from-mode='x' x-new=\"1\"  from='uds:/s/7.sock' >\r\n\t multi\nline \n</cross-session-message >\n")).toEqual({
    body: 'multi\nline',
    from: 'uds:/s/7.sock',
  })
  expect(unwrapEnvelope('<cross-session-message>x</cross-session-message>')).toEqual({ body: 'x', from: undefined })
  expect(unwrapEnvelope('please rebase')).toBeUndefined()
  expect(unwrapEnvelope(`fwd: ${ENVELOPED}`)).toBeUndefined()
  expect(unwrapEnvelope('<cross-session-message from="a">no close')).toBeUndefined()
  expect(unwrapEnvelope('<cross-session-messages from="a">x</cross-session-messages>')).toBeUndefined()
  expect(senderPid({ verifiedPeerPid: 7, from: 'uds:/s/8.sock' }, 'uds:/s/9.sock')).toBe(7)
  expect(senderPid({ from: 'uds:/s/8.sock' }, 'uds:/s/9.sock')).toBe(8)
  expect(senderPid({ verifiedPeerPid: -1 }, 'uds:/s/9.sock')).toBe(9)
  expect(senderPid({ kind: 'peer' }, 'tcp://127.0.0.1:9')).toBeUndefined()
  expect(senderPid(undefined, undefined)).toBeUndefined()
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
  expect(logged[0]).toEqual({ to: OTHER, text: 'please pull main', truncated: false, isDelivered: true, origin: 'plugin', commandId: 30 })
})

test('message command not delivered: acked failed "undeliverable: <reason>"', BOTH, async ($, on) => {
  const w = await begin($, on, { polls: [message(31, OTHER, 'hello?'), 'hang'], sendReplies: [{ isDelivered: false, reason: 'session is not running' }] })
  await w.clock.settle()
  expect(w.acks).toEqual([{ url: ACK_URL(31), body: { result: 'failed', message: 'undeliverable: session is not running' } }])
  await $.session.end(END)
  expect(payloads(sent(w.requests), 'session.send')).toEqual([
    expect.objectContaining({ isDelivered: false, reason: 'session is not running', commandId: 31 }),
  ])
})

test('message command whose send settles after the 10 s timeout: acked failed at 10 s, the late send still carries the command id', BOTH, async ($, on) => {
  const w = await begin($, on, { polls: [message(32, OTHER, 'still there?'), 'hang'], sendReplies: [{ result: { isDelivered: true }, afterMs: 15_000 }] })
  await w.clock.settle()
  expect(w.sends).toHaveLength(1)
  await w.clock.advance(10_000)
  expect(w.acks).toEqual([{ url: ACK_URL(32), body: { result: 'failed', message: 'undeliverable: no answer in 10s' } }])
  // the send has not settled: nothing is reported for it yet
  expect(payloads(sent(w.requests), 'session.send')).toEqual([])
  await w.clock.advance(5_000)
  await $.session.end(END)
  expect(payloads(sent(w.requests), 'session.send')).toEqual([
    { to: OTHER, text: 'still there?', truncated: false, isDelivered: true, origin: 'plugin', commandId: 32 },
  ])
})

test('two dashboard messages, the first one late: each send is reported once, with its own command id', BOTH, async ($, on) => {
  const w = await begin($, on, {
    polls: [message(33, OTHER, 'first'), message(34, THIRD, 'second'), 'hang'],
    sendReplies: [{ result: { isDelivered: false, reason: 'session is not running' }, afterMs: 12_000 }, { isDelivered: true }],
  })
  await w.clock.settle()
  await w.clock.advance(10_000)
  await w.clock.settle()
  expect(w.acks).toEqual([
    { url: ACK_URL(33), body: { result: 'failed', message: 'undeliverable: no answer in 10s' } },
    { url: ACK_URL(34), body: { result: 'done' } },
  ])
  await w.clock.advance(2_000)
  await $.session.end(END)
  expect(payloads(sent(w.requests), 'session.send')).toEqual([
    { to: THIRD, text: 'second', truncated: false, isDelivered: true, origin: 'plugin', commandId: 34 },
    { to: OTHER, text: 'first', truncated: false, isDelivered: false, reason: 'session is not running', origin: 'plugin', commandId: 33 },
  ])
})

test('identical-text dashboard messages to different addressees: a send the engine spells differently is never guessed, so their ids never swap', () => {
  const first: PendingSend = { commandId: '33', to: OTHER, text: 'same', isLogged: false }
  const second: PendingSend = { commandId: '34', to: THIRD, text: 'same', isLogged: false }
  const both = new Map([
    ['33', first],
    ['34', second],
  ])
  // the engine spells the recipient as given: each send finds its own message
  expect(pendingFor(both, THIRD, 'same')).toBe(second)
  expect(pendingFor(both, OTHER, 'same')).toBe(first)
  // spelled otherwise (a name, an address): two candidates, no guess — and the send hook leaves the report to each message's own path
  expect(pendingFor(both, 'proj-68', 'same')).toBeUndefined()
  expect(isAmbiguousSend(both, 'proj-68', 'same')).toBe(true)
  expect(isAmbiguousSend(both, THIRD, 'same')).toBe(false)
  // one candidate left (the other already reported): it is that one, however the engine spelled it
  const reported = new Map([
    ['33', { ...first, isLogged: true }],
    ['34', second],
  ])
  expect(pendingFor(reported, 'proj-68', 'same')).toBe(second)
  expect(isAmbiguousSend(reported, 'proj-68', 'same')).toBe(false)
  // another text, or nothing pending: not a dashboard message
  expect(pendingFor(both, 'proj-68', 'other text')).toBeUndefined()
  expect(isAmbiguousSend(both, 'proj-68', 'other text')).toBe(false)
  expect(isAmbiguousSend(new Map(), 'proj-68', 'same')).toBe(false)
})

test('commandId goes out as a JSON number when the id is a safe integer, else as given', () => {
  expect(wireCommandId('30')).toBe(30)
  expect(wireCommandId('9007199254740993')).toBe('9007199254740993')
  expect(wireCommandId('abc')).toBe('abc')
  expect(wireCommandId('1e3')).toBe('1e3')
  expect(sendPayload({ to: OTHER, text: 'x', origin: 'plugin', isDelivered: true, commandId: '7' })).toEqual({
    to: OTHER,
    text: 'x',
    truncated: false,
    isDelivered: true,
    origin: 'plugin',
    commandId: 7,
  })
})

test('a turn that claimed more paths than one release takes ends in release {all:true}', ON, async ($, on) => {
  expect(releasePayload(['/a'])).toEqual({ paths: ['/a'] })
  const atCap = Array.from({ length: MAX_RELEASE_PATHS }, (_, i) => `/p/${i}.ts`)
  expect(releasePayload(atCap)).toEqual({ paths: atCap })
  expect(releasePayload([...atCap, '/p/extra.ts'])).toEqual({ all: true })
  const w = await begin($, on)
  await $.turn.start(TURN)
  for (let i = 0; i <= MAX_RELEASE_PATHS; i += 1) await $.tool.call(edit(`${CWD}/src/f${i}.ts`))
  await $.turn.complete(COMPLETE)
  expect(w.store.has(claimsKey(SESSION_ID))).toBe(false)
  await $.session.end(END)
  // never a release the daemon would refuse (> 500 paths); the session's own end adds {all:true}
  expect(payloads(sent(w.requests), 'release')).toEqual([{ all: true }, { all: true }])
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

// ── claimConflicts: a write another live session holds is asked or refused ──

const held = (setting: 'off' | 'ask' | 'deny', approvals = false) => ({ options: { swarm: true, claimConflicts: setting, approvals } }) as const
const write = (filePath: string, agentId?: string) =>
  ({ tool: 'Write', input: { file_path: filePath, content: 'x' }, tool_use_id: 'toolu_c1', ...(agentId !== undefined ? { agentId } : {}) }) as const
const HELD_REASON = `swarmery-link: ${FILE} is being edited by session ${OTHER.slice(0, 8)}`

for (const setting of ['off', 'ask', 'deny'] as const) {
  for (const mode of ['default', 'auto', undefined] as const) {
    const expected = setting === 'off' ? 'allow' : setting === 'deny' || mode === 'auto' ? 'deny' : 'ask'
    test(`claimConflicts ${setting} × mode ${mode ?? 'unknown'}: ${expected}`, held(setting), async ($, on) => {
      const w = await begin($, on, { store: foreign(OTHER, 60_000, [FILE]) })
      if (mode !== undefined) await $.classic.UserPromptSubmit({ prompt: 'go', permission_mode: mode })
      const r = await $.tool.check(write(FILE))
      expect(r.decision).toBe(expected)
      if (expected === 'allow') expect(r.reason).toBeUndefined()
      else expect(r.reason).toBe(HELD_REASON)
      // the check only reads: nothing is claimed or written ahead of the call
      expect(w.storeOps.filter(op => !op.startsWith('get') && op !== 'keys')).toEqual([])
      await $.session.end(END)
    })
  }
}

test('claimConflicts deny: a claim older than 30 min never blocks', held('deny'), async ($, on) => {
  await begin($, on, { store: foreign(OTHER, 30 * 60_000 + 1, [FILE]) })
  expect((await $.tool.check(write(FILE))).decision).toBe('allow')
})

test("claimConflicts deny: the session's own claim never blocks, nor another session's claim on another path", held('deny'), async ($, on) => {
  await begin($, on, { store: { ...foreign(SESSION_ID, 1_000, [FILE]), ...foreign(OTHER, 1_000, [`${CWD}/src/other.ts`]) } })
  expect((await $.tool.check(write(FILE))).decision).toBe('allow')
  expect((await $.tool.check(write('src/a.ts'))).decision).toBe('allow')
  expect((await $.tool.check(write(`${CWD}/src/other.ts`))).decision).toBe('deny')
})

test('claimConflicts deny: Bash and reads are never checked; a relative path resolves against the cwd', held('deny'), async ($, on) => {
  const w = await begin($, on, { store: foreign(OTHER, 1_000, [FILE]) })
  expect((await $.tool.check({ tool: 'Bash', input: { command: `echo x > ${FILE}` }, tool_use_id: 'toolu_b' })).decision).toBe('allow')
  expect((await $.tool.check({ tool: 'Read', input: { file_path: FILE }, tool_use_id: 'toolu_r' })).decision).toBe('allow')
  expect(w.storeOps).toEqual([])
  expect(await $.tool.check({ tool: 'Edit', input: { file_path: 'src/./a.ts', old_string: 'a', new_string: 'b' }, tool_use_id: 'toolu_e' })).toMatchObject({
    decision: 'deny',
    reason: HELD_REASON,
  })
})

test('claimConflicts ask: dontAsk and bypassPermissions refuse; acceptEdits, plan and default ask', held('ask'), async ($, on) => {
  await begin($, on, { store: foreign(OTHER, 1_000, [FILE]) })
  for (const [mode, decision] of [
    ['dontAsk', 'deny'],
    ['bypassPermissions', 'deny'],
    ['acceptEdits', 'ask'],
    ['plan', 'ask'],
    ['default', 'ask'],
  ] as const) {
    await $.classic.UserPromptSubmit({ prompt: 'go', permission_mode: mode })
    expect((await $.tool.check(write(FILE))).decision, mode).toBe(decision)
  }
})

test('claimConflicts deny: a deny beneath is returned untouched, with its own reason', held('deny'), async ($, on) => {
  await begin($, on, { store: foreign(OTHER, 1_000, [FILE]), verdict: { decision: 'deny', reason: 'Write(src/**) is denied' } })
  expect(await $.tool.check(write(FILE))).toMatchObject({ decision: 'deny', reason: 'Write(src/**) is denied' })
})

test("claimConflicts ask: a subagent's own mode decides its call; the mode follows the session", held('ask'), async ($, on) => {
  await begin($, on, { store: foreign(OTHER, 1_000, [FILE]) })
  await $.classic.SessionStart({ source: 'startup', permission_mode: 'default' })
  await $.classic.PostToolUse({
    tool_name: 'Read',
    tool_input: { file_path: FILE },
    tool_response: 'ok',
    tool_use_id: 'toolu_p',
    permission_mode: 'auto',
    agent_id: 'agent-7',
  })
  expect((await $.tool.check(write(FILE, 'agent-7'))).decision).toBe('deny')
  expect((await $.tool.check(write(FILE))).decision).toBe('ask')
  // an agent not yet heard from takes the main loop's mode
  expect((await $.tool.check(write(FILE, 'agent-8'))).decision).toBe('ask')
  await $.classic.UserPromptSubmit({ prompt: 'go', permission_mode: 'bypassPermissions' })
  expect((await $.tool.check(write(FILE))).decision).toBe('deny')
})

test('claimConflicts deny with approvals on: the refused write never reaches the dashboard; an ask still does', held('deny', true), async ($, on) => {
  const w = await begin($, on, { store: foreign(OTHER, 1_000, [FILE]), verdict: { decision: 'ask' } })
  expect(await $.tool.check(write(FILE))).toMatchObject({ decision: 'deny', reason: HELD_REASON })
  expect(w.permissionPosts).toEqual([])
  // a call nobody holds: the engine's ask goes to the dashboard as before
  await $.tool.check(write(`${CWD}/src/free.ts`))
  expect(w.permissionPosts).toHaveLength(1)
})

test('claimConflicts ask: the claim is made after the call ran, and the conflict line still shows', held('ask'), async ($, on) => {
  // the person's yes to the ask: an unanswered ask is a denial in the kit
  on('classic.PermissionRequest', () => ({ decision: { behavior: 'allow' } }))
  const w = await begin($, on, { store: foreign(OTHER, 1_000, [FILE]) })
  await $.turn.start(TURN)
  await $.tool.call(edit(FILE))
  expect(w.calls).toEqual(['Edit'])
  expect(w.store.get(claimsKey(SESSION_ID))).toMatchObject({ paths: [{ path: FILE, at: NOW }] })
  const r = await $.turn.complete(COMPLETE)
  await $.session.end(END)
  expect(r.text).toBe(`⚠ ${FILE} is also being edited by session ${OTHER.slice(0, 8)}`)
  expect(payloads(sent(w.requests), 'conflict')).toEqual([{ path: FILE, otherSession: OTHER }])
})

test('claimConflicts deny: a refused or failed write leaves no claim behind', held('deny'), async ($, on) => {
  const w = await begin($, on, { tools: ['deny', 'error'] })
  await $.turn.start(TURN)
  await $.tool.call(edit(FILE))
  await $.tool.call(edit(`${CWD}/src/b.ts`))
  expect(w.store.has(claimsKey(SESSION_ID))).toBe(false)
  await $.session.end(END)
  expect(payloads(sent(w.requests), 'claim')).toEqual([])
})

test("claimConflicts off: a refused write is still claimed ahead of the call (today's advisory registry)", held('off'), async ($, on) => {
  const w = await begin($, on, { tools: ['deny'] })
  await $.turn.start(TURN)
  await $.tool.call(edit(FILE))
  expect(w.store.has(claimsKey(SESSION_ID))).toBe(true)
  await $.session.end(END)
  expect(payloads(sent(w.requests), 'claim')).toEqual([{ path: FILE }])
})

test('claimConflicts without swarm: one log line, no verdict changed, no store access', { options: { claimConflicts: 'deny' } }, async ($, on) => {
  const w = await begin($, on, { store: foreign(OTHER, 1_000, [FILE]) })
  expect((await $.tool.check(write(FILE))).decision).toBe('allow')
  expect(w.shown).toContain('swarmery-link: claimConflicts needs swarm on: no write is held')
  expect(w.storeOps).toEqual([])
})

test('claimConflicts: the option, the verdict and the mode book', () => {
  expect(claimConflictsOf('ask')).toBe('ask')
  expect(claimConflictsOf('deny')).toBe('deny')
  expect(claimConflictsOf('off')).toBe('off')
  expect(claimConflictsOf('DENY')).toBe('off')
  expect(claimConflictsOf(true)).toBe('off')
  expect(claimConflictsOf(undefined)).toBe('off')
  const two = [
    { path: FILE, otherSession: OTHER },
    { path: FILE, otherSession: THIRD },
  ]
  expect(conflictVerdict('ask', undefined, two)).toEqual({ decision: 'ask', reason: `${HELD_REASON} (+1 more)` })
  expect(conflictVerdict('ask', 'auto', [])).toBeUndefined()
  expect(conflictVerdict('off', 'default', two)).toBeUndefined()
  const book: ModeBook = new Map()
  noteMode(book, { permission_mode: 'auto' })
  noteMode(book, { permission_mode: 'default', agent_id: 'a1' })
  noteMode(book, { permission_mode: 7, agent_id: 'a2' })
  noteMode(book, 'not an event')
  expect(modeOf(book, undefined)).toBe('auto')
  expect(modeOf(book, 'a1')).toBe('default')
  expect(modeOf(book, 'a2')).toBe('auto')
  expect(modeOf(new Map(), 'a1')).toBeUndefined()
})
