import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  base: '/ui/',
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
  server: {
    // changeOrigin stays false: the string shorthand turns it on, which
    // rewrites Host to :8000 while the browser's Origin says :5173, and the
    // gateway's origin guard then rejects every write as cross-origin.
    proxy: {
      '/api': { target: 'http://localhost:8000', changeOrigin: false },
      '/health': { target: 'http://localhost:8000', changeOrigin: false },
    },
  },
})
