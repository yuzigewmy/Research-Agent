import { defineConfig } from 'vite'
import { viteStaticCopy } from 'vite-plugin-static-copy'

export default defineConfig({
  base: './',
  plugins: [viteStaticCopy({
    targets: [
      ...['cmaps', 'iccs', 'standard_fonts', 'wasm', 'LICENSE'].map((name) => ({
        src: `node_modules/pdfjs-dist/${name}`,
        dest: 'pdf-resources',
        rename: { stripBase: 2 },
      })),
      {
        src: 'node_modules/pdfjs-dist/package.json',
        dest: 'pdf-resources',
        rename: { stripBase: true, name: 'version.json' },
        transform: (source) => JSON.stringify({ version: JSON.parse(source).version }),
      },
    ],
  })],
  server: {
    host: '127.0.0.1',
    proxy: { '/api': 'http://127.0.0.1:8080' },
  },
  build: { outDir: 'dist', license: { fileName: 'assets/third-party-notices.md' } },
})
