import { resolve } from 'path'
import { defineConfig } from 'vite'
import dts from 'vite-plugin-dts'

export default defineConfig({
  build: {
    sourcemap: true,
    lib: {
      // Could also be a dictionary or array of multiple entry points
      entry: resolve(__dirname, 'lib.ts'),
      name: 'wasmCallbacks',
      // the proper extensions will be added
      fileName: 'index',
      formats: ['cjs', 'es', 'umd']
    },
    rollupOptions: {
      // make sure to externalize deps that shouldn't be bundled
      // into your library
      output: {
        // Provide global variables to use in the UMD build
        // for externalized deps
      }
    }
  },
  plugins: [
    dts({
      insertTypesEntry: true
    })
  ]
})
