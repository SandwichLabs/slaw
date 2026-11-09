<script setup lang="ts">
import { App } from '../lib/bindings'
// BundleExportDialog — "Export workspace content…" (#118). Pick what goes in a
// .slaw bundle, optionally lock it with a passphrase, and save. A bundle holds
// no data and no credentials; the dialog says so where the person is deciding.
import { ref, computed, watch } from 'vue'
import Icon from './Icon.vue'
import { useModalKeys } from '../composables/useModal'
import { bundleExportOpen, formatBytes } from '../composables/useBundle'
import { errorText } from '../lib/errors'

const root = ref<HTMLElement | null>(null)
useModalKeys(bundleExportOpen, () => close(), root)

const content = ref<BundleContent | null>(null)
const loading = ref(false)
const busy = ref(false)
const error = ref('')
const done = ref<BundleExportResult | null>(null)

// Ticked ids per group: nb notebooks, qs queries, sk skills, ar data artifacts, sm semantic definitions, wt watches, cn connections.
const picked = ref<Record<string, Set<string>>>({ nb: new Set(), qs: new Set(), sk: new Set(), ar: new Set(), sm: new Set(), wt: new Set(), cn: new Set() })
const encrypt = ref(false)
const pass1 = ref('')
const pass2 = ref('')

function close() {
  if (busy.value) return
  bundleExportOpen.value = false
}

watch(bundleExportOpen, async (open) => {
  if (!open) { pass1.value = ''; pass2.value = ''; return }
  content.value = null
  done.value = null
  error.value = ''
  encrypt.value = false
  pass1.value = ''
  pass2.value = ''
  loading.value = true
  try {
    const c = await App.GetBundleContent()
    content.value = c
    picked.value = {
      nb: new Set(c.notebooks.map((x) => x.id)),
      qs: new Set(c.queries.map((x) => x.id)),
      sk: new Set(c.skills.map((x) => x.id)),
      ar: new Set(c.artifacts.map((x) => x.id)),
      sm: new Set(c.semantics.map((x) => x.id)),
      wt: new Set((c.watches).map((x) => x.id)),
      cn: new Set(),
    }
  } catch (e: unknown) {
    error.value = errorText(e)
  }
  loading.value = false
})

function toggle(key: string, id: string, on: boolean) {
  const next = new Set(picked.value[key])
  if (on) next.add(id); else next.delete(id)
  picked.value = { ...picked.value, [key]: next }
}

function setAll(key: string, ids: string[], on: boolean) {
  picked.value = { ...picked.value, [key]: on ? new Set(ids) : new Set() }
}

const portableConns = computed(() => (content.value?.connections ?? []).filter((c) => c.portable))
const blockedConns = computed(() => (content.value?.connections ?? []).filter((c) => !c.portable))
const total = computed(() => picked.value.nb.size + picked.value.qs.size + picked.value.sk.size + picked.value.ar.size + picked.value.sm.size + picked.value.wt.size + picked.value.cn.size)
const passMismatch = computed(() => encrypt.value && pass2.value !== '' && pass1.value !== pass2.value)
const valid = computed(() => total.value > 0 && (!encrypt.value || (pass1.value !== '' && pass1.value === pass2.value)))

async function submit() {
  if (!valid.value || busy.value || !content.value) return
  busy.value = true
  error.value = ''
  const team = portableConns.value.filter((c) => c.scope === 'team' && picked.value.cn.has(c.name)).map((c) => c.name)
  const local = portableConns.value.filter((c) => c.scope === 'this computer' && picked.value.cn.has(c.name)).map((c) => c.name)
  try {
    const res = await App.ExportBundle({
      path: '',
      selection: {
        all: false,
        notebooks: [...picked.value.nb], queries: [...picked.value.qs], skills: [...picked.value.sk], artifacts: [...picked.value.ar], semantics: [...picked.value.sm], watches: [...picked.value.wt],
        sharedConnections: false, teamConnections: team, localConnections: local,
      },
      passphrase: encrypt.value ? pass1.value : '',
    })
    if (res.cancelled) { busy.value = false; return }
    done.value = res
    pass1.value = ''
    pass2.value = ''
  } catch (e: unknown) {
    error.value = errorText(e)
  }
  busy.value = false
}
</script>

