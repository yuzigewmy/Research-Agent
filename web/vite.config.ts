import { defineConfig } from 'vite'
import { viteStaticCopy } from 'vite-plugin-static-copy'

export default defineConfig({
  base: './',
  plugins: [viteStaticCopy({
    targets: ['cmaps', 'iccs', 'standard_fonts', 'wasm', 'LICENSE'].map((name) => ({
      src: `node_modules/pdfjs-dist/${name}`,
      dest: 'pdf-resources',
      rename: { stripBase: 2 },
    })),
  })],
  server: {
    host: '127.0.0.1',
    proxy: { '/api': 'http://127.0.0.1:8080' },
  },
  build: { outDir: 'dist' },
})
