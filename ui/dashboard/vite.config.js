import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  base: '/pulse/ui/',
  build: {
    outDir: '../dist',
    emptyOutDir: true,
  },
  server: {
    proxy: {
      '/pulse/api': 'http://localhost:8080',
      '/pulse/ws': { target: 'ws://localhost:8080', ws: true },
    },
  },
})
