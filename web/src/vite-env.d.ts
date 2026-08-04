/// <reference types="vite/client" />

// Injected by vite.config.ts's `define` from the GIT_SHA build arg
// (or a local `git rev-parse --short HEAD` fallback in dev).
declare const __GIT_SHA__: string
// Injected by vite.config.ts's `define` from package.json's version field.
declare const __APP_VERSION__: string
