// Counts the user-visible string literals in the SPA that have not gone
// through Lingui yet — the "no literal in JSX" gate of plan D3 (Biome has no
// jsx-no-literals rule). src/i18n/literals.test.ts runs it and holds the count
// to a budget (src/i18n/budget.ts), so `npm test` (and CI) fail when an
// unwrapped string lands.
//
// Usage:
//   node scripts/i18n-literals.mjs                        # JSON: {count, items:[{file,line,text}]}
//   node scripts/i18n-literals.mjs --list                 # one line per item, then the total
//   node scripts/i18n-literals.mjs --paths src/pages/today,src/pages/Overview.tsx
//                                                         # only items under these prefixes
//
// What counts (AST walk over src/**/*.{ts,tsx}):
//   - JSX text with at least two Latin letters, unless it is only units and
//     punctuation (`ms`, `px`, `KB`, `→`);
//   - a string in JSX child position (`{'Save'}`, `{busy ? 'Saving' : 'Save'}`)
//     with at least two letters, through ?: / && / || / ?? branches;
//   - the same for a visible attribute: placeholder, title, alt, aria-label,
//     aria-description, aria-valuetext, data-tip, data-title, data-label, and
//     the label props label, tip, emptyText, hint, description, caption,
//     summary — plus any camelCase prop ending in Label, Text, Title, Tip,
//     Hint, Placeholder, Description, Caption or Summary (`confirmLabel`);
//     also those keys inside a JSX spread object (`{...{'aria-label': 'Close'}}`)
//     and the hyphenated ones (aria-*, data-tip) in any object literal;
//   - in .tsx files, any other string or template literal with a space and at
//     least two words of three or more letters (labels kept in arrays,
//     ternaries, helper arguments);
//   - in .ts files under pages/, lib/, components/, workspace/, theme/ and
//     api/ (+ api.ts) — view models such as today/loopModel.ts,
//     health/healthModel.ts, lib/needsYou.ts, pages/docsRail.ts, theme
//     palettes, API error fallbacks — the same sentence rule anywhere in the
//     module; single-word `label:`/`title:`/`hint:`… keys in config objects
//     (nav, tabs, options) count there too.
//
// What does not:
//   - anything inside a Lingui macro. Macros are recognised by the file's own
//     imports from '@lingui/*/macro' (local names, aliases included:
//     `import { plural as pluralMessage }`) and by `t` / `_` destructured from
//     useLingui(); i18n._(…) / i18n.t(…) are runtime lookups. A local helper
//     that happens to be called `plural` or `t` is NOT a macro — its string
//     arguments count;
//   - code attributes: className, class, href, src, key, id, to, path, rel,
//     role, type, style, htmlFor, name, aria-describedby, aria-labelledby, any
//     other data-*, and value when it is a code-like token (no spaces);
//   - console.* arguments, `throw …` and `new …Error(…)` arguments (developer
//     messages) — EXCEPT under api.ts / api/*, whose Error messages reach the
//     UI through err.message and therefore count; `const MOCK_* = …` fixtures
//     in those modules; dynamic import() specifiers, regex sources (`new RegExp(…)`,
//     a variable named *_RE / *Re / *Regex / *Pattern), type-level strings,
//     strings without letters, Tailwind class lists;
//   - a line marked with an `i18n-ignore` comment (or the line after one).
// Allowlisted paths are skipped whole: lib/glossary.ts (tied to docs by the Go
// drift test), mock/, terminal/, i18n/, test/, tests.
//
// The parser is Vite's own parseAst (Rolldown/Oxc): TypeScript 7 ships no
// in-process compiler API, and this keeps the gate free of new dependencies.

import { readdirSync, readFileSync } from 'node:fs';
import { dirname, join, sep } from 'node:path';
import { fileURLToPath } from 'node:url';
import { parseAst } from 'vite';

const webDir = join(dirname(fileURLToPath(import.meta.url)), '..');
const srcDir = join(webDir, 'src');

