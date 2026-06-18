import { fileURLToPath, URL } from 'node:url';
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

const r = (p: string) => fileURLToPath(new URL(p, import.meta.url));

// DevLab is a standalone IDE-style SPA. It vendors the Holistic Apple-dark design tokens
// (src/theme) for pixel-identical visuals without a fragile cross-repo workspace link, so the
// repo builds anywhere and preview-deploys cleanly via sxgate (build → dist/).
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: [{ find: '@', replacement: r('./src') }],
    dedupe: ['react', 'react-dom'],
  },
  server: {
    port: 5173,
    // Reach the local devlabd backend during `vite dev`; /api/ws/* upgrades for terminal/Claude.
    proxy: {
      '/api': { target: 'http://127.0.0.1:8780', changeOrigin: false, ws: true },
    },
  },
  build: { outDir: 'dist', emptyOutDir: true },
});
