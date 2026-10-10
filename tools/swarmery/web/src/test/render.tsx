// Test render with the app's i18n provider (plan D4). Components wrapped in
// <Trans> or calling useLingui() need an I18nProvider above them; tests import
// render, renderHook (and everything else from Testing Library) from here
// instead of '@testing-library/react', so no test has to mount the provider
// itself. renderStatic does the same for react-dom/server markup.
//
// The singleton is already active in the source locale (importing ../i18n
// does that), so assertions on English text keep matching.

import { I18nProvider } from '@lingui/react';
import {
  type RenderHookOptions,
  type RenderHookResult,
  type RenderOptions,
  type RenderResult,
  render as rtlRender,
  renderHook as rtlRenderHook,
} from '@testing-library/react';
import type { JSXElementConstructor, ReactElement, ReactNode } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { i18n } from '../i18n';

export * from '@testing-library/react';

type Wrapper = JSXElementConstructor<{ children: ReactNode }>;

function I18nWrapper({ children }: { children: ReactNode }): JSX.Element {
  return <I18nProvider i18n={i18n}>{children}</I18nProvider>;
}

/** I18nProvider outside the caller's own wrapper, if any. */
function withI18n(Inner: Wrapper | undefined): Wrapper {
  if (!Inner) return I18nWrapper;
  return ({ children }: { children: ReactNode }) => (
    <I18nWrapper>
      <Inner>{children}</Inner>
    </I18nWrapper>
  );
}

/** Testing Library's render with I18nProvider outside any caller wrapper. */
export function render(ui: ReactElement, options?: RenderOptions): RenderResult {
  return rtlRender(ui, { ...options, wrapper: withI18n(options?.wrapper) });
}

/** Testing Library's renderHook with I18nProvider outside any caller wrapper. */
export function renderHook<Result, Props>(
  callback: (initialProps: Props) => Result,
  options?: RenderHookOptions<Props>,
): RenderHookResult<Result, Props> {
  return rtlRenderHook(callback, { ...options, wrapper: withI18n(options?.wrapper) });
}

/** react-dom/server's renderToStaticMarkup inside I18nProvider. */
export function renderStatic(ui: ReactElement): string {
  return renderToStaticMarkup(<I18nWrapper>{ui}</I18nWrapper>);
}
