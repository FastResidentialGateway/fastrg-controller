import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// The Go server serves the build from ./web/build and exposes hashed assets
// under /static, so the output directory and asset directory must match.
export default defineConfig({
  plugins: [react()],
  base: '/',
  build: {
    outDir: 'build',
    assetsDir: 'static',
    sourcemap: false,
  },
  server: {
    port: 3000,
    proxy: {
      '/api': {
        target: 'https://localhost:8443',
        changeOrigin: true,
        secure: false,
      },
    },
  },
})
