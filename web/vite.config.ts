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
  test: {
    environment: 'node',
    include: ['src/**/*.test.ts'],
  },
})
