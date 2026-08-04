/// <reference types="vitest/config" />
import { execSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import path from 'node:path'
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

// GIT_SHA is passed as a build arg in the Docker image build (see
// web/Dockerfile); falls back to reading the local git HEAD for `npm
// run dev`/local `npm run build`, where no build arg is set.
function gitSha() {
  if (process.env.GIT_SHA) return process.env.GIT_SHA
  try {
    return execSync('git rev-parse --short HEAD', { cwd: __dirname }).toString().trim()
  } catch {
    return 'unknown'
  }
}

// APP_VERSION is passed as a build arg in the Docker image build (see
// web/Dockerfile), set from the release tag so the sidebar always
// shows what was actually deployed — package.json's own version field
// is a source file that drifts from the tag whenever a release ships
// without a matching bump. Falls back to package.json for `npm run
// dev`/local `npm run build`, where no build arg is set.
function appVersion() {
  if (process.env.APP_VERSION) return process.env.APP_VERSION
  return JSON.parse(readFileSync(path.resolve(__dirname, 'package.json'), 'utf-8')).version
}

// https://vite.dev/config/
export default defineConfig({
  plugins: [react(), tailwindcss()],
  define: {
    __GIT_SHA__: JSON.stringify(gitSha()),
    __APP_VERSION__: JSON.stringify(appVersion()),
  },
  resolve: {
    alias: { '@': path.resolve(__dirname, './src') },
  },
  server: {
    port: 3000,
    proxy: {
      // Same-origin /v1 in the browser; the dev server forwards to
      // brain (compose sets BRAIN_URL to the internal service name).
      '/v1': {
        target: process.env.BRAIN_URL ?? 'http://localhost:8300',
        changeOrigin: true,
      },
    },
  },
  test: {
    environment: 'jsdom',
    setupFiles: ['./src/test-setup.ts'],
    globals: false,
  },
})
