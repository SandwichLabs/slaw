<script setup lang="ts">
import { App } from '../lib/bindings'
// WorkspaceSwitcher — the folder button in the top bar and its menu (SPEC 04).
// Sections: current workspace, recents (missing entries disabled with Locate…),
// Open folder…, New workspace…, Manage workspaces… (the launcher). Roving
// focus: opening focuses the current item, Up/Down/Home/End move, Enter
// activates, Escape closes and returns focus to the button.
import { ref, computed, watch, nextTick, onMounted, onUnmounted } from 'vue'
import Icon from './Icon.vue'
import {
  workspaceInfo, switcherOpen, newWorkspaceOpen, openWorkspacePath, openFolderDialog,
  closeWorkspace, relativeTime,
} from '../composables/useWorkspace'
import { shortcutLabel } from '../composables/useModal'
import { useMenuKeys } from '../composables/useMenuKeys'
import { openBundleExport, openBundleImport } from '../composables/useBundle'
import { errorText } from '../lib/errors'

const emit = defineEmits<{ error: [message: string] }>()

const recents = ref<RecentWorkspace[]>([])
const btn = ref<HTMLButtonElement | null>(null)
const menuEl = ref<HTMLElement | null>(null)

const info = computed(() => workspaceInfo.value)
const others = computed(() => recents.value.filter((r) => r.path !== info.value?.root))

async function loadRecents() {
  try {
    recents.value = (await App.ListRecentWorkspaces()) || []
  } catch {
    recents.value = []
  }
}

function items(): HTMLElement[] {
  return Array.from(menuEl.value?.querySelectorAll<HTMLElement>('[role^="menuitem"]:not([aria-disabled="true"])') ?? [])
}

async function open() {
  await loadRecents()
  switcherOpen.value = true
}

function close(returnFocus = true) {
  switcherOpen.value = false
  if (returnFocus) btn.value?.focus()
}

function toggle() {
  if (switcherOpen.value) close()
  else void open()
}

watch(switcherOpen, async (isOpen) => {
  if (!isOpen) return
  if (!recents.value.length) await loadRecents()
  await nextTick()
  const all = items()
  ;(all.find((el) => el.getAttribute('aria-checked') === 'true') ?? all[0])?.focus()
})

const onMenuKey = useMenuKeys({
  enabled: () => items().map(() => true),
  current: () => items().indexOf(document.activeElement as HTMLElement),
  moveTo: (i) => items()[i]?.focus(),
  close: (restore) => close(restore),
  tab: 'leave',
})

function onDocClick(e: MouseEvent) {
  if (!switcherOpen.value) return
  const t = e.target as Node
  if (menuEl.value?.contains(t) || btn.value?.contains(t)) return
  close(false)
}

onMounted(() => document.addEventListener('mousedown', onDocClick))
onUnmounted(() => document.removeEventListener('mousedown', onDocClick))

async function switchTo(r: RecentWorkspace) {
  if (r.missing) return
  close(false)
  const res = await openWorkspacePath(r.path)
  if (!res.ok && res.error) emit('error', res.error)
}

async function locate(r: RecentWorkspace) {
  try {
    const path = await App.ChooseFolder(`Locate ${r.name}`)
    if (!path) return
    await App.RelocateRecentWorkspace(r.path, path)
    await loadRecents()
  } catch (e: unknown) {
    emit('error', errorText(e))
  }
}

async function openFolder() {
  close(false)
  const res = await openFolderDialog()
  if (!res.ok && res.error) emit('error', res.error)
}

function newWorkspace() {
  close(false)
  newWorkspaceOpen.value = true
}

function exportBundle() {
  close(false)
  openBundleExport()
}

function importBundle() {
  close(false)
  openBundleImport('here')
}

async function manage() {
  close(false)
  const res = await closeWorkspace()
  if (!res.ok && res.error) emit('error', res.error)
}
</script>

