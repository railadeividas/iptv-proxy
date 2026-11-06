import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { writeFileSync } from 'node:fs'

const backend = process.env.BACKEND || 'http://localhost:8810'

export default defineConfig({
  plugins: [
    vue(),
    // keep web/dist/.gitkeep so `go build` works before the UI is built
    { name: 'gitkeep', closeBundle() { writeFileSync('dist/.gitkeep', '') } },
  ],
  build: { outDir: 'dist', emptyOutDir: true },
  server: {
    proxy: {
      '/api': backend,
      '/ws': { target: backend, ws: true },
      '/p': backend, '/x': backend, '/xd': backend,
    },
  },
})
