<script setup lang="ts">
// PagePrompt — the allow prompt around an HTML page's file list (#162, H6): what
// kind of page it is, the queries it can run and the connection each runs on, the
// capabilities a scripted page declares with a plain line each, the macOS network
// disclosure and the warning about a page that asks for a credential. The
// sentences come from Go inside the same status read as the hash on screen
// (`status.disclosure`), so they are never from a different read than the
// bytes being allowed; when they are missing the prompt says so and the parent
// keeps Allow off. Query and connection names are the manifest's, shown as text.
import { computed } from 'vue'
import Icon from './Icon.vue'

const props = defineProps<{ status: ArtifactViewStatus }>()
const info = computed(() => props.status.disclosure ?? null)
const scripted = computed(() => props.status.page_kind === 'scripted')
</script>

<template>
  <div class="col page-prompt" data-testid="page-prompt">
    <div v-if="!info" class="note warn" role="alert" data-testid="page-prompt-missing">
      <Icon name="alert" :size="16" />
      <span>Couldn't load what this page can do, so it can't be allowed yet. Refresh to try again.</span>
    </div>
    <p v-else class="page-prompt__host" data-testid="page-prompt-host">{{ info.host }}</p>

    <div class="page-prompt__block">
      <strong class="page-prompt__h">{{ scripted ? 'Scripted page: queries it can run' : 'Static page: queries it uses' }}</strong>
      <p v-if="scripted" class="help" style="margin: 0;">A script can run any of the artifact's queries, not only the ones its templates name.</p>
      <ul v-if="status.query_uses?.length" class="page-prompt__list" data-testid="page-prompt-queries">
        <li v-for="q in status.query_uses" :key="q.name" :data-testid="`page-prompt-query-${q.name}`">
          <code>{{ q.name }}</code> on <code>{{ q.connection || 'the workspace database' }}</code>
          <span v-if="scripted" class="help"> ({{ q.used ? 'used by its templates' : 'script only' }})</span>
        </li>
      </ul>
      <p v-else class="help" data-testid="page-prompt-no-queries">The artifact has no queries.</p>
    </div>

    <div v-if="scripted" class="page-prompt__block">
      <strong class="page-prompt__h">Capabilities it asks for</strong>
      <ul v-if="info?.capabilities.length" class="page-prompt__list" data-testid="page-prompt-caps">
        <li v-for="c in info.capabilities" :key="c.name" :data-testid="`page-prompt-cap-${c.name}`">{{ c.text }}</li>
      </ul>
      <ul v-else-if="!info && status.capabilities?.length" class="page-prompt__list" data-testid="page-prompt-caps">
        <li v-for="c in status.capabilities" :key="c">{{ c }}</li>
      </ul>
      <p v-else-if="info" class="help" style="margin: 0;">It asks for no capabilities.</p>
    </div>

    <div v-if="info?.network" class="note warn" role="note" data-testid="page-prompt-network">
      <Icon name="alert" :size="16" /><span>{{ info.network }}</span>
    </div>
    <div v-if="info" class="note warn" role="note" data-testid="page-prompt-credential">
      <Icon name="alert" :size="16" /><span>{{ info.credential }}</span>
    </div>
  </div>
</template>

<style scoped>
.page-prompt { gap: var(--sh-space-3); }
.page-prompt__host { margin: 0; }
.page-prompt__block { display: flex; flex-direction: column; gap: var(--sh-space-1); }
.page-prompt__h { font-size: var(--sh-fs-sm); }
.page-prompt__list { margin: 0; padding-left: var(--sh-space-4); display: flex; flex-direction: column; gap: var(--sh-space-1); }
</style>