<template>
  <div v-if="info" style="position: relative; min-width: 0;">
    <button
      ref="btn"
      class="ws-switch"
      type="button"
      aria-haspopup="menu"
      :aria-expanded="switcherOpen"
      :aria-label="`Workspace: ${info.name}. Switch workspace`"
      :title="`${info.root} · ${shortcutLabel('Shift+O')}`"
      data-testid="ws-switch"
      @click="toggle"
    >
      <Icon name="folderOpen" :size="16" />
      <span class="ws-name">{{ info.name }}</span>
      <span v-if="info.gitBranch" class="ws-branch"><Icon name="gitBranch" :size="12" />{{ info.gitBranch }}</span>
      <Icon name="chevUpDown" :size="16" />
    </button>

    <div
      v-if="switcherOpen"
      ref="menuEl"
      class="menu"
      role="menu"
      aria-label="Workspaces"
      style="left: 0; top: calc(100% + 8px); width: 400px; max-width: calc(100vw - 32px); max-height: calc(100vh - 88px); overflow-y: auto;"
      data-testid="ws-menu"
      @keydown="onMenuKey"
    >
      <div class="mhead">Current workspace</div>
      <button class="mi active" type="button" role="menuitemradio" aria-checked="true" tabindex="-1" @click="close()">
        <Icon name="folderOpen" :size="16" />
        <span class="grow"><span class="t">{{ info.name }}</span><span class="sub">{{ info.root }}<template v-if="info.gitBranch"> · {{ info.gitBranch }}</template></span></span>
        <span class="end"><Icon name="check" :size="16" /></span>
      </button>

      <template v-if="others.length">
        <div class="mhead">Recent</div>
        <div v-for="r in others" :key="r.path" style="position: relative;">
          <button
            class="mi"
            :class="{ disabled: r.missing }"
            type="button"
            role="menuitemradio"
            aria-checked="false"
            :aria-disabled="r.missing ? 'true' : undefined"
            :tabindex="-1"
            data-testid="ws-recent"
            @click="switchTo(r)"
          >
            <Icon :name="r.missing ? 'alert' : 'folderOpen'" :size="16" />
            <span class="grow">
              <span class="t">{{ r.name }}</span>
              <span class="sub">{{ r.missing ? 'Folder not found' : r.path + (r.gitBranch ? ' · ' + r.gitBranch : ' · no git') }}</span>
            </span>
            <span v-if="!r.missing" class="end">{{ relativeTime(r.lastOpened) }}</span>
          </button>
          <button v-if="r.missing" class="linkbtn" type="button" style="position: absolute; right: 8px; top: 6px;" @click="locate(r)">Locate…</button>
        </div>
      </template>

      <div class="msep"></div>
      <button class="mi" type="button" role="menuitem" tabindex="-1" data-testid="ws-open-folder" @click="openFolder">
        <Icon name="folderOpen" :size="16" /><span class="grow"><span class="t">Open folder…</span></span><span class="end"><kbd>{{ shortcutLabel('O') }}</kbd></span>
      </button>
      <button class="mi" type="button" role="menuitem" tabindex="-1" data-testid="ws-new" @click="newWorkspace">
        <Icon name="plus" :size="16" /><span class="grow"><span class="t">New workspace…</span></span><span class="end"><kbd>{{ shortcutLabel('N') }}</kbd></span>
      </button>
      <div class="msep"></div>
      <button class="mi" type="button" role="menuitem" tabindex="-1" data-testid="ws-export-bundle" @click="exportBundle">
        <Icon name="upload" :size="16" /><span class="grow"><span class="t">Export workspace content…</span><span class="sub">Notebooks, queries and skills as a .slaw file</span></span>
      </button>
      <button class="mi" type="button" role="menuitem" tabindex="-1" data-testid="ws-import-bundle" @click="importBundle">
        <Icon name="download" :size="16" /><span class="grow"><span class="t">Import .slaw…</span><span class="sub">Add a bundle to this workspace</span></span>
      </button>
      <div class="msep"></div>
      <button class="mi" type="button" role="menuitem" tabindex="-1" data-testid="ws-manage" @click="manage">
        <Icon name="layers" :size="16" /><span class="grow"><span class="t">Manage workspaces…</span><span class="sub">Opens the launcher</span></span>
      </button>
      <div class="mnote">Switching saves your open notebooks first.</div>
    </div>
  </div>
</template>
