// formatCell.spec.ts — table-driven per PLAN-83 §6's type matrix (wire →
// grid display → report display), both contexts, plus cellEditText,
// decimalScale, typeFamily-without-a-type inference, and groupDigits.
//
// Locale: Intl defaults to the process locale, which is en-US in this repo's
// dev/CI environment (verified: `new Intl.NumberFormat().resolvedOptions().locale`
// === 'en-US'). formatCell.ts has no locale parameter to pin explicitly.
import { describe, it, expect } from 'vitest'
import { formatCell, cellEditText, cellClass, isEditableType, decimalScale, typeFamily, isNumericType, groupDigits, type FormattedCell } from './formatCell'

interface Case {
  name: string
  value: unknown
  dbType?: string
  grid: Partial<FormattedCell>
  report: Partial<FormattedCell>
}

const cases: Case[] = [
  {
    name: 'BIGINT past 2^53 (arrives as an exact decimal string)',
    value: '9007199254740993',
    dbType: 'BIGINT',
    grid: { text: '9007199254740993', kind: 'number', align: 'right' },
    report: { text: '9,007,199,254,740,993', kind: 'number', align: 'right' },
  },
  {
    name: 'INTEGER',
    value: 42,
    dbType: 'INTEGER',
    grid: { text: '42', kind: 'number', align: 'right' },
    report: { text: '42', kind: 'number', align: 'right' },
  },
  {
    name: 'BIGINT within safe range',
    value: 1234567,
    dbType: 'BIGINT',
    grid: { text: '1234567', kind: 'number', align: 'right' },
    report: { text: '1,234,567', kind: 'number', align: 'right' },
  },
  {
    name: 'DECIMAL(18,4) keeps its declared scale',
    value: 12.5,
    dbType: 'DECIMAL(18,4)',
    grid: { text: '12.5000', kind: 'number', align: 'right' },
    report: { text: '12.5000', kind: 'number', align: 'right' },
  },
  {
    name: 'DECIMAL(12,2), grouped in both contexts',
    value: 15675.3,
    dbType: 'DECIMAL(12,2)',
    grid: { text: '15,675.30', kind: 'number', align: 'right' },
    report: { text: '15,675.30', kind: 'number', align: 'right' },
  },
  {
    name: 'DECIMAL(20,2) past the exact-double threshold (exact string)',
    value: '12345678901234567.89',
    dbType: 'DECIMAL(20,2)',
    grid: { text: '12,345,678,901,234,567.89', kind: 'number', align: 'right' },
    report: { text: '12,345,678,901,234,567.89', kind: 'number', align: 'right' },
  },
  {
    name: 'DOUBLE float noise: grid shows it exactly, report rounds to 2dp',
    value: 0.30000000000000004,
    dbType: 'DOUBLE',
    grid: { text: '0.30000000000000004', kind: 'number', align: 'right' },
    report: { text: '0.3', kind: 'number', align: 'right' },
  },
  {
    name: 'DATE',
    value: '2025-04-06',
    dbType: 'DATE',
    grid: { text: '2025-04-06', kind: 'text', align: 'left', title: undefined },
    report: { text: '2025-04-06', kind: 'text', align: 'left', title: undefined },
  },
  {
    name: 'TIME',
    value: '13:45:00',
    dbType: 'TIME',
    grid: { text: '13:45:00', kind: 'text', align: 'left' },
    report: { text: '13:45:00', kind: 'text', align: 'left' },
  },
  {
    name: 'TIMESTAMP',
    value: '2025-04-06 13:45:00',
    dbType: 'TIMESTAMP',
    grid: { text: '2025-04-06 13:45:00', kind: 'text', align: 'left' },
    report: { text: '2025-04-06 13:45:00', kind: 'text', align: 'left' },
  },
  {
    name: 'TIMESTAMPTZ',
    value: '2025-04-06 06:45:00-05',
    dbType: 'TIMESTAMPTZ',
    grid: { text: '2025-04-06 06:45:00-05', kind: 'text', align: 'left' },
    report: { text: '2025-04-06 06:45:00-05', kind: 'text', align: 'left' },
  },
  {
    name: 'INTERVAL',
    value: '1 year 2 months 3 days 04:05:06',
    dbType: 'INTERVAL',
    grid: { text: '1 year 2 months 3 days 04:05:06', kind: 'text', align: 'left' },
    report: { text: '1 year 2 months 3 days 04:05:06', kind: 'text', align: 'left' },
  },
  {
    name: 'UUID',
    value: '4ac7a9e9-607c-4c8a-84f3-843f0191e3fd',
    dbType: 'UUID',
    grid: { text: '4ac7a9e9-607c-4c8a-84f3-843f0191e3fd', kind: 'text', align: 'left' },
    report: { text: '4ac7a9e9-607c-4c8a-84f3-843f0191e3fd', kind: 'text', align: 'left' },
  },
  {
    name: 'BLOB, DuckDB-escaped text shown as-is',
    value: '\\x00\\xFFab',
    dbType: 'BLOB',
    grid: { text: '\\x00\\xFFab', kind: 'text', align: 'left' },
    report: { text: '\\x00\\xFFab', kind: 'text', align: 'left' },
  },
  {
    name: 'empty string is NOT null',
    value: '',
    dbType: 'VARCHAR',
    grid: { text: '', kind: 'text', align: 'left' },
    report: { text: '', kind: 'text', align: 'left' },
  },
  {
    name: 'leading whitespace is kept',
    value: '  Kim Park',
    dbType: 'VARCHAR',
    grid: { text: '  Kim Park', kind: 'text', align: 'left' },
    report: { text: '  Kim Park', kind: 'text', align: 'left' },
  },
  {
    name: 'long text gets a title with the full value',
    value: 'long text '.repeat(30),
    dbType: 'VARCHAR',
    grid: { text: 'long text '.repeat(30), kind: 'text', align: 'left', title: 'long text '.repeat(30) },
    report: { text: 'long text '.repeat(30), kind: 'text', align: 'left', title: 'long text '.repeat(30) },
  },
  {
    name: 'LIST',
    value: [1, 2, 3],
    dbType: 'INTEGER[]',
    grid: { text: '[1, 2, 3]', kind: 'nested', align: 'left' },
    report: { text: '[1, 2, 3]', kind: 'nested', align: 'left' },
  },
  {
    name: 'STRUCT',
    value: { a: 1, b: 'x' },
    dbType: 'STRUCT("a" INTEGER, "b" VARCHAR)',
    grid: { text: "{'a': 1, 'b': x}", kind: 'nested', align: 'left' },
    report: { text: "{'a': 1, 'b': x}", kind: 'nested', align: 'left' },
  },
]

