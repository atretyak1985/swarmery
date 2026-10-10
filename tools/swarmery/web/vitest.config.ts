import { defineConfig, mergeConfig } from 'vitest/config';
import viteConfig from './vite.config';

// The web app's unit and component tests: `npm test`. They reuse the app's own
// Vite config (React plugin, Tailwind), so a test compiles exactly like the
// bundle. Every test imports describe/it/expect from 'vitest' explicitly, so
// globals stay off.
export default mergeConfig(
  viteConfig,
  defineConfig({
    test: {
      environment: 'jsdom',
      include: ['src/**/*.test.{ts,tsx}'],
      // Activates the i18n source locale for every test (src/test/setup.ts).
      setupFiles: ['src/test/setup.ts'],
    },
  }),
);
