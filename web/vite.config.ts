import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// Build output goes to ./dist; `make web-build` copies it into
// internal/web/static/ so the Go binary can embed it.
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: 'dist',
  },
})
