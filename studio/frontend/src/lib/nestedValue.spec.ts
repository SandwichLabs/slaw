// The shared vectors in internal/engine/testdata/nested_text.json are also run
// by Go (TestNestedTextSharedVectors), so the grid's text and the console's
// cannot drift apart.
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { jsonText, nestedText } from './nestedValue'

const cases: { name: string; type: string; value: unknown; text: string }[] = JSON.parse(
  readFileSync(resolve(process.cwd(), '../../internal/engine/testdata/nested_text.json'), 'utf8'),
)

describe('nestedText — shared Go/TS vectors', () => {
  for (const c of cases) {
    it(c.name, () => {
      expect(c.type === 'JSON' ? jsonText(c.value) : nestedText(c.value, c.type)).toBe(c.text)
    })
  }
})