describe('formatCell — type matrix (PLAN-83 §6)', () => {
  for (const c of cases) {
    it(`${c.name} — grid`, () => {
      expect(formatCell(c.value, c.dbType, { context: 'grid' })).toMatchObject(c.grid)
    })
    it(`${c.name} — report`, () => {
      expect(formatCell(c.value, c.dbType, { context: 'report' })).toMatchObject(c.report)
    })
  }

  it('NULL renders as the literal NULL, muted, in grid context', () => {
    expect(formatCell(null, 'VARCHAR', { context: 'grid' })).toMatchObject({ text: 'NULL', kind: 'null' })
  })

  it('NULL renders as an em dash in report context', () => {
    expect(formatCell(null, 'VARCHAR', { context: 'report' })).toMatchObject({ text: '—', kind: 'null' })
  })

  it('defaults to grid context when none is given', () => {
    expect(formatCell(null).text).toBe('NULL')
  })

  // NULL aligns by the column's family, not hard-coded right — it must sit
  // under whatever the non-null cells in that column align to.
  it('NULL in a numeric column aligns right', () => {
    expect(formatCell(null, 'DECIMAL(12,2)')).toMatchObject({ align: 'right' })
    expect(formatCell(null, 'BIGINT')).toMatchObject({ align: 'right' })
    expect(formatCell(null, 'DOUBLE')).toMatchObject({ align: 'right' })
  })

  it('NULL in a text/temporal/nested column aligns left', () => {
    expect(formatCell(null, 'VARCHAR')).toMatchObject({ align: 'left' })
    expect(formatCell(null, 'DATE')).toMatchObject({ align: 'left' })
    expect(formatCell(null, 'INTEGER[]')).toMatchObject({ align: 'left' })
  })

  it('NULL with no dbType at all defaults left', () => {
    expect(formatCell(null)).toMatchObject({ align: 'left' })
  })

  // NaN/±Inf cross the wire as DuckDB's own text ("nan"/"inf"/"-inf"),
  // not null — formatCell must display that
  // text (not crash on a non-numeric string in a float column) and never
  // treat it as SQL NULL.
  it('NaN/Inf cross as DuckDB text and display as such, not as NULL', () => {
    expect(formatCell('nan', 'DOUBLE')).toMatchObject({ text: 'nan', kind: 'number' })
    expect(formatCell('inf', 'DOUBLE')).toMatchObject({ text: 'inf', kind: 'number' })
    expect(formatCell('-inf', 'FLOAT')).toMatchObject({ text: '-inf', kind: 'number' })
  })

  it('BOOLEAN', () => {
    expect(formatCell(true, 'BOOLEAN')).toMatchObject({ text: 'true', kind: 'bool', align: 'left' })
    expect(formatCell(false, 'BOOLEAN')).toMatchObject({ text: 'false', kind: 'bool', align: 'left' })
  })

  it('newlines and tabs render as visible markers', () => {
    const fc = formatCell('a\nb\tc', 'VARCHAR')
    expect(fc.text).toBe('a↵b→c')
    expect(fc.title).toBe('a\nb\tc')
  })
})

