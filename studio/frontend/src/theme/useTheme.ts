/**
 * Theme state for Slaw — one shared instance across the app.
 *
 * Preferences (which theme, follow-system pairing, motion, scale) are owned by
 * the Go side (internal/prefs) so they persist per user, not per file. In
 * environments without the Wails runtime (vitest, `vite dev` in a browser) we
 * fall back to localStorage so the UI stays usable.
 */
import { computed, reactive, readonly, watch } from 'vue'
import { BUILTIN_THEMES, DEFAULT_DARK_ID, DEFAULT_LIGHT_ID, DEFAULT_THEME_ID } from './builtin'
import { idFromFilename, parseTheme, resolveTheme, ThemeError, type ResolvedTheme, type ThemeSpec } from './schema'
import { applyReduceMotion, applyThemeToDocument, applyUiScale } from './apply'
import { App, hasBinding } from '../lib/bindings'
import { errorText } from '../lib/errors'

export const THEME_SYSTEM = 'system'

export interface ThemePreferences {
  theme: string
  systemLight: string
  systemDark: string
  reduceMotion: boolean
  uiScale: number
}

export interface UserThemeFile {
  path: string
  filename: string
  json: string
}

/** A theme the picker can show, with any load problem attached. */
export interface ThemeListing {
  id: string
  name: string
  type: 'light' | 'dark'
  description?: string
  source: 'builtin' | 'user'
  path?: string
  /** Set when a user theme failed validation; it is listed but not selectable. */
  error?: string
  resolved?: ResolvedTheme
}

const DEFAULT_PREFS: ThemePreferences = {
  theme: DEFAULT_THEME_ID,
  systemLight: DEFAULT_LIGHT_ID,
  systemDark: DEFAULT_DARK_ID,
  reduceMotion: false,
  uiScale: 100,
}

const LS_KEY = 'slaw.preferences'

type Bindings = Pick<AppApi, 'GetPreferences' | 'SetPreferences' | 'ListUserThemes'>

function bindings(): Bindings | null {
  return hasBinding('GetPreferences') ? App : null
}

function readLocal(): Partial<ThemePreferences> {
  try {
    const raw = globalThis.localStorage?.getItem(LS_KEY)
    return raw ? JSON.parse(raw) : {}
  } catch {
    return {}
  }
}
function writeLocal(p: ThemePreferences) {
  try {
    globalThis.localStorage?.setItem(LS_KEY, JSON.stringify(p))
  } catch {
    /* private mode etc. */
  }
}

const state = reactive({
  prefs: { ...DEFAULT_PREFS } as ThemePreferences,
  systemDark: false,
  listings: [] as ThemeListing[],
  problems: [] as string[],
  ready: false,
  /** Non-fatal message for the Settings view (e.g. a theme file that failed). */
  notice: '',
})

const registry = new Map<string, ThemeSpec>()
/** Parse errors for user themes kept in the registry as placeholders. */
const brokenErrors = new Map<string, string>()
for (const t of BUILTIN_THEMES) registry.set(t.id, t)

function buildListings(): void {
  const out: ThemeListing[] = []
  for (const spec of registry.values()) {
    try {
      const resolved = resolveTheme(spec, registry)
      out.push({ id: spec.id, name: spec.name, type: spec.type, description: spec.description, source: spec.source ?? 'builtin', path: spec.path, resolved })
    } catch (e) {
      out.push({ id: spec.id, name: spec.name, type: spec.type, source: spec.source ?? 'user', path: spec.path, error: brokenErrors.get(spec.id) ?? errorText(e) })
    }
  }
  // Built-ins first in their declared order, then user themes by name.
  out.sort((a, b) => {
    if (a.source !== b.source) return a.source === 'builtin' ? -1 : 1
    if (a.source === 'builtin') return BUILTIN_THEMES.findIndex((t) => t.id === a.id) - BUILTIN_THEMES.findIndex((t) => t.id === b.id)
    return a.name.localeCompare(b.name)
  })
  state.listings = out
}

/** Load user theme files (from Go) into the registry, replacing any previous user set. */
function ingestUserThemes(files: UserThemeFile[], problems: string[]): void {
  for (const [id, spec] of [...registry]) if (spec.source === 'user') registry.delete(id)
  brokenErrors.clear()
  const errs = [...problems]
  for (const f of files) {
    const id = idFromFilename(f.filename)
    try {
      const raw = JSON.parse(f.json)
      const spec = parseTheme(raw, { id, source: 'user', path: f.path })
      if (BUILTIN_THEMES.some((b) => b.id === spec.id)) {
        errs.push(`${f.filename}: id "${spec.id}" is a built-in theme; rename the file`)
        continue
      }
      registry.set(spec.id, spec)
    } catch (e) {
      const detail = e instanceof ThemeError && e.path ? e.message.slice(e.path.length + 2) : errorText(e)
      errs.push(`${f.path}: ${detail}`)
      brokenErrors.set(id, detail)
      // Keep it visible in the picker as a broken entry so the author sees it.
      let declaredType: 'light' | 'dark' = 'light'
      try { if (JSON.parse(f.json)?.type === 'dark') declaredType = 'dark' } catch { /* not even JSON */ }
      registry.set(id, { id, name: f.filename, type: declaredType, colors: {}, extends: '__broken__', source: 'user', path: f.path })
    }
  }
  state.problems = errs
  buildListings()
}

