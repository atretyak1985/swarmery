import { describe, expect, it } from 'vitest';
import { safeHref } from './safeHref';

describe('safeHref', () => {
  it('keeps http and https URLs as given', () => {
    expect(safeHref('https://example.test/org/repo/pull/7')).toBe('https://example.test/org/repo/pull/7');
    expect(safeHref('http://127.0.0.1:7777/x')).toBe('http://127.0.0.1:7777/x');
    expect(safeHref('HTTPS://EXAMPLE.TEST/')).toBe('HTTPS://EXAMPLE.TEST/');
    expect(safeHref('  https://example.test/  ')).toBe('https://example.test/');
  });

  it('rejects script and inline-content schemes', () => {
    for (const raw of [
      'javascript:alert(1)',
      'JavaScript:alert(1)',
      ' javascript:alert(1)',
      'java\tscript:alert(1)',
      'data:text/html,<p>x</p>',
      'DATA:text/html;base64,PHA+eDwvcD4=',
      'vbscript:msgbox(1)',
      'blob:https://example.test/uuid',
      'file:///etc/hosts',
    ]) {
      expect(safeHref(raw), raw).toBeNull();
    }
  });

  it('rejects relative, protocol-relative, empty and missing values', () => {
    for (const raw of ['/sessions', 'pull/7', '//example.test/x', '', '   ', null, undefined]) {
      expect(safeHref(raw), String(raw)).toBeNull();
    }
  });

  it('admits an app deep-link scheme only when the caller names it', () => {
    const focus = 'warp://action/focus_tab?id=7';
    expect(safeHref(focus)).toBeNull();
    expect(safeHref(focus, ['warp:'])).toBe(focus);
    // Naming one extra scheme does not re-admit the web ones or a script one.
    expect(safeHref('https://example.test/', ['warp:'])).toBeNull();
    expect(safeHref('javascript:alert(1)', ['warp:'])).toBeNull();
  });
});
