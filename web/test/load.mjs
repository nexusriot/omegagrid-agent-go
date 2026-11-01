import { build } from 'vite'

// Exercise the same TypeScript and import.meta.env transformation as the UI,
// without adding a second test framework or writing build artifacts.
export async function loadModule(path) {
  const result = await build({
    configFile: false,
    logLevel: 'silent',
    build: {
      write: false,
      minify: false,
      lib: { entry: path, formats: ['es'] },
    },
  })
  const output = Array.isArray(result) ? result[0].output : result.output
  const code = output.find((item) => item.type === 'chunk').code
  return import(`data:text/javascript;base64,${Buffer.from(code).toString('base64')}`)
}
