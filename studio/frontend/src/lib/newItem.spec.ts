import { describe, it, expect } from 'vitest'
import { starterKind, nameProblem, needsAllow, titleTooLong } from './newItem'

describe('newItem rules', () => {
  it('maps a choice to the NewArtifact kind', () => {
    expect(starterKind('notebook', false)).toBeNull()
    expect(starterKind('live', true)).toBe('a2ui')
    expect(starterKind('page', false)).toBe('html')
    expect(starterKind('page', true)).toBe('html-script')
    expect(starterKind('script', false)).toBe('script')
  })
  it('validates per type', () => {
    expect(nameProblem('notebook', '  ')).toBe('Give it a title')
    expect(nameProblem('notebook', 'Any Title!')).toBe('')
    for (const t of ['live', 'page', 'script'] as const) {
      expect(nameProblem(t, 'Bad Name')).toMatch(/lowercase/)
      expect(nameProblem(t, '-x')).toMatch(/lowercase/)
      expect(nameProblem(t, 'a'.repeat(65))).toMatch(/lowercase/)
      expect(nameProblem(t, 'a'.repeat(64))).toBe('')
      expect(nameProblem(t, 'weekly_sales-2')).toBe('')
      for (const r of ['con', 'nul', 'com1', 'lpt9']) expect(nameProblem(t, r)).toMatch(/reserved/)
      expect(nameProblem(t, 'com10')).toBe('')
      expect(nameProblem(t, 'com0')).toBe('')
    }
  })
  it('flags kinds that open behind Allow, and long titles', () => {
    expect([needsAllow('page'), needsAllow('script'), needsAllow('live'), needsAllow('notebook')]).toEqual([true, true, false, false])
    expect(titleTooLong('x'.repeat(200))).toBe(false)
    expect(titleTooLong('x'.repeat(201))).toBe(true)
  })
})
