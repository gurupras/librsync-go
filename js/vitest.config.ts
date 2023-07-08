import { execSync } from 'child_process'
import { defineConfig } from 'vitest/config'

export default async function () {
  execSync('make', {
    cwd: __dirname
  })

  return defineConfig({
  })
}
