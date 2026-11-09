<script setup lang="ts">
import { App } from '../lib/bindings'
// LauncherView — the first screen when no workspace is open (#111; replaces
// UnlockView). Get back into recent work, or start, in one click. Design spec:
// docs/design/workspaces-connections/SPEC.md section 1.
//
// Keyboard: initial focus on the most recent row (or New workspace when there
// are none); Up/Down move between rows, Enter opens, Delete/Backspace removes a
// row from the list (with an Undo toast), Tab reaches the action cards.
// Ctrl/Cmd+O and Ctrl/Cmd+N are global (App.vue).
import { ref, computed, onMounted, onUnmounted, nextTick } from 'vue'
import Icon from '../components/Icon.vue'
import {
  openWorkspacePath, openFolderDialog, launcherNotice, newWorkspaceOpen, relativeTime,
} from '../composables/useWorkspace'
import { shortcutLabel } from '../composables/useModal'
import { openBundleImport } from '../composables/useBundle'
import { errorText } from '../lib/errors'

const recents = ref<RecentWorkspace[]>([])
const loaded = ref(false)
const opening = ref('')
const error = ref('')
const undoEntry = ref<RecentWorkspace | null>(null)
let undoTimer: ReturnType<typeof setTimeout> | null = null

const rowEls = ref<HTMLButtonElement[]>([])
const newBtn = ref<HTMLButtonElement | null>(null)
const firstRun = computed(() => loaded.value && recents.value.length === 0)

async function load() {
  try {
    recents.value = (await App.ListRecentWorkspaces()) || []
  } catch {
    recents.value = []
  }
  loaded.value = true
  await nextTick()
  const first = rowEls.value.find((el) => el && !el.disabled)
  ;(first ?? newBtn.value)?.focus()
}

async function open(r: RecentWorkspace) {
  if (opening.value || r.missing) return
  opening.value = r.path
  error.value = ''
  const res = await openWorkspacePath(r.path)
  if (!res.ok && res.error) error.value = res.error
  opening.value = ''
}

async function openFolder() {
  if (opening.value) return
  error.value = ''
  const res = await openFolderDialog()
  if (!res.ok && res.error) error.value = res.error
}

async function locate(r: RecentWorkspace) {
  try {
    const path = await App.ChooseFolder(`Locate ${r.name}`)
    if (!path) return
    await App.RelocateRecentWorkspace(r.path, path)
    await load()
  } catch (e: unknown) {
    error.value = errorText(e)
  }
}

async function remove(r: RecentWorkspace) {
  try {
    const removed = await App.RemoveRecentWorkspace(r.path)
    undoEntry.value = removed ?? r
    if (undoTimer) clearTimeout(undoTimer)
    undoTimer = setTimeout(() => { undoEntry.value = null }, 8000)
    const idx = recents.value.findIndex((x) => x.path === r.path)
    recents.value = recents.value.filter((x) => x.path !== r.path)
    await nextTick()
    const rows = rowEls.value.filter(Boolean)
    ;(rows[Math.min(idx, rows.length - 1)] ?? newBtn.value)?.focus()
  } catch (e: unknown) {
    error.value = errorText(e)
  }
}

async function undoRemove() {
  const r = undoEntry.value
  if (!r) return
  undoEntry.value = null
  if (undoTimer) clearTimeout(undoTimer)
  try {
    await App.RestoreRecentWorkspace(r)
    await load()
  } catch (e: unknown) {
    error.value = errorText(e)
  }
}

function onRowKey(e: KeyboardEvent, i: number, r: RecentWorkspace) {
  const rows = rowEls.value.filter(Boolean)
  if (e.key === 'ArrowDown') {
    e.preventDefault()
    rows[Math.min(i + 1, rows.length - 1)]?.focus()
  } else if (e.key === 'ArrowUp') {
    e.preventDefault()
    rows[Math.max(i - 1, 0)]?.focus()
  } else if (e.key === 'Home') {
    e.preventDefault()
    rows[0]?.focus()
  } else if (e.key === 'End') {
    e.preventDefault()
    rows[rows.length - 1]?.focus()
  } else if (e.key === 'Delete' || e.key === 'Backspace') {
    e.preventDefault()
    void remove(r)
  }
}