/** The id that should be active right now given prefs + OS appearance. */
const effectiveId = computed(() => {
  const p = state.prefs
  if (p.theme === THEME_SYSTEM) return state.systemDark ? p.systemDark : p.systemLight
  return p.theme
})

// Listings are built lazily (installTheme) but any component may read
// activeTheme before that — e.g. AppShell's theme toggle under test — so
// build them on first use.
function ensureListings(): void {
  if (state.listings.length === 0) buildListings()
}

const activeTheme = computed<ResolvedTheme>(() => {
  ensureListings()
  const want = effectiveId.value
  const hit = state.listings.find((l) => l.id === want && l.resolved)
  if (hit?.resolved) return hit.resolved
  // Fallback chain: the type-matched default, then the default.
  const fallbackId = state.systemDark && state.prefs.theme === THEME_SYSTEM ? DEFAULT_DARK_ID : DEFAULT_THEME_ID
  return (state.listings.find((l) => l.id === fallbackId) ?? state.listings[0])!.resolved!
})

let mediaQuery: MediaQueryList | null = null
let installed = false

async function loadPreferences(): Promise<void> {
  const b = bindings()
  if (b) {
    try {
      const p = await b.GetPreferences()
      state.prefs = { ...DEFAULT_PREFS, ...p }
      return
    } catch {
      /* fall through to local */
    }
  }
  state.prefs = { ...DEFAULT_PREFS, ...readLocal() }
}

async function persist(): Promise<void> {
  const p = { ...state.prefs }
  writeLocal(p)
  const b = bindings()
  if (b) {
    try {
      await b.SetPreferences(p)
    } catch (e) {
      state.notice = "Couldn't save preferences: " + errorText(e)
    }
  }
}

export async function reloadUserThemes(): Promise<void> {
  const b = bindings()
  if (!b) {
    ingestUserThemes([], [])
    return
  }
  try {
    const r = await b.ListUserThemes()
    ingestUserThemes(r?.themes ?? [], r?.problems ?? [])
  } catch (e) {
    ingestUserThemes([], ["Couldn't read the themes folder: " + errorText(e)])
  }
}

/**
 * Install the theme system: load prefs + user themes, apply, and keep the
 * document in sync with state from then on. Safe to call more than once.
 */
export async function installTheme(): Promise<void> {
  if (!installed) {
    installed = true
    buildListings()
    if (typeof window !== 'undefined' && typeof window.matchMedia === 'function') {
      mediaQuery = window.matchMedia('(prefers-color-scheme: dark)')
      state.systemDark = mediaQuery.matches
      const onChange = (e: MediaQueryListEvent) => { state.systemDark = e.matches }
      if (typeof mediaQuery.addEventListener === 'function') mediaQuery.addEventListener('change', onChange)
      else if (typeof (mediaQuery as any).addListener === 'function') (mediaQuery as any).addListener(onChange)
    }
    watch(activeTheme, (t) => applyThemeToDocument(t), { immediate: true })
    watch(() => state.prefs.uiScale, (s) => applyUiScale(s), { immediate: true })
    watch(() => state.prefs.reduceMotion, (m) => applyReduceMotion(m), { immediate: true })
  }
  await loadPreferences()
  await reloadUserThemes()
  state.ready = true
}

export function useTheme() {
  return {
    prefs: readonly(state.prefs),
    listings: computed(() => state.listings),
    problems: computed(() => state.problems),
    notice: computed(() => state.notice),
    ready: computed(() => state.ready),
    systemDark: computed(() => state.systemDark),
    effectiveId,
    activeTheme,

    async setTheme(id: string) {
      state.prefs.theme = id
      await persist()
    },
    async setSystemPair(light: string, dark: string) {
      state.prefs.systemLight = light
      state.prefs.systemDark = dark
      await persist()
    },
    async setReduceMotion(on: boolean) {
      state.prefs.reduceMotion = on
      await persist()
    },
    async setUiScale(percent: number) {
      state.prefs.uiScale = Math.min(150, Math.max(75, Math.round(percent)))
      await persist()
    },
    reloadUserThemes,
    /** Quick light/dark flip: picks the type-matched default or the system pair. */
    async toggleDark() {
      const cur = activeTheme.value
      const p = state.prefs
      const next = cur.type === 'dark' ? p.systemLight || DEFAULT_LIGHT_ID : p.systemDark || DEFAULT_DARK_ID
      await this.setTheme(next)
    },
  }
}

/** Test hook: reset module state between specs. */
export function __resetThemeStateForTests(): void {
  installed = false
  state.prefs = { ...DEFAULT_PREFS }
  state.systemDark = false
  state.problems = []
  state.notice = ''
  state.ready = false
  for (const [id, spec] of [...registry]) if (spec.source === 'user') registry.delete(id)
  brokenErrors.clear()
  buildListings()
}
