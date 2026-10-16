import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

// The dashboard ships inside the Go binary, so its version is the module's.
// Read it from the source of truth rather than keeping a second copy here.
function pulseVersion() {
  try {
    const configPath = fileURLToPath(new URL('../../pulse/config.go', import.meta.url))
    const match = /const Version = "([^"]+)"/.exec(readFileSync(configPath, 'utf8'))
    return match ? match[1] : ''
  } catch {
    return ''
  }
}

export default defineConfig({
  plugins: [react(), tailwindcss()],
  base: '/pulse/ui/',
  define: {
    'import.meta.env.VITE_PULSE_VERSION': JSON.stringify(pulseVersion()),
  },
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