<template>
  <div v-if="bundleExportOpen" class="modal-overlay" role="presentation" @click.self="close">
    <div ref="root" class="dialog wide" role="dialog" aria-modal="true" aria-labelledby="be-h" data-testid="bundle-export-dialog">
      <div class="d-head">
        <h2 id="be-h">Export workspace content</h2>
        <p>Make a <code>.slaw</code> file you can send to someone, or keep as a backup. They import it into their own workspace.</p>
      </div>

      <div v-if="done" class="d-body" data-testid="bundle-export-done">
        <div class="note">
          <Icon name="circleCheck" :size="16" />
          <span>
            <b>Saved {{ done.notebooks }} notebook{{ done.notebooks === 1 ? '' : 's' }}, {{ done.queries }} quer{{ done.queries === 1 ? 'y' : 'ies' }},
            {{ done.skills }} skill{{ done.skills === 1 ? '' : 's' }},
            {{ done.artifacts }} data artifact{{ done.artifacts === 1 ? '' : 's' }},
            {{ done.semantics }} semantic definition{{ done.semantics === 1 ? '' : 's' }}
            and {{ done.watches }} watch{{ done.watches === 1 ? '' : 'es' }}<template v-if="done.connections">, plus connection definitions</template></b>
            ({{ formatBytes(done.bytes) }}<template v-if="done.encrypted">, locked with a passphrase</template>).
            <span class="path-line">{{ done.path }}</span>
          </span>
        </div>
        <div v-if="done.warnings.length" class="note warn" role="status" data-testid="bundle-export-warnings">
          <Icon name="alert" :size="16" />
          <span><b>Left out or changed</b>
            <span v-for="w in done.warnings" :key="w" style="display: block;">{{ w }}</span>
          </span>
        </div>
      </div>

      <div v-else class="d-body">
        <div v-if="loading" class="help">Reading the workspace…</div>
        <template v-if="content">
          <div class="note info" data-testid="bundle-no-data-note">
            <Icon name="shieldCheck" :size="16" />
            <span><b>Contains no data and no credentials.</b> Your tables stay where they are. Connection definitions (if you pick any) never include passwords, tokens or file paths outside this folder.</span>
          </div>

          <fieldset v-for="g in [
            { key: 'nb', title: 'Notebooks', items: content.notebooks },
            { key: 'qs', title: 'Saved queries', items: content.queries },
            { key: 'sk', title: 'Skills', items: content.skills },
            { key: 'ar', title: 'Data artifacts', items: content.artifacts },
            { key: 'sm', title: 'Semantic definitions', items: content.semantics },
            { key: 'wt', title: 'Watches', items: content.watches },
          ]" :key="g.key" class="be-group">
            <legend>
              {{ g.title }} <span class="count">{{ picked[g.key].size }} of {{ g.items.length }}</span>
            </legend>
            <div v-if="!g.items.length" class="help">None in this workspace.</div>
            <template v-else>
              <button class="linkbtn" type="button" :data-testid="`be-all-${g.key}`" @click="setAll(g.key, g.items.map((i) => i.id), picked[g.key].size !== g.items.length)">
                {{ picked[g.key].size === g.items.length ? 'Select none' : 'Select all' }}
              </button>
              <label v-for="i in g.items" :key="i.id" class="be-row">
                <input type="checkbox" :checked="picked[g.key].has(i.id)" :data-testid="`be-${g.key}-${i.id}`" @change="toggle(g.key, i.id, ($event.target as HTMLInputElement).checked)" />
                <span class="nm">{{ i.title || i.id }}</span>
                <span v-if="g.key === 'sk'" class="sub">{{ i.id }}</span>
                <span v-else class="sub">{{ i.id }}</span>
              </label>
            </template>
          </fieldset>

          <fieldset class="be-group">
            <legend>Connection definitions <span class="count">optional</span></legend>
            <div v-if="!content.connections.length" class="help">This workspace has no connections to share.</div>
            <template v-else>
              <p class="help" style="margin: 0;">Only where the data lives (host, database, bucket). Whoever imports it signs in with their own credentials.</p>
              <label v-for="c in portableConns" :key="c.name" class="be-row">
                <input type="checkbox" :checked="picked.cn.has(c.name)" :data-testid="`be-conn-${c.name}`" @change="toggle('cn', c.name, ($event.target as HTMLInputElement).checked)" />
                <span class="nm">{{ c.name }}</span>
                <span class="sub">{{ c.type }} · {{ c.scope }}<template v-if="c.summary"> · {{ c.summary }}</template></span>
              </label>
              <div v-for="c in blockedConns" :key="c.name" class="be-row be-blocked">
                <input type="checkbox" disabled :aria-label="`${c.name} can't be exported`" />
                <span class="nm">{{ c.name }}</span>
                <span class="sub">{{ c.reason }}</span>
              </div>
            </template>
          </fieldset>

          <div class="switch-row">
            <input id="be-enc" v-model="encrypt" type="checkbox" :disabled="busy" data-testid="be-encrypt" />
            <label for="be-enc">
              <span class="st-t">Lock with a passphrase</span>
              <span class="st-d">Encrypts the whole file. Whoever imports it needs the passphrase; if you forget it, nobody can open the file.</span>
            </label>
          </div>
          <div v-if="encrypt" style="display: grid; grid-template-columns: 1fr 1fr; gap: 16px;">
            <div class="field">
              <label for="be-p1">Passphrase</label>
              <input id="be-p1" v-model="pass1" class="field-input" type="password" autocomplete="new-password" :disabled="busy" data-testid="be-pass1" />
            </div>
            <div class="field">
              <label for="be-p2">Type it again</label>
              <input id="be-p2" v-model="pass2" class="field-input" :class="{ 'field-input-error': passMismatch }" type="password" autocomplete="new-password" :disabled="busy"
                :aria-invalid="passMismatch ? 'true' : undefined" aria-describedby="be-p2-h" data-testid="be-pass2" />
              <div v-if="passMismatch" id="be-p2-h" class="help" style="color: var(--slaw-error);" role="alert">The two don't match.</div>
            </div>
          </div>
        </template>
        <div v-if="error" class="note attn" role="alert" data-testid="bundle-export-error"><Icon name="alert" :size="16" /><span>{{ error }}</span></div>
      </div>

      <div class="d-foot">
        <span v-if="!done" class="left">{{ total }} selected</span>
        <button class="btn" type="button" :disabled="busy" @click="close">{{ done ? 'Done' : 'Cancel' }}</button>
        <button v-if="!done" class="btn btn-primary" type="button" :disabled="!valid || busy" data-testid="be-export" @click="submit">
          {{ busy ? 'Exporting…' : 'Export…' }}
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.be-group { border: 0; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 6px; }
.be-group .linkbtn { align-self: flex-start; }
.be-group legend { font-weight: 600; font-size: 14px; padding: 0; margin-bottom: 4px; }
.be-group legend .count { font-weight: 400; font-size: 12px; color: var(--sh-graphite); margin-left: 8px; }
.be-row { display: flex; align-items: baseline; gap: 10px; padding: 4px 0; min-width: 0; }
.be-row input { flex: none; }
.be-row .nm { font-size: 14px; }
.be-row .sub { font-size: 12px; color: var(--sh-graphite); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; min-width: 0; }
.be-blocked { opacity: 0.75; }
.path-line { display: block; margin-top: 4px; font-family: var(--sh-font-mono); font-size: 12px; word-break: break-all; }
.d-body { max-height: min(60vh, 560px); overflow-y: auto; }
.d-body > * { flex-shrink: 0; }
.be-row .sub, .be-row .nm, .note { overflow-wrap: anywhere; }
</style>
