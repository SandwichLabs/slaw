// numberMarks — draw the verdict on an answer's numbers over its rendered text.
//
// Go decides (internal/llm/numbermatch.go, against the turn's in-memory rows):
// each distinct number as written is either "matches a query result" (with the
// receipt of that read) or "not from a query". This file only draws that. It
// finds the same numbers in the text nodes of the already sanitized HTML by the
// same rules (the shared vectors in internal/llm/testdata/number_tokens.json pin
// both) and wraps each one whose text Go marked; the text itself is never
// rewritten, a number only gains a wrapper and a ✓ button after it. Code, links
// and anything already decorated are skipped, and so is a number Go did not mark.

/** The verdict on one number as written, from Go (llm.NumberMark). */
export interface NumberMark {
  text: string
  matched: boolean
  /** A shown value is this number but could be a coincidence: no mark at all. */
  withheld?: boolean
  receiptId?: string
  column?: string
}

export interface NumberToken {
  text: string
  start: number
  end: number
}

const WORDS = ['thousand', 'million', 'billion', 'trillion']
// Case-insensitive words, case-sensitive single-letter suffixes (m is minutes, M is million).
const ci = (w: string) => w.split('').map((c) => `[${c}${c.toUpperCase()}]`).join('')
const NUMBER_RE = new RegExp(
  `[-+\\u2212]?[$\\u20AC\\u00A3\\u00A5]?(\\d{1,3}(?:,\\d{3})+|\\d+)(\\.\\d+)?( ?(?:${WORDS.map(ci).join('|')})|[KkMBT]|bn)?%?`,
  'g',
)

const WORD_RE = /[\p{L}\p{Nd}_]/u
const isWord = (c: string | undefined) => !!c && WORD_RE.test(c)
const isDigit = (c: string | undefined) => !!c && /\p{Nd}/u.test(c)
const isJoiner = (c: string) => c === '-' || c === '/' || c === ':' || c === ',' || c === '.'

/** The code point just before index i, or undefined. */
function before(text: string, i: number): string | undefined {
  if (i <= 0) return undefined
  const cp = Array.from(text.slice(Math.max(0, i - 2), i))
  return cp[cp.length - 1]
}
function at(text: string, i: number): string | undefined {
  return i < text.length ? Array.from(text.slice(i, i + 2))[0] : undefined
}

function leftOK(text: string, start: number): boolean {
  const p = before(text, start)
  if (p === undefined) return true
  if (isWord(p) || p === '#' || p === '/' || p === ':' || p === '@' || p === '\\') return false
  if (p === '.' || p === ',') {
    if (isWord(before(text, start - 1))) return false
  }
  return true
}

function rightOK(text: string, end: number): boolean {
  const r = at(text, end)
  if (r === undefined) return true
  if (isWord(r)) return false
  if (isJoiner(r) && isDigit(at(text, end + r.length))) return false
  return true
}

/** A single digit, a year or a leading-zero number with no formatting is not marked. */
function ignoredBare(digits: string): boolean {
  if (digits.length === 1 || digits[0] === '0') return true
  return digits.length === 4 && digits >= '1900' && digits <= '2100'
}

/** The numbers of text by the rules of internal/llm/numbermatch.go (without its markdown masking). */
export function numberTokens(text: string): NumberToken[] {
  const out: NumberToken[] = []
  for (const m of text.matchAll(NUMBER_RE)) {
    const start = m.index ?? 0
    const whole = m[0]
    const end = start + whole.length
    if (!leftOK(text, start) || !rightOK(text, end)) continue
    const intText = m[1]
    const bare = !intText.includes(',') && !m[2] && !m[3] && !whole.endsWith('%') && !/[$€£¥]/.test(whole)
    if (bare && ignoredBare(intText)) continue
    out.push({ text: whole, start, end })
  }
  return out
}

const RECEIPT_ID_RE = /^r_[0-9a-f]{20}$/
const SKIP = new Set(['CODE', 'PRE', 'A', 'BUTTON', 'SCRIPT', 'STYLE'])

export interface Decorated {
  html: string
  matched: number
  unmatched: number
}

function insideSkipped(node: Node, root: Node): boolean {
  for (let p = node.parentNode; p && p !== root; p = p.parentNode) {
    if (p instanceof Element && (SKIP.has(p.tagName) || p.classList.contains('num-ok') || p.classList.contains('num-none'))) return true
  }
  return false
}

function textNodes(root: Node): Text[] {
  const out: Text[] = []
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT)
  for (let n = walker.nextNode(); n; n = walker.nextNode()) out.push(n as Text)
  return out
}

/** The tooltip of a matched number: where it was found. */
function matchTitle(mark: NumberMark): string {
  const col = (mark.column ?? '').slice(0, 80)
  return col ? `Matches a value in a query result (column ${col}). Open evidence.` : 'Matches a value in a query result. Open evidence.'
}

/**
 * Decorate the numbers of sanitized answer HTML. A marked number is wrapped in a
 * span (`num-ok` with a ✓ button after it when it matched a read of the turn,
 * `num-none` with a dotted underline when it is not from a query). The marks come
 * from Go and the text is never changed. Nothing is decorated inside code, a
 * link or a button.
 */
export function decorateNumbers(html: string, marks: NumberMark[] | undefined): Decorated {
  const byText = new Map<string, NumberMark>()
  for (const m of marks ?? []) if (m && typeof m.text === 'string' && !m.withheld) byText.set(m.text, m)
  if (byText.size === 0) return { html, matched: 0, unmatched: 0 }
  const tpl = document.createElement('template')
  tpl.innerHTML = html
  let matched = 0
  let unmatched = 0
  for (const node of textNodes(tpl.content)) {
    if (insideSkipped(node, tpl.content)) continue
    const text = node.data
    const hits = numberTokens(text).filter((t) => byText.has(t.text))
    if (hits.length === 0) continue
    const frag = document.createDocumentFragment()
    let pos = 0
    for (const t of hits) {
      if (t.start > pos) frag.append(text.slice(pos, t.start))
      const mark = byText.get(t.text) as NumberMark
      const ok = mark.matched && RECEIPT_ID_RE.test(mark.receiptId ?? '')
      const span = document.createElement('span')
      span.className = ok ? 'num-ok' : 'num-none'
      span.textContent = t.text
      if (!ok) span.title = 'Not from a query'
      frag.append(span)
      if (ok) {
        const btn = document.createElement('button')
        btn.type = 'button'
        btn.className = 'num-check'
        btn.textContent = '✓'
        btn.title = matchTitle(mark)
        btn.setAttribute('aria-label', `${t.text} matches a value in a query result. Open evidence`)
        btn.setAttribute('data-receipt', mark.receiptId as string)
        frag.append(btn)
        matched++
      } else {
        unmatched++
      }
      pos = t.end
    }
    if (pos < text.length) frag.append(text.slice(pos))
    node.replaceWith(frag)
  }
  return { html: tpl.innerHTML, matched, unmatched }
}
