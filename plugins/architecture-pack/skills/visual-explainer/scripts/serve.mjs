#!/usr/bin/env node
// serve.mjs — serve ONE html page on 127.0.0.1 for browser verification.
//
//   node serve.mjs <page.html> [--idle <s>] [--ttl <s>] [--foreground]
//   node serve.mjs --stop <pid>
//
// Prints one JSON line and returns at once: {"url":"http://127.0.0.1:<port>/<page>","pid":<n>}.
// The server runs detached and stops by itself: after --idle seconds with no request
// (default 300) or --ttl seconds in total (default 1800), whichever comes first.
// Stop it earlier with `node serve.mjs --stop <pid>` (or `kill <pid>`).
//
// Safety: it binds 127.0.0.1 only, picks a free port, and answers ONLY the one page —
// every other path is 404, so the directory around the page (.env, .git, …) is never served.
// Responses carry Cache-Control: no-store, so a reload always shows the current file.
// Needs only node; no python, no packages.

import http from 'node:http';
import { readFileSync, statSync } from 'node:fs';
import { spawn } from 'node:child_process';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const argv = process.argv.slice(2);
const opt = (name, def) => {
  const i = argv.indexOf(name);
  return i > -1 && argv[i + 1] !== undefined ? argv[i + 1] : def;
};
const usage = () => {
  console.error('usage: node serve.mjs <page.html> [--idle <s>] [--ttl <s>]   |   node serve.mjs --stop <pid>');
  process.exit(2);
};

// --stop <pid>
if (argv.includes('--stop')) {
  const pid = Number(opt('--stop'));
  if (!Number.isInteger(pid) || pid <= 0) usage();
  try { process.kill(pid, 'SIGTERM'); console.log(JSON.stringify({ stopped: pid })); }
  catch (e) { console.log(JSON.stringify({ stopped: null, error: e.code || String(e) })); }
  process.exit(0);
}

const page = argv.find((a, i) => !a.startsWith('--') && !['--idle', '--ttl'].includes(argv[i - 1]));
if (!page) usage();
const file = path.resolve(page);
try {
  if (!statSync(file).isFile()) throw new Error('not a file');
} catch {
  console.error(`serve.mjs: ${page} is not a readable file`);
  process.exit(1);
}
const idle = Math.max(1, Number(opt('--idle', 300)));
const ttl = Math.max(idle, Number(opt('--ttl', 1800)));

// Parent: start a detached child that does the serving, relay its first line, return.
if (!argv.includes('--foreground') && !argv.includes('--child')) {
  const self = fileURLToPath(import.meta.url);
  const child = spawn(process.execPath, [self, file, '--idle', String(idle), '--ttl', String(ttl), '--child'], {
    detached: true,
    stdio: ['ignore', 'pipe', 'ignore'],
  });
  let buf = '';
  const fail = setTimeout(() => { console.error('serve.mjs: server did not start'); process.exit(1); }, 5000);
  child.stdout.on('data', (d) => {
    buf += d;
    const nl = buf.indexOf('\n');
    if (nl > -1) {
      clearTimeout(fail);
      process.stdout.write(buf.slice(0, nl + 1));
      child.stdout.destroy();
      child.unref();
      process.exit(0);
    }
  });
  child.on('exit', (code) => { clearTimeout(fail); console.error(`serve.mjs: server exited early (${code})`); process.exit(1); });
} else {
  // Child (or --foreground): serve.
  const name = path.basename(file);
  let last = Date.now();
  const server = http.createServer((req, res) => {
    last = Date.now();
    let p;
    try { p = decodeURIComponent(new URL(req.url, 'http://127.0.0.1').pathname); } catch { p = ''; }
    if (req.method !== 'GET' && req.method !== 'HEAD') { res.writeHead(405); res.end(); return; }
    if (p !== '/' + name && p !== '/') { res.writeHead(404, { 'content-type': 'text/plain' }); res.end('not found'); return; }
    let body;
    try { body = readFileSync(file); } catch { res.writeHead(500); res.end(); return; }
    res.writeHead(200, { 'content-type': 'text/html; charset=utf-8', 'cache-control': 'no-store' });
    res.end(req.method === 'HEAD' ? undefined : body);
  });
  const stop = () => server.close(() => process.exit(0));
  process.on('SIGTERM', stop);
  process.on('SIGINT', stop);
  const started = Date.now();
  setInterval(() => {
    const now = Date.now();
    if (now - last > idle * 1000 || now - started > ttl * 1000) stop();
  }, 500).unref();
  server.listen(0, '127.0.0.1', () => {
    const { port } = server.address();
    process.stdout.write(JSON.stringify({ url: `http://127.0.0.1:${port}/${encodeURIComponent(name)}`, pid: process.pid, idle, ttl }) + '\n');
  });
}
