import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// Purser UI build config.
//
// - `base: './'` makes every asset reference in the built bundle RELATIVE, so
//   the control plane can serve the SPA from any mount path (e.g. `/ui/`) and
//   the bundle stays fully self-contained. No absolute host is ever baked in.
// - The whole app is bundled locally (no CDN/runtime external requests), which
//   satisfies the air-gap requirement.
export default defineConfig({
  plugins: [react()],
  base: './',
  build: {
    outDir: 'dist',
    // Fail loudly if a stray huge asset sneaks in; the app should stay small.
    chunkSizeWarningLimit: 900,
  },
});