describe('cellEditText', () => {
  it('seeds a DECIMAL editor with the declared scale, not JS\'s trimmed text', () => {
    expect(cellEditText(15675.3, 'DECIMAL(12,2)')).toBe('15675.30')
  })

  it('passes an out-of-range DECIMAL string through unchanged', () => {
    expect(cellEditText('12345678901234567.89', 'DECIMAL(20,2)')).toBe('12345678901234567.89')
  })

  it('null seeds an empty editor', () => {
    expect(cellEditText(null, 'DECIMAL(12,2)')).toBe('')
    expect(cellEditText(undefined, 'BIGINT')).toBe('')
  })

  it('passes a big BIGINT string through unchanged', () => {
    expect(cellEditText('9007199254740993', 'BIGINT')).toBe('9007199254740993')
  })

  it('stringifies a plain number with no declared scale', () => {
    expect(cellEditText(42, 'INTEGER')).toBe('42')
  })

  it('never groups digits (grouping is display-only)', () => {
    expect(cellEditText(15675.3, 'DECIMAL(12,2)')).not.toContain(',')
  })

  // A nested value seeds with the text formatCell displays (the editor never
  // opens on one, but the untouched-cell guard compares like for like).
  it('seeds a STRUCT with the text the cell displays', () => {
    const value = { a: 1, b: 'x' }
    const t = 'STRUCT("a" INTEGER, "b" VARCHAR)'
    expect(cellEditText(value, t)).toBe("{'a': 1, 'b': x}")
    expect(cellEditText(value, t)).toBe(formatCell(value, t).text)
  })

  it('seeds a LIST with the text the cell displays, including a null element', () => {
    const value = [1, null, 3]
    expect(cellEditText(value, 'INTEGER[]')).toBe('[1, NULL, 3]')
    expect(cellEditText(value, 'INTEGER[]')).toBe(formatCell(value, 'INTEGER[]').text)
  })
})

describe('isEditableType', () => {
  it('refuses nested types and VARIANT, allows the rest', () => {
    for (const t of ['INTEGER[]', 'INTEGER[3]', 'STRUCT("a" INTEGER)', 'MAP(VARCHAR, INTEGER)', 'UNION("n" INTEGER)', 'VARIANT', 'variant']) {
      expect(isEditableType(t), t).toBe(false)
    }
    for (const t of ['INTEGER', 'VARCHAR', 'JSON', 'DECIMAL(10,2)', 'DATE', undefined]) {
      expect(isEditableType(t), String(t)).toBe(true)
    }
  })
})

