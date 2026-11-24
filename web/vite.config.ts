import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

// The build goes to web/dist, which the Go binary embeds. In development Vite proxies /v1 to the Go service.
// public/.gitkeep is copied into dist so the embed directory always exists in git.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: { '/v1': 'http://localhost:8080' },
  },
})
