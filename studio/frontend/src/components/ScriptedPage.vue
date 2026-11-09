<script setup lang="ts">
// ScriptedPage — an allowed scripted HTML page (#162, H4). It runs in its own
// native window, never in Studio's: this panel holds the artifact's parameter
// controls, Open page, and what became of the window. Nothing from the page
// reaches this component; the texts are fixed, or sent by Go.
import ArtifactControls from './ArtifactControls.vue'
import Icon from './Icon.vue'
import { onMac, usePageWindow } from '../composables/usePageWindow'
import type { ParamValue } from '../a2ui/artifactParams'

const props = defineProps<{ name: string; detail: ArtifactDetail; values: Record<string, ParamValue> }>()
const { state, unavailable, error, open, close } = usePageWindow(() => props.name, () => props.detail, () => props.values)
</script>

<template>
  <div class="scripted-page col" data-testid="scripted-page">
    <ArtifactControls :detail="detail" testid="scripted-page" />

    <div v-if="state === 'unavailable'" class="note warn" role="status" data-testid="scripted-page-unavailable">
      <Icon name="alert" :size="16" />
      <span>{{ unavailable }}</span>
    </div>
    <template v-else>
      <div class="row scripted-page__bar">
        <button v-if="state === 'idle' || state === 'closed'" type="button" class="btn btn-primary btn-sm" data-testid="scripted-page-open" @click="open()">Open page</button>
        <button v-else-if="state === 'stopped' || state === 'setup' || state === 'changed'" type="button" class="btn btn-primary btn-sm" data-testid="scripted-page-reopen" @click="open()">Reopen</button>
        <button v-else type="button" class="btn btn-sm" data-testid="scripted-page-close" @click="close()">Close window</button>
        <span v-if="state === 'opening'" class="sh-mono scripted-page__status" role="status" data-testid="scripted-page-opening">Opening…</span>
        <span v-else-if="state === 'open'" class="sh-mono scripted-page__status" role="status" data-testid="scripted-page-is-open">The page is open in its own window.</span>
      </div>
      <div v-if="state === 'stopped'" class="note warn" role="alert" data-testid="scripted-page-stopped">
        <Icon name="alert" :size="16" />
        <span>This page stopped responding. Its window was closed.</span>
      </div>
      <div v-else-if="state === 'setup'" class="note warn" role="alert" data-testid="scripted-page-setup">
        <Icon name="alert" :size="16" />
        <span>This page window couldn't be set up on this Mac.</span>
      </div>
      <div v-else-if="state === 'changed'" class="note warn" role="alert" data-testid="scripted-page-changed">
        <Icon name="alert" :size="16" />
        <span>This page changed, so its window was closed. Reopening checks it is still the page you allowed.</span>
      </div>
      <div v-else-if="state === 'idle' || state === 'closed'" class="note info" role="status" data-testid="scripted-page-about">
        <Icon name="info" :size="16" />
        <span>This page has script, so it opens in its own window. The window cannot reach your files or other pages, and can only run this artifact's queries. Slaw never asks you for a password or a key in a page window; if a page does, close it.</span>
      </div>
      <div v-if="state === 'idle' || state === 'closed'" class="note" :class="onMac() ? 'warn' : 'info'" role="status" data-testid="scripted-page-network">
        <Icon :name="onMac() ? 'alert' : 'info'" :size="16" />
        <span v-if="onMac()">On macOS a page can use a link to open a network connection to an address it chooses, though it cannot read a reply.</span>
        <span v-else>The window has no network of its own.</span>
      </div>
      <div v-if="error" class="note warn" role="alert" data-testid="scripted-page-error">
        <Icon name="alert" :size="16" />
        <span>{{ error }}</span>
      </div>
    </template>
  </div>
</template>

<style scoped>
.scripted-page { gap: var(--sh-space-3); min-width: 0; }
.scripted-page__bar { align-items: center; gap: var(--sh-space-3); }
.scripted-page__status { color: var(--sh-graphite); font-size: var(--sh-fs-xs); }
</style>