describe('formatCell — nested values (#18, #388)', () => {
  const cell = (v: unknown, t?: string) => formatCell(v, t).text

  it('prints the reported [{foo: bar}] the way DuckDB does, not [OBJECT]', () => {
    expect(cell([{ foo: 'bar' }], 'STRUCT("foo" VARCHAR)[]')).toBe("[{'foo': bar}]")
    // No column type (model-authored inline data): compact JSON, as it always was.
    expect(cell([{ foo: 'bar' }])).toBe('[{"foo":"bar"}]')
  })

  it('keeps a struct in its declared field order, not the wire order', () => {
    expect(cell({ a: 2, b: 1 }, 'STRUCT("b" INTEGER, "a" INTEGER)')).toBe("{'b': 1, 'a': 2}")
  })

  it('prints a MAP as {k=v} in key order', () => {
    expect(cell({ k: 1, j: 2 }, 'MAP(VARCHAR, INTEGER)')).toBe('{j=2, k=1}')
  })

  it('keeps a DECIMAL element at its declared scale', () => {
    expect(cell([12.5, 3], 'DECIMAL(10,2)[]')).toBe('[12.50, 3.00]')
    expect(cell({ p: 12.5 }, 'STRUCT("p" DECIMAL(10,2))')).toBe("{'p': 12.50}")
  })

  it('shows exact big integers unquoted', () => {
    expect(cell({ h: '170141183460469231731687303715884105727' }, 'STRUCT("h" HUGEINT)')).toBe("{'h': 170141183460469231731687303715884105727}")
  })

  it('prints NULL, booleans, nesting and quoted field names', () => {
    expect(cell([[1, null], [true]], 'INTEGER[][]')).toBe('[[1, NULL], [true]]')
    expect(cell({ 'a b': 1 }, 'STRUCT("a b" INTEGER)')).toBe("{'a b': 1}")
  })

  it('prints a UNION value and an empty list', () => {
    expect(cell(2, 'UNION("num" INTEGER)')).toBe('2')
    expect(cell([], 'INTEGER[]')).toBe('[]')
  })

  it('truncates a long value in the cell and puts the full text in the title', () => {
    const long = Array.from({ length: 400 }, (_, i) => i)
    const fc = formatCell(long, 'INTEGER[]')
    expect(fc.text.length).toBe(501)
    expect(fc.text.endsWith('…')).toBe(true)
    expect(fc.title).toBe('[' + long.join(', ') + ']')
  })

  it('titles a medium value and leaves a short one bare', () => {
    expect(formatCell([1, 2], 'INTEGER[]').title).toBeUndefined()
    expect(formatCell(['x'.repeat(60)], 'VARCHAR[]').title).toContain('x'.repeat(60))
  })

  it('a JSON column shows its decoded document as compact JSON, not [object Object]', () => {
    expect(cell({ a: [1, 2], b: 'x' }, 'JSON')).toBe('{"a":[1,2],"b":"x"}')
    expect(cell([{ foo: 'bar' }], 'JSON')).toBe('[{"foo":"bar"}]')
    expect(formatCell({ a: 1 }, 'JSON').kind).toBe('nested')
  })

  it('a JSON string scalar and number show as they did (unquoted)', () => {
    expect(cell('books', 'JSON')).toBe('books')
    expect(cell(3, 'JSON')).toBe('3')
  })

  it('a JSON cell seeds its editor with the JSON text', () => {
    expect(cellEditText({ a: 1 }, 'JSON')).toBe('{"a":1}')
  })

  it('does not split a surrogate pair at the cut', () => {
    const fc = formatCell(['a'.repeat(497) + '😀' + 'zzz'], 'VARCHAR[]')
    // "[" + 497 a's + the pair would put the high surrogate at unit 499.
    expect(fc.text.endsWith('…')).toBe(true)
    expect(/[\ud800-\udbff]…$/.test(fc.text)).toBe(false)
  })
})

