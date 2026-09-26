import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  build: { outDir: '../internal/web/dist', emptyOutDir: true },
  server: { proxy: { '/api': 'http://127.0.0.1:8484', '/healthz': 'http://127.0.0.1:8484' } }
})
