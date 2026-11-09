import { describe, it, expect } from 'vitest'
import { replayState, summaryParts, byText, fromText, shortHash, durationText, noteText, verdictText } from './receiptText'

function r(over: Partial<ReceiptRecord> = {}): ReceiptRecord {
  return {
    id: 'r_0123456789abcdef0123', at: '2026-11-09T10:00:00Z', duration_ms: 5, surface: 'query_editor', actor: 'person',
    source: {}, verdict: 'unchecked', rows: { returned: 0, total: 0, truncated: false }, result_hmac: 'a'.repeat(64), ...over,
  }
}
const conn = (label: 'replayable' | 'live_only' | 'not_recorded' | null, value = 'v'): ReceiptConnection => ({
  name: 'c', type: 't', read_only: true, status: 'connected', snapshot: label ? { fact: 'f', value, label } : undefined,
})

describe('receiptText', () => {
  it('replayable only when every connection has a replayable snapshot', () => {
    expect(replayState(r({ connections: [conn('replayable'), conn('replayable')] }))).toBe('replayable')
    expect(replayState(r({ connections: [conn('replayable'), conn('live_only')] }))).toBe('live_only')
    expect(replayState(r({ connections: [conn('replayable'), conn('not_recorded')] }))).toBe('not_recorded')
    expect(replayState(r({ connections: [conn(null)] }))).toBe('not_recorded')
    expect(replayState(r())).toBe('not_recorded')
  })

  it('the summary names the snapshot only for one replayable connection', () => {
    expect(summaryParts(r({ connections: [conn('replayable', '4812')] }))).toEqual({ names: ['c'], snapshot: '4812' })
    expect(summaryParts(r({ connections: [conn('live_only', '0/1')] })).snapshot).toBe('')
    expect(summaryParts(r({ connections: [conn('replayable', '1'), conn('replayable', '2')] })).snapshot).toBe('')
  })

  it('says who ran it and from where', () => {
    expect(byText(r({ surface: 'notebook_cell' }))).toBe('You, opening the notebook')
    expect(byText(r({ actor: 'agent', surface: 'agent_run_query' }))).toBe('The agent (run_query)')
    expect(byText(r({ surface: 'weird_new_surface' }))).toBe('You, from weird new surface')
    expect(fromText(r({ source: { kind: 'cell', ref: 'notebooks/n.slnb#c1' } }))).toBe('notebooks/n.slnb#c1')
    expect(fromText(r({ source: { kind: 'sql' } }))).toBe('SQL typed in place')
    expect(fromText(r())).toBe('No file')
  })

  it('formats hashes, durations, verdicts and notes', () => {
    expect(shortHash('9c1e04' + 'f'.repeat(52) + 'b7a4')).toBe('9c1e04…b7a4')
    expect(shortHash('abc')).toBe('abc')
    expect(durationText(42)).toBe('42 ms')
    expect(durationText(1500)).toBe('1.50 s')
    expect(durationText(20_000)).toBe('20.0 s')
    expect(verdictText(r({ verdict: 'read' }))).toBe('Read-only check passed')
    expect(verdictText(r())).toBe('Your own query')
    expect(noteText('unavailable')).toContain('keeps no receipts')
    expect(noteText('not_kept')).toContain("couldn't be written")
    expect(noteText(undefined)).toContain("couldn't be written")
  })
})
