#!/usr/bin/env node
// validate.mjs — structural + referential validation for architecture-map.json.
// Zero dependencies. Exit 0 = valid, 1 = invalid (errors listed), 2 = usage.
import { readFileSync } from 'node:fs';

const file = process.argv[2];
if (!file) {
  console.error('usage: validate.mjs <architecture-map.json>');
  process.exit(2);
}

let map;
try {
  map = JSON.parse(readFileSync(file, 'utf8'));
} catch (e) {
  console.error(`unreadable or invalid JSON: ${e.message}`);
  process.exit(1);
}

if (map === null || typeof map !== 'object' || Array.isArray(map)) {
  console.error('invalid JSON: top-level value must be an object');
  process.exit(1);
}

const errors = [];
const isStr = (v) => typeof v === 'string' && v.length > 0;
const isArr = Array.isArray;
const slug = /^[a-z0-9-]+$/;

// --- structure ---
if (map.schemaVersion !== 1) errors.push('schemaVersion must be 1');
if (!isStr(map.analyzedAt)) errors.push('analyzedAt missing');
if (!isStr(map.analyzedAtCommit) || map.analyzedAtCommit.length < 7)
  errors.push('analyzedAtCommit missing or shorter than 7 chars');
const p = map.project ?? {};
if (!isStr(p.name)) errors.push('project.name missing');
if (!isStr(p.description)) errors.push('project.description missing');
if (!isArr(p.techStack)) errors.push('project.techStack must be an array');
if (!isArr(p.entryPoints)) errors.push('project.entryPoints must be an array');

if (!isArr(map.layers) || map.layers.length < 2) errors.push('layers: need >= 2');
if (!isArr(map.modules) || map.modules.length < 5) errors.push('modules: need >= 5');
if (!isArr(map.flows) || map.flows.length < 3) errors.push('flows: need >= 3');
if (errors.length) {
  errors.forEach((e) => console.error(`✗ ${e}`));
  process.exit(1);
}

// --- id uniqueness ---
const dupes = (list, ctx) => {
  const seen = new Set();
  for (const item of list) {
    if (!isStr(item.id) || !slug.test(item.id)) errors.push(`${ctx}: bad id "${item.id}"`);
    else if (seen.has(item.id)) errors.push(`${ctx}: duplicate id "${item.id}"`);
    seen.add(item.id);
  }
  return seen;
};
const layerIds = dupes(map.layers, 'layers');
const moduleIds = dupes(map.modules, 'modules');
dupes(map.flows, 'flows');

// --- layers: order must be an integer ---
for (const l of map.layers)
  if (!Number.isInteger(l.order)) errors.push(`layer "${l.id}": order must be an integer`);

// --- modules: required fields + refs ---
for (const m of map.modules) {
  const ctx = `module "${m.id}"`;
  if (!isStr(m.name)) errors.push(`${ctx}: name missing`);
  if (!isStr(m.path)) errors.push(`${ctx}: path missing`);
  if (!isStr(m.responsibility)) errors.push(`${ctx}: responsibility missing`);
  if (!layerIds.has(m.layer)) errors.push(`${ctx}: unknown layer "${m.layer}"`);
  if (m.dependencies !== undefined && !isArr(m.dependencies)) errors.push(`${ctx}: dependencies must be an array`);
  else for (const d of m.dependencies ?? [])
    if (!moduleIds.has(d)) errors.push(`${ctx}: unknown dependency "${d}"`);
  if (m.relatedModules !== undefined && !isArr(m.relatedModules)) errors.push(`${ctx}: relatedModules must be an array`);
  else for (const r of m.relatedModules ?? [])
    if (!moduleIds.has(r)) errors.push(`${ctx}: unknown relatedModule "${r}"`);
}

// --- flows: step refs + anchors ---
for (const f of map.flows) {
  const ctx = `flow "${f.id}"`;
  if (!isStr(f.name)) errors.push(`${ctx}: name missing`);
  if (!isStr(f.description)) errors.push(`${ctx}: description missing`);
  if (!isArr(f.steps) || f.steps.length < 2) {
    errors.push(`${ctx}: needs >= 2 steps`);
    continue;
  }
  f.steps.forEach((s, i) => {
    if (!moduleIds.has(s.from)) errors.push(`${ctx} step ${i + 1}: unknown from "${s.from}"`);
    if (!moduleIds.has(s.to)) errors.push(`${ctx} step ${i + 1}: unknown to "${s.to}"`);
    if (!isStr(s.action)) errors.push(`${ctx} step ${i + 1}: action missing`);
  });
  if (!f.steps.some((s) => isStr(s.file)))
    errors.push(`${ctx}: no step is anchored to a file — add "file" to at least one step`);
}

// --- externalServices.usedBy refs ---
for (const svc of map.externalServices ?? []) {
  if (svc.usedBy !== undefined && !isArr(svc.usedBy)) errors.push(`externalService "${svc.name}": usedBy must be an array`);
  else for (const u of svc.usedBy ?? [])
    if (!moduleIds.has(u)) errors.push(`externalService "${svc.name}": unknown usedBy "${u}"`);
}

if (errors.length) {
  errors.forEach((e) => console.error(`✗ ${e}`));
  console.error(`\n${errors.length} error(s)`);
  process.exit(1);
}
console.log(`✓ valid — ${map.modules.length} modules, ${map.layers.length} layers, ${map.flows.length} flows`);
