import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';
import { defineConfig } from 'vite';

// Dev proxy targets the Go daemon on its default port (REST + /api/ws). It
// forwards the browser's Origin and Host (http://localhost:5173), which the
// daemon accepts only once opted in via SWARMERY_TRUSTED_ORIGINS: `make dev`
// sets it; a daemon started by hand needs the same, or writes and /api/ws 403.
export default defineConfig({
  plugins: [react(), tailwindcss()],
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
