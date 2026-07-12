import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';
import { defineConfig } from 'vite';

// Dev proxy targets the Go daemon on its default port (REST + /api/ws).
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