describe('decimalScale', () => {
  it('extracts the scale from DECIMAL(w,s)', () => {
    expect(decimalScale('DECIMAL(12,2)')).toBe(2)
  })

  it('extracts the scale from NUMERIC(w,s)', () => {
    expect(decimalScale('NUMERIC(5,3)')).toBe(3)
  })

  it('returns null for scale 0 (an integer, not a decimal display)', () => {
    expect(decimalScale('DECIMAL(10,0)')).toBeNull()
    expect(decimalScale('DECIMAL(10)')).toBeNull()
  })

  it('returns null for a non-DECIMAL type', () => {
    expect(decimalScale('INTEGER')).toBeNull()
    expect(decimalScale(undefined)).toBeNull()
  })
})

describe('typeFamily — inference with no column type (model-authored inline A2UI data)', () => {
  it('a number infers float', () => {
    expect(typeFamily(undefined, 5)).toBe('float')
  })

  it('a boolean infers boolean', () => {
    expect(typeFamily(undefined, true)).toBe('boolean')
  })

  it('an object infers nested', () => {
    expect(typeFamily(undefined, { a: 1 })).toBe('nested')
  })

  it('a string infers text', () => {
    expect(typeFamily(undefined, 'x')).toBe('text')
  })

  it('null with no type infers text', () => {
    expect(typeFamily(undefined, null)).toBe('text')
  })
})

describe('groupDigits', () => {
  it('groups a 20-digit string with no precision loss', () => {
    expect(groupDigits('12345678901234567890')).toBe('12,345,678,901,234,567,890')
  })

  it('groups a decimal string, swapping in the locale decimal point', () => {
    expect(groupDigits('15675.30')).toBe('15,675.30')
  })

  it('handles a negative number', () => {
    expect(groupDigits('-1234567')).toBe('-1,234,567')
  })

  it('leaves scientific notation untouched (not a plain decimal string)', () => {
    expect(groupDigits('1e+21')).toBe('1e+21')
  })

  it('leaves short numbers unchanged', () => {
    expect(groupDigits('42')).toBe('42')
  })
})

// isNumericType is the one place "is this column numeric" is decided,
// shared by a header's alignment class and
// (via lib/notebookViews.ts) the notebook view-fit classifier — no call
// site re-derives it with its own `.includes(typeFamily(...))` check.
describe('isNumericType', () => {
  it('is true for integer/decimal/float families', () => {
    expect(isNumericType('BIGINT')).toBe(true)
    expect(isNumericType('DECIMAL(12,2)')).toBe(true)
    expect(isNumericType('DOUBLE')).toBe(true)
  })

  it('is false for boolean/temporal/text/nested families', () => {
    expect(isNumericType('BOOLEAN')).toBe(false)
    expect(isNumericType('DATE')).toBe(false)
    expect(isNumericType('VARCHAR')).toBe(false)
    expect(isNumericType('INTEGER[]')).toBe(false)
  })

  it('is false with no type', () => {
    expect(isNumericType(undefined)).toBe(false)
  })
})

// cellClass reads fc.align (not fc.kind === 'number') to decide .cell-num,
// so a NULL cell in a numeric
// column gets right-aligned like its neighbours.
describe('cellClass', () => {
  it('adds .cell-num for a right-aligned NULL in a numeric column', () => {
    const fc = formatCell(null, 'DECIMAL(12,2)')
    expect(cellClass(fc)).toContain('cell-num')
    expect(cellClass(fc)).toContain('cell-null')
  })

  it('does not add .cell-num for a left-aligned NULL in a text column', () => {
    const fc = formatCell(null, 'VARCHAR')
    expect(cellClass(fc)).not.toContain('cell-num')
    expect(cellClass(fc)).toContain('cell-null')
  })

  it('adds .cell-num for an ordinary numeric cell', () => {
    expect(cellClass(formatCell(42, 'INTEGER'))).toContain('cell-num')
  })

  it('adds .cell-temporal for a DATE/TIME cell, without .cell-num', () => {
    const fc = formatCell('2025-04-06', 'DATE')
    const classes = cellClass(fc, 'DATE')
    expect(classes).toContain('cell-temporal')
    expect(classes).not.toContain('cell-num')
  })
})