function setRow(el: unknown, i: number) {
  if (el) rowEls.value[i] = el as HTMLButtonElement
}

function rowLabel(r: RecentWorkspace): string {
  if (r.missing) return `${r.name}, folder not found`
  const branch = r.gitBranch ? `, branch ${r.gitBranch}` : ''
  return `Open ${r.name}, last opened ${relativeTime(r.lastOpened)}${branch}`
}

const missingExample = computed(() => recents.value.find((r) => r.missing))

onMounted(load)
onUnmounted(() => { if (undoTimer) clearTimeout(undoTimer) })
</script>

<template>
  <div class="launcher" data-testid="launcher">
    <div class="lw" :style="firstRun ? 'display:flex;flex-direction:column;align-items:center' : ''">
      <div class="brandrow"><span class="markbig" aria-hidden="true">V</span><h1>Slaw <span>Studio</span></h1></div>

      <div v-if="launcherNotice" class="banner warn" role="status" style="margin-bottom: 20px; width: 100%;" data-testid="launcher-notice">
        <div class="ico"><Icon name="info" :size="20" /></div>
        <div class="body"><div class="sub" style="margin: 0;">{{ launcherNotice }}</div></div>
      </div>

      <!-- B. First run -->
      <template v-if="firstRun">
        <div class="empty-hero" style="padding-top: 36px;">
          <span class="glyph"><Icon name="folderOpen" :size="48" /></span>
          <h2>Pick a folder to start</h2>
          <p class="lead">A workspace is a folder. Notebooks, live views, pages, saved queries and skills live in it as plain files, so you can back it up or share it with git.</p>
          <div class="row" style="gap: 14px; margin-top: 6px;">
            <button ref="newBtn" class="btn btn-primary" type="button" data-testid="new-workspace" @click="newWorkspaceOpen = true">
              <Icon name="plus" :size="16" /> New workspace…
            </button>
            <button class="btn" type="button" data-testid="open-folder" :disabled="!!opening" @click="openFolder">
              <Icon name="folderOpen" :size="16" /> Open folder…
            </button>
            <button class="btn" type="button" data-testid="import-bundle" :disabled="!!opening" @click="openBundleImport('new')">
              <Icon name="download" :size="16" /> Import .slaw…
            </button>
          </div>
        </div>
        <div v-if="error" class="banner attn" role="alert" style="max-width: 760px; width: 100%;" data-testid="launcher-error">
          <div class="ico"><Icon name="alert" :size="20" /></div>
          <div class="body"><div class="ttl">Couldn't open that</div><div class="sub">{{ error }}</div></div>
        </div>
        <div class="hero-mini">
          <div class="card"><b>Library</b><span>Notebooks, live views and pages. They run again when you open them, so the numbers are current.</span></div>
          <div class="card"><b>Connections</b><span>Postgres, MotherDuck, SQLite files, cloud storage. Add them when you need them.</span></div>
          <div class="card"><b>The Slaw assistant</b><span>Ask in plain English. It reads freely and asks before it changes anything.</span></div>
        </div>
      </template>

      <!-- A. Recents -->
      <template v-else-if="loaded">
        <p class="lead">Open a folder to work on your data. Notebooks, live views, pages, saved queries and skills live in that folder as plain files.</p>
        <div class="cols">
          <section aria-labelledby="recent-h">
            <div class="sec-title" style="margin-top: 0;"><h2 id="recent-h">Recent workspaces</h2><span class="label">Last opened first</span></div>
            <div class="card" style="overflow: hidden;">
              <ul role="list" style="list-style: none; margin: 0; padding: 0;" data-testid="recent-list">
                <li v-for="(r, i) in recents" :key="r.path" class="recent-li" :class="{ missing: r.missing }">
                  <button
                    :ref="(el) => setRow(el, i)"
                    class="recent"
                    :class="{ missing: r.missing }"
                    type="button"
                    :disabled="r.missing || (!!opening && opening !== r.path)"
                    :aria-label="rowLabel(r)"
                    :aria-describedby="r.missing ? 'missing-help' : undefined"
                    :aria-busy="opening === r.path"
                    data-testid="recent-row"
                    @click="open(r)"
                    @keydown="onRowKey($event, i, r)"
                  >
                    <span class="fico"><Icon :name="r.missing ? 'alert' : 'folderOpen'" :size="20" /></span>
                    <span style="min-width: 0;"><span class="nm">{{ r.name }}</span><span class="path" :title="r.path">{{ r.path }}</span></span>
                    <span class="tail">
                      <template v-if="opening === r.path"><span>Opening…</span></template>
                      <template v-else-if="r.missing"><span class="badge badge--warn"><span class="dot"></span>Folder not found</span></template>
                      <template v-else>
                        <span v-if="r.gitBranch" class="branch"><Icon name="gitBranch" :size="12" />{{ r.gitBranch }}</span>
                        <span v-else class="branch" style="color: var(--sh-graphite);">no git</span>
                        <span>{{ relativeTime(r.lastOpened) }}</span>
                      </template>
                    </span>
                  </button>
                  <button v-if="r.missing" class="linkbtn" type="button" @click="locate(r)">Locate…</button>
                  <button
                    class="icon-btn"
                    type="button"
                    :aria-label="`Remove ${r.name} from the list`"
                    title="Remove from list (doesn't delete anything)"
                    data-testid="recent-remove"
                    :disabled="!!opening"
                    @click="remove(r)"
                  ><Icon name="x" :size="16" /></button>
                </li>
              </ul>
            </div>
            <p v-if="missingExample" id="missing-help" class="help" style="margin-top: 10px;">
              {{ missingExample.name }}'s folder isn't there anymore. It may be on a drive that isn't plugged in. Locate it, or remove it from this list. Removing never deletes files.
            </p>
            <div v-if="error" class="banner attn" role="alert" style="margin-top: 16px;" data-testid="launcher-error">
              <div class="ico"><Icon name="alert" :size="20" /></div>
              <div class="body"><div class="ttl">Couldn't open that</div><div class="sub">{{ error }}</div></div>
            </div>
          </section>

          <section aria-label="Start something">
            <div class="col" style="gap: 14px;">
              <button class="card act-card primary" type="button" data-testid="open-folder" :disabled="!!opening" @click="openFolder">
                <span class="ai"><Icon name="folderOpen" :size="20" /></span>
                <span><span class="at">Open folder… <kbd>{{ shortcutLabel('O') }}</kbd></span><span class="ad">Choose a workspace, or any folder you'd like to turn into one.</span></span>
              </button>
              <button ref="newBtn" class="card act-card" type="button" data-testid="new-workspace" :disabled="!!opening" @click="newWorkspaceOpen = true">
                <span class="ai"><Icon name="plus" :size="20" /></span>
                <span><span class="at">New workspace… <kbd>{{ shortcutLabel('N') }}</kbd></span><span class="ad">Make a new folder, blank or from a template, with git if you like.</span></span>
              </button>
              <button class="card act-card" type="button" data-testid="import-bundle" :disabled="!!opening" @click="openBundleImport('new')">
                <span class="ai"><Icon name="download" :size="20" /></span>
                <span><span class="at">Import .slaw…</span><span class="ad">Start a new workspace from a bundle someone shared with you.</span></span>
              </button>
            </div>
          </section>
        </div>
      </template>

      <p class="trust-line" :style="firstRun ? 'margin-top: 32px;' : ''">
        <Icon name="shieldCheck" :size="16" />
        <span v-if="firstRun">Passwords and tokens go in this computer's keychain, not in your folder.</span>
        <span v-else>Passwords stay in this computer's keychain, never in the folder, so you can put a workspace in git or send it to a teammate.</span>
      </p>
    </div>

    <div v-if="undoEntry" class="toast-undo" role="status" data-testid="undo-toast">
      <span>Removed {{ undoEntry.name }} from the list.</span>
      <button class="linkbtn" type="button" @click="undoRemove">Undo</button>
    </div>
  </div>
</template>
