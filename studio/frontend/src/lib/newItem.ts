// newItem: the pure rules behind the New dialog (docs/plans/library.md L3, L6):
// which binding kind a choice means, the name rule per type, and the words.
// Go checks every name again; this is for a good message before the call.
import type { LibraryType } from './libraryRoutes'

/** NewArtifact's kind strings (studio/artifacts_edit.go). */
export type StarterKind = 'a2ui' | 'html' | 'html-script' | 'script'

const NAME_RE = /^[a-z0-9][a-z0-9_-]{0,63}$/
// Windows device names, mirrored from filestore.ValidID.
const RESERVED_RE = /^(con|prn|aux|nul|com[1-9]|lpt[1-9])$/

/** True when value is a name filestore.ValidID accepts (artifacts, queries); the one rule the route checks reuse. */
export function validName(value: string): boolean {
  return NAME_RE.test(value) && !RESERVED_RE.test(value)
}

export const MAX_TITLE = 200
export const RUNS_AFTER_ALLOW = 'Runs after you allow it'

export const CARD_LINE: Record<LibraryType, string> = {
  notebook: 'SQL, notes and charts in cells you run top to bottom.',
  live: 'A view with controls (dates, filters) over named queries. Built from Slaw’s components.',
  page: 'Your own HTML bound to named queries. Static by default; tick the script box for an interactive page in its own window.',
  script: 'A view.js that draws an SVG or a component page from query rows.',
}

export const STEP2_TITLE: Record<LibraryType, string> = { notebook: 'New notebook', live: 'New live view', page: 'New page', script: 'New chart script' }

/** The kind NewArtifact is called with; null for a notebook (CreateNotebook). */
export function starterKind(type: LibraryType, withScript: boolean): StarterKind | null {
  if (type === 'notebook') return null
  if (type === 'live') return 'a2ui'
  if (type === 'script') return 'script'
  return withScript ? 'html-script' : 'html'
}

/** True for a kind that opens as source behind an Allow button. */
export function needsAllow(type: LibraryType): boolean {
  return type === 'page' || type === 'script'
}

/** The message for a bad entry, or '' when it is fine. */
export function nameProblem(type: LibraryType, value: string): string {
  const v = value.trim()
  if (type === 'notebook') return v ? '' : 'Give it a title'
  if (!NAME_RE.test(v)) return 'Use 1 to 64 lowercase letters, digits, - or _ (for example "weekly-sales").'
  return RESERVED_RE.test(v) ? `"${v}" is a reserved name on Windows (con, prn, aux, nul, com1-9, lpt1-9). Pick another.` : ''
}

export function titleTooLong(value: string): boolean {
  return [...value].length > MAX_TITLE
}
