<script setup lang="ts">
// BundleImportDialog — "Import .slaw…" (#118), from the workspace menu (into the
// open workspace) and from the launcher (into a new one). The file is verified
// and previewed before anything is written; each item that already exists gets
// its own Rename / Overwrite / Skip. A bundle holds no data and no credentials.
// The state and actions are useBundleImport.
import { ref } from 'vue'
import Icon from './Icon.vue'
import { useModalKeys } from '../composables/useModal'
import { useBundleImport, KIND_LABEL, keepBothLabel } from '../composables/useBundleImport'
import { workspaceInfo } from '../composables/useWorkspace'

const root = ref<HTMLElement | null>(null)
const passEl = ref<HTMLInputElement | null>(null)
const {
  open, mode, stage, passphrase, preview, busy, error, result, conflictChoice, included, connIncluded, wsName,
  close, load, conflicts, chosenCount, setAllConflicts, canImport, choose, submit, failed, summaryText, when,
  parentDir, git, gitAvailable, slug,
} = useBundleImport(passEl)
useModalKeys(open, () => close(), root)
</script>

<template>
  <div v-if="open" class="modal-overlay" role="presentation" @click.self="close">
    <div ref="root" class="dialog wide" role="dialog" aria-modal="true" aria-labelledby="bi-h" data-testid="bundle-import-dialog">
      <div class="d-head">
        <h2 id="bi-h">Import .slaw</h2>
        <p v-if="mode === 'new'">Start a new workspace from a bundle someone shared with you.</p>
        <p v-else>Add notebooks, queries, skills and data artifacts from a bundle to <b>{{ workspaceInfo?.name }}</b>.</p>
      </div>

      <div class="d-body">
        <div v-if="stage === 'picking' && !error" class="help">Choose a .slaw file…</div>

        <form v-if="stage === 'passphrase'" class="bi-pass" @submit.prevent="load">
          <div class="note info"><Icon name="lock" :size="16" /><span><b>This bundle is locked.</b> Enter the passphrase its author gave you. Slaw can't recover it.</span></div>
          <div class="field">
            <label for="bi-pass">Passphrase</label>
            <input id="bi-pass" ref="passEl" v-model="passphrase" class="field-input" :class="{ 'field-input-error': !!error }" type="password" autocomplete="off" :disabled="busy"
              :aria-invalid="error ? 'true' : undefined" aria-describedby="bi-pass-e" data-testid="bi-passphrase" />
            <div v-if="error" id="bi-pass-e" class="help" style="color: var(--slaw-error);" role="alert" data-testid="bi-wrong-pass">{{ error }}</div>
          </div>
        </form>

        <template v-if="stage === 'preview' && preview">
          <div class="bi-from">
            <Icon :name="preview.encrypted ? 'lock' : 'fileText'" :size="16" />
            <span>From <b>{{ preview.workspaceName || 'a workspace' }}</b><template v-if="when(preview.createdAt)"> · {{ when(preview.createdAt) }}</template><template v-if="preview.createdBy"> · {{ preview.createdBy }}</template></span>
          </div>
          <div class="note info"><Icon name="shieldCheck" :size="16" /><span><b>Contains no data and no credentials.</b> Everything below was checked before this screen. Nothing is written until you import.</span></div>

          <div v-if="mode === 'new'" class="bi-new" data-testid="bi-new-fields">
            <div style="display: grid; grid-template-columns: 1fr 1fr; gap: 16px;">
              <div class="field">
                <label for="bi-name">Workspace name</label>
                <input id="bi-name" v-model="wsName" class="field-input" autocomplete="off" autocapitalize="off" autocorrect="off" spellcheck="false" :disabled="busy" data-testid="bi-ws-name" />
              </div>
              <div class="field">
                <label for="bi-loc">Where to put it</label>
                <div class="row" style="gap: 8px;">
                  <input id="bi-loc" v-model="parentDir" class="field-input" autocomplete="off" autocapitalize="off" autocorrect="off" spellcheck="false" :disabled="busy" data-testid="bi-ws-location" />
                  <button class="btn" type="button" style="flex: none;" :disabled="busy" @click="choose">Choose…</button>
                </div>
              </div>
            </div>
            <div class="url-tile" style="font-size: 13px; margin-top: 8px;"><span class="sh-label" style="display: block; margin-bottom: 4px;">Will create</span>{{ parentDir ? parentDir.replace(/[\\/]+$/, '') + '/' : '' }}<span class="acc">{{ slug }}</span></div>
            <div class="switch-row" style="margin-top: 14px;">
              <input id="bi-git" v-model="git" type="checkbox" :disabled="!gitAvailable || busy" />
              <label for="bi-git"><span class="st-t">Start a git repository</span></label>
            </div>
          </div>

          <div v-if="conflicts.length" class="bi-bulk" data-testid="bi-bulk">
            <span>{{ conflicts.length }} already exist{{ conflicts.length === 1 ? 's' : '' }} here. For all of them:</span>
            <button class="linkbtn" type="button" @click="setAllConflicts('rename')">Keep both</button>
            <button class="linkbtn" type="button" @click="setAllConflicts('overwrite')">Replace</button>
            <button class="linkbtn" type="button" @click="setAllConflicts('skip')">Skip</button>
          </div>

          <ul class="bi-list" role="list" data-testid="bi-items">
            <li v-for="it in preview.items" :key="it.key" class="bi-item" :class="{ conflict: it.conflict }" data-testid="bi-item">
              <input :id="`bi-inc-${it.key}`" v-model="included[it.key]" type="checkbox" :aria-label="`Import ${it.title || it.id}`" :disabled="busy" />
              <label :for="`bi-inc-${it.key}`" class="bi-name">
                <span class="kind">{{ KIND_LABEL[it.kind] }}</span>
                <span class="nm">{{ it.title || it.id }}</span>
                <span class="sub">{{ it.id }}</span>
              </label>
              <span v-if="it.conflict" class="bi-choice">
                <span class="badge badge--warn"><span class="dot"></span>Already here</span>
                <span v-if="it.noRename" class="sub" data-testid="bi-norename">Can't keep both</span>
                <select v-model="conflictChoice[it.key]" class="field-input bi-select" :aria-label="`What to do with ${it.title || it.id}`" :disabled="busy || !included[it.key]" :data-testid="`bi-choice-${it.key}`">
                  <option v-if="!it.noRename" value="rename">{{ keepBothLabel(it.kind) }}</option>
                  <option value="overwrite">Replace mine</option>
                  <option value="skip">Skip</option>
                </select>
              </span>
            </li>
          </ul>
          <div v-if="!preview.items.length && !preview.connections.length" class="help">This bundle has nothing in it.</div>

          <fieldset v-if="preview.connections.length" class="bi-conns" data-testid="bi-connections">
            <legend>Connection definitions</legend>
            <p class="help" style="margin: 0 0 6px;">Added to <b>This computer</b> only, never to a shared file. They arrive without credentials, so each one asks you to sign in before it connects. Only add the ones you recognise.</p>
            <div v-for="c in preview.connections" :key="c.key" class="bi-conn" :data-testid="`bi-connrow-${c.name}`">
              <label class="bi-conn-head">
                <input v-model="connIncluded[c.name]" type="checkbox" :disabled="busy" :data-testid="`bi-conn-${c.name}`" />
                <span class="nm">{{ c.name }}</span>
                <span class="sub">{{ c.type }}</span>
                <span class="badge badge--graphite">{{ c.askedWritable ? 'imported read-only' : 'read-only' }}</span>
              </label>
              <ul v-if="c.details.length" class="bi-details" role="list" :aria-label="`${c.name} settings`">
                <li v-for="d in c.details" :key="d">{{ d }}</li>
              </ul>
              <div v-if="c.askedWritable" class="bi-risk" data-testid="bi-asked-writable">The bundle asked for write access. It is imported read-only; you can turn writes on later.</div>
              <div v-for="r in c.risks" :key="r" class="bi-risk" role="alert" data-testid="bi-risk">{{ r }}</div>
              <span v-if="c.conflict" class="badge badge--warn"><span class="dot"></span>{{ c.conflictScope }} has one of this name; this one is added as {{ c.renamedTo }}</span>
            </div>
          </fieldset>

          <div v-if="preview.warnings.length" class="note warn" role="status" data-testid="bi-warnings">
            <Icon name="alert" :size="16" />
            <span><b>Warnings</b><span v-for="w in preview.warnings" :key="w" style="display: block;">{{ w }}</span></span>
          </div>
          <div v-if="preview.makerNotes.length" class="note maker" data-testid="bi-maker-notes">
            <Icon name="fileText" :size="16" />
            <span><b>From the person who made this bundle</b> (not checked by Slaw)<span v-for="w in preview.makerNotes" :key="w" style="display: block;">{{ w }}</span></span>
          </div>
        </template>

        <template v-if="stage === 'done' && result">
          <div class="note" data-testid="bi-done">
            <Icon name="circleCheck" :size="16" />
            <span>
              <b>Imported.</b> {{ summaryText }}
            </span>
          </div>
          <ul v-if="result.outcomes.length" class="bi-list" role="list">
            <li v-for="o in result.outcomes" :key="o.key" class="bi-item">
              <span class="bi-name"><span class="kind">{{ KIND_LABEL[o.kind] ?? o.kind }}</span><span class="nm">{{ o.id }}</span><span v-if="o.finalId && o.finalId !== o.id" class="sub">saved as {{ o.finalId }}</span></span>
              <span class="bi-result" :class="{ bad: !!o.error }">{{ o.error ? 'Failed' : o.result }}</span>
            </li>
          </ul>
          <div v-for="f in failed" :key="f.key" class="note attn" role="alert"><Icon name="alert" :size="16" /><span><b>{{ f.id }}</b> {{ f.error }}</span></div>
        </template>

        <div v-if="error && stage !== 'passphrase'" class="note attn" role="alert" data-testid="bi-error"><Icon name="alert" :size="16" /><span>{{ error }}</span></div>
      </div>

      <div class="d-foot">
        <span v-if="stage === 'preview' && preview" class="left">{{ chosenCount }} to import</span>
        <button class="btn" type="button" :disabled="busy" @click="close">{{ stage === 'done' ? 'Done' : 'Cancel' }}</button>
        <button v-if="stage === 'passphrase'" class="btn btn-primary" type="button" :disabled="busy || !passphrase" data-testid="bi-unlock" @click="load">{{ busy ? 'Opening…' : 'Open bundle' }}</button>
        <button v-if="stage === 'preview' && preview" class="btn btn-primary" type="button" :disabled="!canImport" data-testid="bi-import" @click="submit">{{ busy ? 'Importing…' : mode === 'new' ? 'Create workspace and import' : 'Import' }}</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.bi-pass { display: flex; flex-direction: column; gap: 16px; }
