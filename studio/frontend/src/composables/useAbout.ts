/**
 * Settings > About (#40): build info, the links it offers, and the notices viewer's open flag.
 * Links open in the system browser through the Wails runtime; nothing is prefilled, so a
 * bug report starts from GitHub's issue chooser and carries no workspace or user data.
 */
import { ref } from 'vue'
import { App } from '../lib/bindings'
import { BrowserOpenURL } from '../wailsjs/runtime/runtime'
import { errorText } from '../lib/errors'

const REPO = 'https://github.com/SandwichLabs/slaw'
const LICENSE_URL = `${REPO}/blob/main/LICENSE`
const PRIVACY_URL = `${REPO}/blob/main/docs/privacy.md`
const REPORT_BUG_URL = `${REPO}/issues/new/choose`

const UNKNOWN: BuildInfo = { version: 'unknown', commit: '', date: '', goVersion: '', duckdbVersion: '', platform: '' }

/** The text the "Copy build info" button puts on the clipboard (safe to paste into an issue). */
export function buildInfoText(i: BuildInfo): string {
  const rows: Array<[string, string]> = [
    ['Slaw', i.version], ['Commit', i.commit], ['Built', i.date], ['Platform', i.platform], ['Go', i.goVersion], ['DuckDB', i.duckdbVersion],
  ]
  return rows.filter(([, v]) => v).map(([k, v]) => `${k}: ${v}`).join('\n')
}

export function useAbout() {
  const info = ref<BuildInfo>(UNKNOWN)
  const copied = ref(false)
  const error = ref('')

  async function load() {
    try {
      info.value = await App.GetBuildInfo()
    } catch (e: unknown) {
      error.value = errorText(e)
    }
  }

  async function copyBuildInfo() {
    try {
      await navigator.clipboard.writeText(buildInfoText(info.value))
      copied.value = true
      setTimeout(() => { copied.value = false }, 2000)
    } catch {
      error.value = "Couldn't copy to the clipboard."
    }
  }

  const open = (url: string) => BrowserOpenURL(url)

  return { info, copied, error, load, copyBuildInfo, openLicense: () => open(LICENSE_URL), openPrivacy: () => open(PRIVACY_URL), reportBug: () => open(REPORT_BUG_URL) }
}
