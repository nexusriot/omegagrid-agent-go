import assert from 'node:assert/strict'
import { test } from 'node:test'
import { loadModule } from './load.mjs'

const { streamQuery } = await loadModule('src/api/stream.ts')

for (const status of [200, 401, 500]) {
  test(`HTTP ${status} completes the stream exactly once`, async (t) => {
    t.mock.method(globalThis, 'fetch', async () => new Response(
      status === 200 ? 'event: final\ndata: {"answer":"ok"}\n\n' : '',
      { status },
    ))
    globalThis.window = { prompt: () => null }
    t.after(() => { delete globalThis.window })
    const events = []
    let completions = 0
    await new Promise(resolve => {
      streamQuery({ query: 'hello' }, ev => events.push(ev), () => {
        completions++
        resolve()
      })
    })
    assert.equal(completions, 1)
    assert.equal(events.length, 1)
    assert.equal(events[0].event, status === 200 ? 'final' : 'error')
    if (status === 401) assert.doesNotMatch(events[0].data.message, /saved/)
  })
}