.bi-from svg { flex: none; }
.bi-from { display: flex; align-items: center; gap: 8px; font-size: 14px; color: var(--sh-graphite); }
.bi-bulk { display: flex; gap: 12px; align-items: center; font-size: 13px; flex-wrap: wrap; }
.bi-list { flex: none; list-style: none; margin: 0; padding: 0; border: 1.5px solid var(--slaw-border); border-radius: var(--sh-radius-sm); overflow: hidden; }
.bi-item { display: flex; align-items: center; gap: 10px; padding: 8px 12px; border-bottom: 1px solid var(--slaw-border); min-width: 0; }
.bi-item:last-child { border-bottom: 0; }
.bi-item.conflict { background: color-mix(in srgb, var(--slaw-warn) 8%, transparent); }
.bi-name { display: flex; align-items: baseline; gap: 8px; min-width: 0; flex: 1; cursor: pointer; }
.bi-name .kind { font-size: 11px; text-transform: uppercase; letter-spacing: 0.04em; color: var(--sh-graphite); flex: none; min-width: 96px; }
.bi-name .nm { font-size: 14px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.bi-name .sub, .bi-conn .sub { font-size: 12px; color: var(--sh-graphite); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.bi-choice { display: flex; align-items: center; gap: 8px; flex: none; }
.bi-select { width: 210px; padding-top: 4px; padding-bottom: 4px; }
.bi-result { font-size: 13px; color: var(--sh-graphite); }
.bi-result.bad { color: var(--slaw-error); }
.bi-conns { border: 0; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 6px; }
.bi-conns legend { font-weight: 600; font-size: 14px; padding: 0; margin-bottom: 4px; }
.bi-conn { display: flex; flex-direction: column; gap: 4px; padding: 8px 0; border-top: 1px solid var(--slaw-border-subtle); }
.bi-conn-head { display: flex; align-items: baseline; gap: 10px; flex-wrap: wrap; }
.bi-details { list-style: none; margin: 0 0 0 24px; padding: 0; font-family: var(--sh-font-mono); font-size: 12px; color: var(--sh-graphite); }
.bi-risk { margin-left: 24px; font-size: 13px; padding: 4px 10px; border-left: 3px solid var(--slaw-warn); background: color-mix(in srgb, var(--slaw-warn) 10%, transparent); }
.note.maker { border-left-color: var(--slaw-border); background: var(--sh-paper-2); }
.bi-from, .bi-details li, .bi-risk, .note, .bi-name .nm, .bi-conn .nm { overflow-wrap: anywhere; }
/* The body scrolls; nothing inside it shrinks to fit (a long list would
   otherwise be squeezed and its rows hidden). */
.d-body > * { flex-shrink: 0; }
.d-body { max-height: min(62vh, 600px); overflow-y: auto; }
</style>
