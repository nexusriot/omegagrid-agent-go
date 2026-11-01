import assert from 'node:assert/strict'
import { test } from 'node:test'
import { loadModule } from './load.mjs'

const { nextCronFires } = await loadModule('src/lib/cron.ts')

test('a single value with /1 previews every remaining minute', (t) => {
  t.mock.method(Date, 'now', () => Date.parse('2026-01-01T00:04:00Z'))
  assert.deepEqual(nextCronFires('5/1 * * * *', 3).map(d => d.toISOString()), [
    '2026-01-01T00:05:00.000Z',
    '2026-01-01T00:06:00.000Z',
    '2026-01-01T00:07:00.000Z',
  ])
  assert.deepEqual(nextCronFires('5 * * * *', 2).map(d => d.toISOString()), [
    '2026-01-01T00:05:00.000Z',
    '2026-01-01T01:05:00.000Z',
  ])
})
