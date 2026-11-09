import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { decorateNumbers, numberTokens, type NumberMark } from './numberMarks'

const ID = 'r_0123456789abcdef0123'

// The Go tokenizer (internal/llm) reads the same file.
const vectors = JSON.parse(
  readFileSync(resolve(process.cwd(), '../../internal/llm/testdata/number_tokens.json'), 'utf8'),
) as { text: string; tokens: string[] }[]

describe('numberTokens', () => {
  it.each(vectors.map((v) => [v.text, v.tokens] as const))('reads %j as the Go side does', (text, tokens) => {
    expect(numberTokens(text).map((t) => t.text)).toEqual(tokens)
  })

  it('gives each number its own span of the text', () => {
    const text = 'Up 12.5% to $1.2M'
    for (const t of numberTokens(text)) expect(text.slice(t.start, t.end)).toBe(t.text)
  })
})

const ok = (text: string): NumberMark => ({ text, matched: true, receiptId: ID, column: 'revenue' })
const none = (text: string): NumberMark => ({ text, matched: false })

describe('decorateNumbers', () => {
  it('puts a check after a matched number and a dotted mark on one that is not from a query', () => {
    const d = decorateNumbers('<p>Revenue was $1.2M, up 12.5%.</p>', [ok('$1.2M'), none('12.5%')])
    expect(d.matched).toBe(1)
    expect(d.unmatched).toBe(1)
    const box = document.createElement('div')
    box.innerHTML = d.html
    const good = box.querySelector('.num-ok')!
    expect(good.textContent).toBe('$1.2M')
    const btn = box.querySelector('button.num-check') as HTMLButtonElement
    expect(btn.getAttribute('data-receipt')).toBe(ID)
    expect(btn.title).toContain('column revenue')
    expect(good.nextElementSibling).toBe(btn)
    expect(box.querySelector('.num-none')!.textContent).toBe('12.5%')
    expect(box.querySelector('.num-none')!.getAttribute('title')).toBe('Not from a query')
  })

  it('never changes the text', () => {
    const html = '<p>Sales: <strong>$1.2M</strong> in 1,234 orders (12.5%), see <em>note 4,321</em>.</p>'
    const d = decorateNumbers(html, [ok('$1.2M'), ok('1,234'), none('12.5%'), none('4,321')])
    const before = document.createElement('div')
    before.innerHTML = html
    const after = document.createElement('div')
    after.innerHTML = d.html
    after.querySelectorAll('button.num-check').forEach((b) => b.remove())
    expect(after.textContent).toBe(before.textContent)
  })

  it('leaves numbers inside code, pre and links alone', () => {
    const html =
      '<p>Run <code>SELECT 98,765</code> or <a href="https://x.test/98,765">98,765 rows</a></p><pre><code>98,765</code></pre><p>and 98,765 here</p>'
    const d = decorateNumbers(html, [ok('98,765')])
    expect(d.matched).toBe(1)
    const box = document.createElement('div')
    box.innerHTML = d.html
    expect(box.querySelectorAll('.num-ok')).toHaveLength(1)
    expect(box.querySelector('code .num-ok, pre .num-ok, a .num-ok')).toBeNull()
  })

  it('decorates only the numbers Go marked, and every occurrence of one', () => {
    const d = decorateNumbers('<p>1,234 then 5,678 then 1,234 again</p>', [ok('1,234')])
    expect(d.matched).toBe(2)
    expect(d.unmatched).toBe(0)
    expect(d.html).not.toContain('<span class="num-none">5,678')
  })

  it('does not read numbers out of dates, versions or words', () => {
    const d = decorateNumbers('<p>On 2024-03-05 v1.2.3 order_55 and 12:30</p>', [none('2024'), none('03'), none('55'), none('12'), none('30')])
    expect(d.matched + d.unmatched).toBe(0)
  })

  it('draws a mark whose receipt id is not a receipt id as not from a query, never as a link', () => {
    const d = decorateNumbers('<p>about 1,234</p>', [{ text: '1,234', matched: true, receiptId: '"><img src=x onerror=alert(1)>' }])
    expect(d.matched).toBe(0)
    expect(d.html).not.toContain('<img')
    expect(d.html).toContain('num-none')
  })

  it('escapes a column name that carries markup', () => {
    const d = decorateNumbers('<p>1,234</p>', [{ text: '1,234', matched: true, receiptId: ID, column: '"><b>x</b>' }])
    const box = document.createElement('div')
    box.innerHTML = d.html
    expect(box.querySelector('b')).toBeNull()
    expect(box.querySelector('button')!.title).toContain('"><b>x</b>')
  })

  it('returns the html untouched with no marks', () => {
    expect(decorateNumbers('<p>1,234</p>', undefined)).toEqual({ html: '<p>1,234</p>', matched: 0, unmatched: 0 })
    expect(decorateNumbers('<p>1,234</p>', [])).toEqual({ html: '<p>1,234</p>', matched: 0, unmatched: 0 })
  })
})
