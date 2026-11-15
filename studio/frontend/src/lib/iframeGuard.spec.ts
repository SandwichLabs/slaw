// #173 guard: StaticPage.vue is the only place Studio renders an iframe. On
// Linux, Wails authorises an IPC message by the top web view, not the sending
// frame, so any frame that can run script could reach the bound App methods.
// StaticPage's frame is sandbox="" with a script-less CSP; a second frame would
// need the same review. This fails when any other .vue/.ts file under src
// renders one (spec files are not scanned: they may name one in a fixture).
import { describe, expect, it } from 'vitest'
import { readdirSync, readFileSync } from 'node:fs'
import { join, relative, sep } from 'node:path'

const SRC = join(__dirname, '..')
const ALLOWED = ['components/StaticPage.vue']

// <iframe (or <object, <embed, <frame, which nest a browsing context the same
// way) in a template or a string (not after an identifier, so a TypeScript
// generic such as WeakMap<object, X> is not one), and any quoted 'iframe' literal, which covers
// createElement('iframe'), h('iframe') and a computed tag.
const IFRAME = /<\s*iframe\b|(?<![\w$])<(?:object|embed|frame)[\s/>]|(['"`])iframe\1/i

function findIframes(files: Record<string, string>): string[] {
  return Object.keys(files).filter((f) => IFRAME.test(files[f])).sort()
}

function sourceFiles(dir: string, out: string[] = []): string[] {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, e.name)
    if (e.isDirectory()) {
      if (e.name !== 'node_modules' && e.name !== 'wailsjs') sourceFiles(p, out)
    } else if (/\.(vue|[cm]?[jt]sx?)$/.test(e.name) && !/\.(spec|test)\.[cm]?[jt]sx?$/.test(e.name)) {
      out.push(p)
    }
  }
  return out
}

describe('iframe guard (#173)', () => {
  it('fires on every way of rendering an iframe', () => {
    const fixture = {
      'a.vue': '<template><iframe src="x" /></template>',
      'b.ts': "document.createElement('iframe')",
      'c.ts': 'return h("iframe", { sandbox: "" })',
      'd.ts': 'const f = createElement(`iframe`)',
      'e.vue': '<template><IFRAME></IFRAME></template>',
      'f.vue': '<template><object data="x" /></template>',
      'g.ts': "const t = 'embed'; el.innerHTML = '<embed src=x>'",
      'h.vue': '<template><frame src="x" /></template>',
      'i.js': 'const tag = "iframe"',
      'j.tsx': 'return <iframe />',
      'ok2.ts': 'const g = new WeakMap<object, string>()',
      'ok.ts': '// shown in a sandbox="" iframe\nconst n = "iframes are refused"',
    }
    expect(findIframes(fixture)).toEqual(['a.vue', 'b.ts', 'c.ts', 'd.ts', 'e.vue', 'f.vue', 'g.ts', 'h.vue', 'i.js', 'j.tsx'])
  })

  it('finds an iframe only in StaticPage.vue', () => {
    const files: Record<string, string> = {}
    for (const f of sourceFiles(SRC)) files[relative(SRC, f).split(sep).join('/')] = readFileSync(f, 'utf8')
    expect(findIframes(files)).toEqual(ALLOWED)
  })
})
