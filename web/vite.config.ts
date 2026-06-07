/// <reference types="vitest/config" />
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// Build output goes to ./dist; `make embed` copies it into
// internal/web/static/ so the Go binary can embed it.
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: 'dist',
  },
  // `npm run dev` serves the UI with hot reload and proxies the API to a locally
  // running engine (`tmula --role local`), so the front end can be developed
  // without rebuilding/embedding. Override the target with VITE_API_TARGET.
  server: {
    proxy: {
      '/api': {
        target: process.env.VITE_API_TARGET || 'http://localhost:8080',
        changeOrigin: true,
      },
    },
  },
  test: {
    environment: 'node',
    include: ['src/**/*.test.ts'],
  },
})
