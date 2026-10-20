import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';
import { getConfig } from '@lingui/conf';
import { lingui } from '@lingui/vite-plugin';
import babel, { defineRolldownBabelPreset } from '@rolldown/plugin-babel';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';
import { defineConfig } from 'vite';

// Both Lingui plugins look for lingui.config.ts from process.cwd(), not from
// Vite's root, so `vitest --root tools/swarmery/web` run from the repo root
// died with "Lingui was unable to find a config!". Pin the config to this
// file's own directory; <rootDir> in it then resolves here whatever the cwd.
// The macro plugin is passed by absolute path, not by name: Babel resolves a
// plugin name from process.cwd() too, which has no node_modules at the root.
const linguiConfigPath = fileURLToPath(new URL('./lingui.config.ts', import.meta.url));
const linguiConfig = getConfig({ configPath: linguiConfigPath });
const linguiMacroPluginPath = createRequire(import.meta.url).resolve(
  '@lingui/babel-plugin-lingui-macro',
);

// Lingui macros (t``, <Trans>, plural()) compile to i18n._() calls through
// Babel. @vitejs/plugin-react 6 no longer runs Babel itself, so the macro
// plugin rides on @rolldown/plugin-babel — and only for files that import a
// macro: every other module keeps the native (oxc) transform, which is what
// holds the build-time budget of plan D1.
const linguiMacro = defineRolldownBabelPreset({
  preset: () => ({ plugins: [[linguiMacroPluginPath, { linguiConfig }]] }),
  rolldown: { filter: { code: /@lingui\/(?:core|react)\/macro/ } },
});

// Dev proxy targets the Go daemon on its default port (REST + /api/ws). It
// forwards the browser's Origin and Host (http://localhost:5173), which the
// daemon accepts only once opted in via SWARMERY_TRUSTED_ORIGINS: `make dev`
// sets it; a daemon started by hand needs the same, or writes and /api/ws 403.
export default defineConfig({
  plugins: [
    react(),
    babel({ presets: [linguiMacro] }),
    lingui({ configPath: linguiConfigPath }),
    tailwindcss(),
  ],
  build: {
    // Keep web/dist/.gitkeep (required by go:embed on fresh clones).
    emptyOutDir: false,
  },
  server: {
    proxy: {
      '/api': {
        target: `http://localhost:${process.env.SWARMERY_PORT ?? '7777'}`,
        ws: true,
      },
    },
  },
});
