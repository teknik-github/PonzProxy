import path from 'node:path'
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

// The bundle is embedded in the Go binary, so it builds straight into the
// package that embeds it. During development the dev server proxies the API
// to a locally running ponzproxy, which keeps the frontend free of any
// knowledge of where the backend lives in production.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    // shadcn components are generated with "@/..." imports.
    alias: { '@': path.resolve(import.meta.dirname, './src') },
  },
  build: {
    outDir: '../internal/webui/dist',
    // The directory is wiped so stale hashed assets do not accumulate into
    // the binary. The build script puts .gitkeep back afterwards: it is the
    // one tracked file there, and without it a fresh checkout cannot compile
    // because go:embed needs at least one match.
    emptyOutDir: true,
    // The console is opened over a LAN more often than over the internet;
    // a smaller bundle matters less than being able to read a stack trace
    // from an operator's screenshot.
    sourcemap: true,
  },
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:8080',
        changeOrigin: true,
        ws: true,
      },
    },
  },
})