const SKIP_PATHS = [/^lib\/glossary\.ts$/, /^mock\//, /^terminal\//, /^i18n\//, /^test\//];
const SKIP_FILES = /\.test\.[tj]sx?$|\.d\.ts$/;
/** .ts modules whose sentences are UI copy (view models, formatters, rails). */
const TS_SENTENCE_DIRS = /^(?:pages|lib|components|workspace|theme|api)\/|^api\.ts$/;
/** api modules: their thrown Error messages reach the UI through err.message, so they count. */
const KEEP_ERROR_ARGS = /^api(?:\.ts$|\/)/;

const VISIBLE_ATTRS = new Set([
  'placeholder',
  'title',
  'alt',
  'aria-label',
  'aria-description',
  'aria-valuetext',
  'data-tip',
  'data-title',
  'data-label',
  'label',
  'tip',
  'emptyText',
  'hint',
  'description',
  'caption',
  'summary',
]);
const VISIBLE_SUFFIX = /[a-z](?:Label|Text|Title|Tip|Hint|Placeholder|Description|Caption|Summary)$/;
/** Hyphenated keys that are visible wherever an object literal carries them. */
const VISIBLE_OBJECT_KEYS = /^(?:aria-(?:label|description|valuetext)|data-(?:tip|title|label))$/;
const CODE_ATTRS = new Set([
  'className',
  'class',
  'href',
  'src',
  'key',
  'id',
  'to',
  'path',
  'rel',
  'role',
  'type',
  'style',
  'htmlFor',
  'name',
  'aria-describedby',
  'aria-labelledby',
]);
const MACRO_SOURCE = /^@lingui\/(?:core|react)\/macro$|^@lingui\/macro$/;
const LINGUI_SOURCE = /^@lingui\//;
const REGEX_NAME = /(?:_RE|Re|RE|Regex|RegExp|Pattern|_PATTERN)$/;
const TYPE_NODES = new Set([
  'TSTypeAnnotation',
  'TSTypeAliasDeclaration',
  'TSInterfaceDeclaration',
  'TSTypeParameterInstantiation',
  'TSTypeParameterDeclaration',
  'TSLiteralType',
  'TSModuleDeclaration',
  'TSImportType',
  'TSDeclareFunction',
]);
/** Wrappers that pass a value through unchanged. */
const PASS_THROUGH = new Set([
  'ParenthesizedExpression',
  'TSAsExpression',
  'TSSatisfiesExpression',
  'TSNonNullExpression',
  'TSTypeAssertion',
]);

const LATIN = /[A-Za-z]/g;
const TEXT_LIMIT = 100;

function latinCount(text) {
  return (text.match(LATIN) ?? []).length;
}

/** A word: letters (and apostrophes) only, three or more letters, punctuation stripped. */
function wordCount(text) {
  let n = 0;
  for (const token of text.split(/\s+/)) {
    const bare = token.replace(/^[^A-Za-z]+|[^A-Za-z']+$/g, '');
    if (/^[A-Za-z][A-Za-z']*$/.test(bare) && latinCount(bare) >= 3) n += 1;
  }
  return n;
}

// Single-word Tailwind utilities; any other utility token carries - : [ / or a digit.
const UTILITY_WORDS = new Set(
  'flex grid block hidden inline contents static relative absolute fixed sticky truncate italic uppercase lowercase capitalize underline border rounded shadow grow shrink transition invisible visible outline ring isolate table container antialiased group peer'.split(
    ' ',
  ),
);

/** A Tailwind class list kept in a variable or helper (`const pill = 'rounded border …'`). */
function isClassList(text) {
  const tokens = text.split(/\s+/).filter((t) => t !== '' && t !== '{…}');
  return (
    tokens.length > 0 &&
    tokens.every((t) => UTILITY_WORDS.has(t) || (/^[!-]?[a-z0-9:.\-[\]/%#_()&>*=']+$/.test(t) && /[-:[/\d]/.test(t)))
  );
}

function isSentence(text) {
  return /\S\s+\S/.test(text) && wordCount(text) >= 2 && !isClassList(text);
}

// Units and abbreviations that stay as they are next to a number in JSX text.
const UNIT_WORDS = new Set(['ms', 's', 'm', 'h', 'd', 'w', 'px', 'k', 'x', 'b', 'kb', 'mb', 'gb', 'tb', 'kib', 'mib', 'gib']);

/** JSX text made of units, digits and punctuation only (`ms`, `{n} KB`, `→`). */
function isUnitsOnly(text) {
  const words = text.split(/[^A-Za-z]+/).filter(Boolean);
  return words.length > 0 && words.every((w) => UNIT_WORDS.has(w.toLowerCase()));
}

function literalText(node) {
  if (node.type === 'Literal' && typeof node.value === 'string') return node.value;
  if (node.type === 'TemplateLiteral') {
    return node.quasis.map((q) => q.value.cooked ?? q.value.raw).join('{…}');
  }
  return null;
}

/** Code-like `value=` token: no whitespace (an enum, an id, a number). */
function isCodeToken(text) {
  return !/\s/.test(text.trim());
}

function attrName(attr) {
  const n = attr.name;
  if (n.type === 'JSXIdentifier') return n.name;
  if (n.type === 'JSXNamespacedName') return `${n.namespace.name}:${n.name.name}`;
  return '';
}

function isVisibleAttr(name) {
  return VISIBLE_ATTRS.has(name) || VISIBLE_SUFFIX.test(name);
}

function propertyKey(prop) {
  if (prop.type !== 'Property' || prop.computed) return null;
  if (prop.key.type === 'Identifier') return prop.key.name;
  if (prop.key.type === 'Literal' && typeof prop.key.value === 'string') return prop.key.value;
  return null;
}

function elementName(el) {
  const n = el.openingElement?.name;
  return n?.type === 'JSXIdentifier' ? n.name : '';
}

function isConsoleCall(node) {
  return (
    node.type === 'CallExpression' &&
    node.callee.type === 'MemberExpression' &&
    node.callee.object.type === 'Identifier' &&
    node.callee.object.name === 'console'
  );
}

/** `new Error(…)`, `new TypeError(…)`, `new ApiError(…)`, `new RegExp(…)`. */
function isErrorOrRegexConstruction(node) {
  return (
    node.type === 'NewExpression' &&
    node.callee.type === 'Identifier' &&
    (/Error$/.test(node.callee.name) || node.callee.name === 'RegExp')
  );
}

/** In-module mock fixtures (`const MOCK_LESSONS = …` in src/api/*) are never translated. */
function isMockDeclarator(node) {
  return node.type === 'VariableDeclarator' && node.id.type === 'Identifier' && /^MOCK_/.test(node.id.name);
}

function isRegexDeclarator(node) {
  return node.type === 'VariableDeclarator' && node.id.type === 'Identifier' && REGEX_NAME.test(node.id.name);
}

/** Generic pre-order visit of every AST node (used by the macro pre-pass). */
function visit(node, fn) {
  if (node === null || typeof node !== 'object') return;
  if (Array.isArray(node)) {
    for (const child of node) visit(child, fn);
    return;
  }
  if (typeof node.type !== 'string') return;
  fn(node);
  for (const key of Object.keys(node)) {
    if (key === 'type' || key === 'start' || key === 'end' || key === 'range') continue;
    const child = node[key];
    if (child !== null && typeof child === 'object') visit(child, fn);
  }
}

/**
 * The local names that are Lingui macros in this file: every named import from
 * '@lingui/*\/macro' (aliases resolved to their local name), and every `t` / `_`
 * destructured from a useLingui() call (from the macro or the runtime package).
 */
function macroNames(ast) {
  const names = new Set();
  const useLingui = new Set();
  for (const stmt of ast.body) {
    if (stmt.type !== 'ImportDeclaration' || typeof stmt.source.value !== 'string') continue;
    const source = stmt.source.value;
    if (!LINGUI_SOURCE.test(source)) continue;
    for (const spec of stmt.specifiers) {
      if (spec.type !== 'ImportSpecifier') continue;
      const imported = spec.imported.type === 'Identifier' ? spec.imported.name : spec.imported.value;
      if (imported === 'useLingui') useLingui.add(spec.local.name);
      else if (MACRO_SOURCE.test(source)) names.add(spec.local.name);
    }
  }
  if (useLingui.size > 0) {
    visit(ast, (node) => {
      if (
        node.type === 'VariableDeclarator' &&
        node.id.type === 'ObjectPattern' &&
        node.init?.type === 'CallExpression' &&
        node.init.callee.type === 'Identifier' &&
        useLingui.has(node.init.callee.name)
      ) {
        for (const prop of node.id.properties) {
          const key = propertyKey(prop);
          if ((key === 't' || key === '_') && prop.value.type === 'Identifier') names.add(prop.value.name);
        }
      }
    });
  }
  return names;
}

function lineStarts(code) {
  const starts = [0];
  for (let i = 0; i < code.length; i += 1) if (code.charCodeAt(i) === 10) starts.push(i + 1);
  return starts;
}

function lineOf(starts, offset) {
  let lo = 0;
  let hi = starts.length - 1;
  while (lo < hi) {
    const mid = (lo + hi + 1) >> 1;
    if (starts[mid] <= offset) lo = mid;
    else hi = mid - 1;
  }
  return lo + 1;
}

function clip(text) {
  const flat = text.replace(/\s+/g, ' ').trim();
  return flat.length > TEXT_LIMIT ? `${flat.slice(0, TEXT_LIMIT - 1)}…` : flat;
}

function scanFile(abs, rel) {
  const code = readFileSync(abs, 'utf8');
  const isTsx = rel.endsWith('.tsx');
  const sentenceRule = isTsx || TS_SENTENCE_DIRS.test(rel);
  const ast = parseAst(code, { lang: isTsx ? 'tsx' : 'ts' }, abs);
  const macros = macroNames(ast);
  const starts = lineStarts(code);
  const lines = code.split('\n');
  const found = new Map(); // start offset → item (a node is counted once)

  const ignored = (line) =>
    (lines[line - 1] ?? '').includes('i18n-ignore') || (lines[line - 2] ?? '').includes('i18n-ignore');

  const add = (node, text) => {
    if (found.has(node.start)) return;
    const line = lineOf(starts, node.start);
    if (ignored(line)) return;
    found.set(node.start, { file: `src/${rel}`, line, col: node.start - starts[line - 1], text: clip(text) });
  };

  const isMacroCall = (node) => {
    if (node.type === 'TaggedTemplateExpression') return node.tag.type === 'Identifier' && macros.has(node.tag.name);
    if (node.type !== 'CallExpression') return false;
    const c = node.callee;
    if (c.type === 'Identifier') return macros.has(c.name);
    // i18n._(…) / i18n.t(…) — a runtime lookup, already translatable.
    return (
      c.type === 'MemberExpression' &&
      c.object.type === 'Identifier' &&
      c.object.name === 'i18n' &&
      c.property.type === 'Identifier' &&
      (c.property.name === '_' || c.property.name === 't')
    );
  };

  /** Count every string an expression can evaluate to (JSX child / visible attribute). */
  const addShown = (expr) => {
    if (!expr || typeof expr !== 'object') return;
    if (PASS_THROUGH.has(expr.type)) addShown(expr.expression);
    else if (expr.type === 'ConditionalExpression') {
      addShown(expr.consequent);
      addShown(expr.alternate);
    } else if (expr.type === 'LogicalExpression') {
      if (expr.operator !== '&&') addShown(expr.left);
      addShown(expr.right);
    } else if (expr.type === 'SequenceExpression') addShown(expr.expressions.at(-1));
    else {
      const text = literalText(expr);
      if (text !== null && latinCount(text) >= 2) add(expr, text);
    }
  };

  const addObjectLabels = (obj, keyIsVisible) => {
    for (const prop of obj.properties) {
      const key = propertyKey(prop);
      if (key !== null && keyIsVisible(key)) addShown(prop.value);
    }
  };

  // inMacro: the node sits inside a Lingui macro, so it is already a message.
  const walk = (node, inMacro, parent) => {
    if (node === null || typeof node !== 'object') return;
    if (Array.isArray(node)) {
      for (const child of node) walk(child, inMacro, parent);
      return;
    }
    if (typeof node.type !== 'string') return;
    if (TYPE_NODES.has(node.type) || node.type === 'ImportDeclaration' || node.type === 'ExportAllDeclaration') return;
    if (node.type === 'ExportNamedDeclaration' && node.source) return;
    if (node.type === 'ImportExpression') return;
    if (node.type === 'ThrowStatement' && !KEEP_ERROR_ARGS.test(rel)) return;
    if (isConsoleCall(node) || isRegexDeclarator(node) || isMockDeclarator(node)) return;
    if (isErrorOrRegexConstruction(node) && !(KEEP_ERROR_ARGS.test(rel) && /Error$/.test(node.callee.name))) return;

    const next =
      inMacro || isMacroCall(node) || (node.type === 'JSXElement' && macros.has(elementName(node)));

    if (!next) {
      if (node.type === 'JSXText' && latinCount(node.value) >= 2 && !isUnitsOnly(node.value)) add(node, node.value);
      if (
        node.type === 'JSXExpressionContainer' &&
        (parent?.type === 'JSXElement' || parent?.type === 'JSXFragment')
      ) {
        addShown(node.expression);
      }
      if (node.type === 'JSXSpreadAttribute' && node.argument.type === 'ObjectExpression') {
        addObjectLabels(node.argument, isVisibleAttr);
      }
      // single-word labels in config objects (nav, tabs, options) are visible copy too
      if (node.type === 'ObjectExpression') {
        addObjectLabels(node, (key) => VISIBLE_OBJECT_KEYS.test(key) || (sentenceRule && isVisibleAttr(key)));
      }
      if (node.type === 'JSXAttribute') {
        const name = attrName(node);
        const v = node.value?.type === 'JSXExpressionContainer' ? node.value.expression : node.value;
        if (isVisibleAttr(name)) {
          addShown(v);
        } else {
          if (CODE_ATTRS.has(name) || name.startsWith('data-')) return;
          if (name === 'value' && v) {
            const text = literalText(v);
            if (text !== null && isCodeToken(text)) return;
          }
        }
      }
      if (sentenceRule && (node.type === 'Literal' || node.type === 'TemplateLiteral')) {
        const text = literalText(node);
        if (text !== null && isSentence(text)) add(node, text);
      }
    }

    for (const key of Object.keys(node)) {
      if (key === 'type' || key === 'start' || key === 'end' || key === 'range') continue;
      const child = node[key];
      if (child !== null && typeof child === 'object') walk(child, next, node);
    }
  };

  walk(ast, false, null);
  return [...found.values()];
}

function sourceFiles() {
  return readdirSync(srcDir, { recursive: true })
    .map((p) => String(p).split(sep).join('/'))
    .filter((p) => /\.tsx?$/.test(p) && !SKIP_FILES.test(p) && !SKIP_PATHS.some((re) => re.test(p)))
    .sort();
}

/** `--paths a,b` → item filter; a prefix is a file or a folder (`src/pages/today`). */
function pathFilter(argv) {
  const i = argv.indexOf('--paths');
  if (i === -1) return () => true;
  const prefixes = (argv[i + 1] ?? '')
    .split(',')
    .map((p) => p.trim().replace(/^\.\//, '').replace(/\/+$/, ''))
    .filter(Boolean);
  if (prefixes.length === 0) {
    console.error('i18n-literals: --paths needs a comma-separated list, e.g. --paths src/pages/today');
    process.exit(2);
  }
  return (file) => prefixes.some((p) => file === p || file.startsWith(`${p}/`));
}

// `node … | head` closes the pipe early; that is not an error.
process.stdout.on('error', (err) => {
  if (err.code === 'EPIPE') process.exit(0);
  throw err;
});

const keep = pathFilter(process.argv);
const items = sourceFiles()
  .flatMap((rel) => scanFile(join(srcDir, rel), rel))
  .filter((it) => keep(it.file))
  .sort((a, b) => (a.file < b.file ? -1 : a.file > b.file ? 1 : a.line - b.line || a.col - b.col))
  .map(({ file, line, text }) => ({ file, line, text }));

if (process.argv.includes('--list')) {
  for (const it of items) console.log(`${it.file}:${it.line}\t${it.text}`);
  console.log(`total: ${items.length}`);
} else {
  process.stdout.write(`${JSON.stringify({ count: items.length, items }, null, 2)}\n`);
}
