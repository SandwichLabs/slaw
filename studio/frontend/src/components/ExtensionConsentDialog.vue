<script setup lang="ts">
import { App } from '../lib/bindings'
// ExtensionConsentDialog — SPEC 07 B. The one place Slaw downloads something:
// a DuckDB extension a connection type needs. Nothing downloads without a yes.
import { ref, computed, watch } from 'vue'
import Icon from './Icon.vue'
import { useModalKeys } from '../composables/useModal'
import { extensionFor, connectionList, refreshConnections } from '../composables/useConnections'
import { errorText } from '../lib/errors'

const open = computed(() => extensionFor.value !== null)
const root = ref<HTMLElement | null>(null)
useModalKeys(open, () => close(), root)
const busy = ref(false)
const error = ref('')

const ext = computed(() => extensionFor.value?.extension || '')
const neededBy = computed(() => connectionList.value.filter((c) => c.extension === ext.value || c.name === extensionFor.value?.connection))
const what = computed(() => {
  switch (ext.value) {
    case 'httpfs': return 'To read files from S3, Google Cloud Storage or R2, Slaw’s database engine needs a small add-on called the'
    case 'postgres': return 'To read a Postgres database, Slaw’s database engine needs a small add-on called the'
    case 'sqlite': return 'To read SQLite files, Slaw’s database engine needs a small add-on called the'
    case 'motherduck': return 'To connect to MotherDuck, Slaw’s database engine needs a small add-on called the'
    case 'mysql': return 'To read a MySQL database, Slaw’s database engine needs a small add-on called the'
    case 'azure': return 'To read files from Azure Blob Storage or Data Lake, Slaw’s database engine needs a small add-on called the'
    case 'ducklake': return 'To use a DuckLake, Slaw’s database engine needs a small add-on called the'
    case 'iceberg': return 'To read Iceberg tables, Slaw’s database engine needs a small add-on called the'
    case 'avro': return 'Iceberg tables keep their metadata in Avro files, so reading one needs two small add-ons (this one first, then iceberg). The first is called the'
    case 'delta': return 'To read Delta Lake tables, Slaw’s database engine needs a small add-on called the'
  }
  return 'Slaw’s database engine needs a small add-on called the'
})

watch(extensionFor, () => { busy.value = false; error.value = '' })

function close() {
  if (!busy.value) extensionFor.value = null
}

async function install() {
  busy.value = true
  error.value = ''
  try {
    await App.InstallExtension({ name: ext.value })
    extensionFor.value = null
    await refreshConnections()
  } catch (e: unknown) {
    error.value = errorText(e)
  }
  busy.value = false
}
</script>

<template>
  <div v-if="open" class="modal-overlay" role="presentation" @click.self="close">
    <div ref="root" class="dialog" role="dialog" aria-modal="true" aria-labelledby="ec-h" aria-describedby="ec-d" data-testid="extension-dialog">
      <div class="d-head"><h2 id="ec-h">Install the {{ ext }} extension?</h2></div>
      <div id="ec-d" class="d-body">
        <p>{{ what }} <code>{{ ext }}</code> extension. It isn't included, so Slaw needs to download it once.</p>
        <ul class="bullets" style="margin: 0; padding-left: 20px; list-style: disc; font-size: 14px; line-height: 1.6;">
          <li>Downloaded from <code>extensions.duckdb.org</code>.</li>
          <li>Kept in Slaw's own folder, shared by all workspaces, never touching an existing DuckDB install.</li>
          <li>Needed by {{ neededBy.length || 1 }} connection{{ (neededBy.length || 1) === 1 ? '' : 's' }} here<template v-if="neededBy.length">: {{ neededBy.map((c) => c.name).join(', ') }}</template>.</li>
        </ul>
        <div class="note info"><Icon name="info" :size="16" /><span>Offline? You can skip this and the connection will wait with an "Extension needed" icon.</span></div>
        <div v-if="busy" class="note" role="status"><Icon name="download" :size="16" /><span>Installing…</span></div>
        <div v-if="error" class="note attn" role="alert" data-testid="extension-error"><Icon name="alert" :size="16" /><span>{{ error }}</span></div>
      </div>
      <div class="d-foot">
        <button class="btn" type="button" :disabled="busy" @click="close">Not now</button>
        <button class="btn btn-primary" type="button" :disabled="busy" data-testid="extension-install" @click="install">{{ error ? 'Try again' : `Install ${ext} extension` }}</button>
      </div>
    </div>
  </div>
</template>
