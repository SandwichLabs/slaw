<script setup lang="ts">
// ConnectionEditorView — add or edit a connection (SPEC 06). A full-page route
// (#/connections/new, #/connections/<name>) so there is room to explain and the
// rail stays visible. The form is generated from the type registry; only the
// secret half of a credential is write-only (password, token, secret key), the
// username and key id are readable. Leaving with unsaved edits asks first.
// The form is useConnectionForm, the MotherDuck picker useMotherDuckPicker, and
// test / save / remove are useConnectionEditor.
import { useRouter } from 'vue-router'
import Icon from '../components/Icon.vue'
import ConnTypeTile from '../components/ConnTypeTile.vue'
import ConnectionTypePicker from '../components/ConnectionTypePicker.vue'
import CredentialField from '../components/CredentialField.vue'
import S3PresetPicker from '../components/S3PresetPicker.vue'
import SsoSignIn from '../components/SsoSignIn.vue'
import StorageNeed from '../components/StorageNeed.vue'
import ScopePicker from '../components/ScopePicker.vue'
import TestResult from '../components/TestResult.vue'
import { shortcutLabel } from '../composables/useModal'
import { useConnectionForm } from '../composables/useConnectionForm'
import { useMotherDuckPicker } from '../composables/useMotherDuckPicker'
import { useConnectionEditor } from '../composables/useConnectionEditor'
import { typeLabel, scopeLabel, CRED_KEY as KEY } from '../lib/connections'

const router = useRouter()
const form = useConnectionForm()
const {
  types, loading, loadError, isNew, editName, typeId, spec, f, readOnly, connScope, method, envPrefix, ssoProfile, typed,
  status, detail, advancedOpen, saveError, result, isMD, remote, mainFields, advFields, metadataDriven,
  insideWorkspace, exampleTail, portField, readableCreds, secretCreds, needsSignIn, envVars, credFields, val, setVal,
  bindingChanged, fingerprintHelp, secretIsSet, serverChangedHelp, privateCopy, shadowedBy, failedTest, title,
  chooseType, presets, applyPreset, storageLine, storageSlot, storageFailed, prefilled, changedWhileAway,
} = form
const md = useMotherDuckPicker(form)
const { mdDbs, mdLoading, mdLoaded, mdError, mdInstall, mdTokenReady, loadDatabases, pickDatabase } = md
const {
  saving, savedNote, testing, resultEl, errorEl, keychainBlocked, keychainDetail,
  runTest, installFor, save, clearSecret, useEnvInstead, removeMine, remove, cancel, handoff,
} = useConnectionEditor(form, md)
</script>

<template>
  <div class="page-narrow" style="height: 100%; overflow-y: auto; padding: 32px;" data-testid="connection-editor">
    <div class="headrow">
      <div>
        <div class="crumb">Studio <span class="sep">/</span> Connections <span class="sep">/</span> {{ isNew ? 'New' : editName }}</div>
        <h1>{{ title }}</h1>
      </div>
      <div v-if="!isNew" style="display: flex; gap: 8px;">
        <router-link class="btn btn-sm" :to="{ path: '/connections/new', query: { from: editName } }" data-testid="editor-duplicate"><Icon name="copy" :size="14" /> Duplicate</router-link>
        <button type="button" class="btn btn-sm btn-destructive" data-testid="editor-remove" @click="remove"><Icon name="trash" :size="14" /> Remove…</button>
      </div>
    </div>

    <div v-if="loading" class="help">Loading…</div>
    <div v-else-if="loadError" class="note attn" role="alert"><Icon name="alert" :size="16" /><span>{{ loadError }}</span></div>

    <!-- Step 1: type -->
    <section v-else-if="!typeId" aria-labelledby="pick-h">
      <h2 id="pick-h" style="font-size: 20px; margin-bottom: 12px;">Where does the data live?</h2>
      <ConnectionTypePicker :types="types" @choose="chooseType" />
      <div style="margin-top: 20px;"><button type="button" class="btn" @click="cancel">Cancel</button></div>
    </section>

    <!-- Step 2: the generated form -->
    <form v-else class="card" style="margin-top: 16px;" novalidate @submit.prevent="save">
      <div v-if="handoff.returning.value" class="note info" role="status" style="margin: 16px 24px 0;" data-testid="storage-handoff">
        <Icon name="info" :size="16" />
        <span>Setting up storage for <b>{{ handoff.lakeName.value }}</b>. Save returns to it with the storage connection in place.
          <button type="button" class="linkbtn" data-testid="handoff-back" @click="cancel">Back to {{ handoff.lakeName.value }}</button></span>
      </div>
      <div v-else-if="prefilled" class="note info" style="margin: 16px 24px 0;" role="status" data-testid="prefill-note">
        <Icon name="info" :size="16" />
        <span>Some fields were filled in from a link. Check the scope and name before you save.</span>
      </div>
      <div v-if="changedWhileAway" class="note warn" role="status" style="margin: 16px 24px 0;" data-testid="changed-away">
        <Icon name="alert" :size="16" />
        <span><b>{{ f.name }} changed on disk while you were away.</b> Your form is back as you left it. Check it before you save.</span>
      </div>
      <div v-if="privateCopy" class="note info" style="margin: 16px 24px 0;" data-testid="shadow-banner">
        <Icon name="layers" :size="16" />
        <span><b>This is your private copy of {{ f.name }}.</b> On this computer it overrides the {{ scopeLabel(detail!.shadows[0]) }} <code>{{ f.name }}</code>.
          <button type="button" class="linkbtn" @click="router.push({ path: `/connections/${f.name}`, query: { scope: detail!.shadows[0] } })">Look at the {{ scopeLabel(detail!.shadows[0]) }} one</button>
          <button type="button" class="linkbtn danger" @click="removeMine">Remove my copy</button></span>
      </div>
      <div v-if="shadowedBy" class="note warn" style="margin: 16px 24px 0;">
        <Icon name="layers" :size="16" />
        <span>This {{ scopeLabel(detail!.scope) }} copy is overridden on this computer by your private copy.
          <button type="button" class="linkbtn" @click="router.push(`/connections/${f.name}`)">Open my copy</button></span>
      </div>

      <!-- Connection -->
      <div class="form-sec">
        <h3><ConnTypeTile :type="typeId" /> {{ typeLabel(typeId) }} <span class="label">Connection</span></h3>
        <div class="field">
          <label class="field-label" for="f-name">Name</label>
          <input id="f-name" v-model="f.name" class="field-input" :readonly="!isNew" autocomplete="off" autocapitalize="off" autocorrect="off" spellcheck="false" aria-describedby="f-name-h" data-testid="f-name" />
          <div id="f-name-h" class="help">How you'll refer to it in SQL: <code>{{ f.name || 'prod_pg' }}<template v-if="spec?.capabilities.catalog">{{ exampleTail }}</template></code>. <template v-if="isNew">Lowercase letters, numbers and underscores. It can't be changed after you save.</template><template v-else>It can't be changed after saving.</template></div>
        </div>

        <div v-for="fd in mainFields" :key="fd.name" :class="fd.name === 'host' && portField ? 'form-grid' : ''">
          <div class="field">
            <label class="field-label" :for="`f-${fd.name}`">{{ fd.label }}<span v-if="fd.required" aria-hidden="true"> *</span></label>
            <div style="display: flex; gap: 8px;">
              <input :id="`f-${fd.name}`" :value="val(fd)" class="field-input" style="flex: 1;" :type="fd.kind === 'number' ? 'number' : 'text'" autocomplete="off" autocapitalize="off" autocorrect="off" spellcheck="false" :aria-describedby="fd.help || fd.name === 'database' ? `f-${fd.name}-h` : undefined" :data-testid="`f-${fd.name}`" @input="setVal(fd, ($event.target as HTMLInputElement).value)" />
            </div>
            <div v-if="fd.help || fd.name === 'database'" :id="`f-${fd.name}-h`" class="help">{{ fd.help }}<template v-if="fd.name === 'database' && remote && fingerprintHelp">{{ ' ' + fingerprintHelp }}</template></div>
            <div v-if="fd.name === 'scope' && handoff.wholeBucket.value" class="help">
              <button type="button" class="linkbtn" data-testid="whole-bucket" @click="handoff.useWholeBucket">Use the whole bucket, <code>{{ handoff.wholeBucket.value }}</code></button>
              Queries and the Slaw assistant can then read anything in it.
            </div>
            <div v-if="storageSlot(fd)" class="storage-slot" role="status" aria-live="polite">
              <StorageNeed v-if="storageLine(fd)" :need="storageLine(fd)!" :testid="`storage-${fd.name}`" @setup="handoff.setup" />
              <div v-else-if="storageFailed" class="help" data-testid="storage-failed">Couldn't check storage connections just now.</div>
            </div>
            <div v-if="isMD && fd.name === 'database'" class="md-picker" data-testid="md-picker">
              <div class="md-picker-bar">
                <button type="button" class="btn btn-sm" :disabled="mdLoading || !mdTokenReady" data-testid="md-load" @click="loadDatabases">
                  <Icon name="refresh" :size="14" /> {{ mdLoading ? 'Loading databases…' : mdLoaded ? 'Refresh databases' : 'Load databases' }}
                </button>
                <span v-if="!mdTokenReady && !mdLoading" class="help" data-testid="md-hint">Enter your token below to pick from your databases, or type the name.</span>
              </div>
              <div v-if="mdLoading" class="help" role="status" data-testid="md-loading">Asking MotherDuck which databases this token can reach…</div>
              <div v-else-if="mdError" class="note attn" role="alert" data-testid="md-error">
                <Icon name="alert" :size="16" />
                <span>{{ mdError }}
                  <button v-if="mdInstall" type="button" class="linkbtn" data-testid="md-install" @click="installFor(mdInstall)">Install the {{ mdInstall }} extension…</button></span>
              </div>
              <div v-else-if="mdLoaded && !mdDbs.length" class="help" role="status" data-testid="md-empty">This account has no databases yet. You can still type a name.</div>
              <div v-else-if="mdLoaded" role="group" aria-label="Your MotherDuck databases" class="md-dbs" data-testid="md-dbs">
                <button v-for="d in mdDbs" :key="d.name" type="button" class="md-db" :class="{ on: f.database === d.name }" :aria-pressed="f.database === d.name" :data-testid="`md-db-${d.name}`" @click="pickDatabase(d.name)">
                  {{ d.name }}<span v-if="d.share" class="md-share">shared</span>
                </button>
              </div>
            </div>
          </div>
          <div v-if="fd.name === 'host' && portField" class="field">
            <label class="field-label" for="f-port">{{ portField.label }}</label>
            <input id="f-port" :value="val(portField)" class="field-input" type="number" :placeholder="portField.default" data-testid="f-port" @input="setVal(portField, ($event.target as HTMLInputElement).value)" />
          </div>
        </div>

        <S3PresetPicker v-if="presets.length" :presets="presets" @pick="applyPreset" />

        <details v-if="advFields.length" :open="advancedOpen" @toggle="advancedOpen = ($event.target as HTMLDetailsElement).open">
          <summary style="cursor: pointer; font-weight: 600;">Advanced options</summary>
          <div style="display: flex; flex-direction: column; gap: 14px; margin-top: 12px;">
            <div v-for="fd in advFields" :key="fd.name" class="field">
              <label class="field-label" :for="`f-${fd.name}`">{{ fd.label }}</label>
              <select v-if="fd.kind === 'enum'" :id="`f-${fd.name}`" class="field-input" :value="val(fd)" :data-testid="`f-${fd.name}`" @change="setVal(fd, ($event.target as HTMLSelectElement).value)">
                <option value="">{{ fd.default ? `Default (${fd.default})` : 'Default' }}</option>
                <option v-for="c in fd.choices" :key="c" :value="c">{{ c }}</option>
              </select>
              <input v-else :id="`f-${fd.name}`" class="field-input" :value="val(fd)" autocomplete="off" autocapitalize="off" autocorrect="off" spellcheck="false" :data-testid="`f-${fd.name}`" @input="setVal(fd, ($event.target as HTMLInputElement).value)" />
              <div v-if="fd.help" class="help">{{ fd.help }}</div>
            </div>
          </div>
        </details>
      </div>

      <!-- Sign-in -->
      <div v-if="needsSignIn" class="form-sec">
        <h3>Sign-in <span class="label">Just you · this computer</span></h3>
        <div class="opt-row" :class="{ four: spec?.signInMethods.includes('aws-sso') }" role="radiogroup" aria-label="How you sign in">
          <button type="button" role="radio" class="opt-card" :class="{ on: method === 'keychain' }" :aria-checked="method === 'keychain'" data-testid="method-keychain" @click="method = 'keychain'">
            <span><span class="ot">My keychain</span><span class="od">Saved once on this computer.</span></span>
          </button>
          <button type="button" role="radio" class="opt-card" :class="{ on: method === 'env' }" :aria-checked="method === 'env'" data-testid="method-env" @click="method = 'env'; envPrefix = envPrefix || f.name.toUpperCase()">
            <span><span class="ot">Environment variables</span><span class="od">Read from <code>{{ envVars[0]?.name }}</code> and friends when Slaw starts. Good for scripts and servers.</span></span>
          </button>
          <button type="button" role="radio" class="opt-card" :class="{ on: method === 'prompt' }" :aria-checked="method === 'prompt'" data-testid="method-prompt" @click="method = 'prompt'">
            <span><span class="ot">Ask me each time</span><span class="od">Slaw asks when it connects and forgets it when you close the workspace.</span></span>
          </button>
          <button v-if="spec?.signInMethods.includes('aws-sso')" type="button" role="radio" class="opt-card" :class="{ on: method === 'aws-sso' }" :aria-checked="method === 'aws-sso'" data-testid="method-aws-sso" @click="method = 'aws-sso'">
            <span><span class="ot">AWS profile (SSO)</span><span class="od">Sign in with your IAM Identity Center profile. Short-lived keys, nothing stored.</span></span>
          </button>
        </div>

        <div v-if="method === 'env' && bindingChanged" class="note warn" role="status" data-testid="env-server-changed">
          <Icon name="alert" :size="16" />
          <span><b>The server changed since you chose these variables.</b>{{ ' ' }}{{ detail?.info.serverChanged ? `(${detail.info.serverChanged.old} to ${detail.info.serverChanged.new}). ` : '' }}They are not used for it until you confirm: Save asks first, and nothing is sent in the meantime.</span>
        </div>
        <SsoSignIn v-if="method === 'aws-sso'" v-model:profile="ssoProfile" :connection="isNew ? '' : editName" :saved="!isNew && detail?.signIn === `aws-sso:${ssoProfile}`" />
        <template v-else-if="method === 'keychain'">
          <div v-for="c in readableCreds" :key="c.name" class="field">
            <label class="field-label" :for="`f-c-${c.name}`">{{ c.label }}<span v-if="c.required" aria-hidden="true"> *</span></label>
            <input :id="`f-c-${c.name}`" class="field-input" autocomplete="off" autocapitalize="off" autocorrect="off" spellcheck="false" :value="typed[KEY[c.name]]" :data-testid="`f-c-${c.name}`" @input="typed[KEY[c.name]] = ($event.target as HTMLInputElement).value" />
            <div v-if="c.help" class="help">{{ c.help }}</div>
          </div>
          <CredentialField
            v-for="c in secretCreds"
            :id="`f-c-${c.name}`"
            :key="c.name"
            :label="c.label"
            :is-set="secretIsSet(c.name)"
            :where="status?.sessionOnly ? 'For this session only' : undefined"
            :required="c.required"
            :help="serverChangedHelp || c.help"
            :model-value="typed[KEY[c.name]]"
            @update:model-value="typed[KEY[c.name]] = $event"
            @clear="clearSecret(c.name)"
          />
          <div class="note info"><Icon name="shieldCheck" :size="16" /><span>These go in your computer's keychain. They're never written to the workspace folder, to git, or to logs.</span></div>
        </template>
        <template v-else-if="method === 'env'">
          <div class="field">
            <label class="field-label" for="f-envprefix">Variable prefix</label>
            <input id="f-envprefix" v-model="envPrefix" class="field-input" autocomplete="off" autocapitalize="off" autocorrect="off" spellcheck="false" data-testid="f-envprefix" />
          </div>
          <div v-for="v in envVars" :key="v.name" class="envvar" data-testid="envvar">
            <code>{{ v.name }}</code>
            <span v-if="v.set !== null" class="set" :class="v.set ? 'yes' : 'no'"><Icon :name="v.set ? 'circleCheck' : 'alert'" :size="14" /> {{ v.set ? 'Set' : 'Not set' }}</span>
          </div>
          <p class="help">Slaw shows whether each variable is set, never what it is. Set them in the environment Slaw starts from.</p>
        </template>
        <p v-else class="help">When you connect, Slaw asks for {{ credFields.map((c) => c.label.toLowerCase()).join(' and ') }} and forgets {{ credFields.length > 1 ? 'them' : 'it' }} when you close the workspace. The prompt can offer to remember {{ credFields.length > 1 ? 'them' : 'it' }} in your keychain, off by default.</p>
        <p class="help">The team's file only says which details are needed. How you sign in is your own choice and stays on this computer, so teammates can use a different way.</p>
      </div>

      <!-- Safety -->
      <div v-if="spec?.capabilities.catalog" class="form-sec">
        <h3>Safety</h3>
        <template v-if="isMD">
          <div class="note info" data-testid="md-note"><Icon name="info" :size="16" /><span>MotherDuck connections can't be set read-only here. For read-only access, sign in with a <b>read-scaling token</b>. The Slaw assistant still asks before every change.</span></div>
        </template>
        <template v-else>
          <div class="switch-row">
            <button type="button" role="switch" class="toggle" :class="{ off: !readOnly }" :aria-checked="readOnly" aria-labelledby="ro-t" aria-describedby="ro-d" data-testid="f-readonly" @click="readOnly = !readOnly"></button>
            <span>
              <span id="ro-t" class="st-t">Read-only{{ readOnly ? ' (recommended)' : '' }}</span>
              <span id="ro-d" class="st-d">
                <template v-if="readOnly">Slaw will refuse to run changes on this connection, from you or the Slaw assistant. This is a safety belt inside Slaw, not a lock on the database: a determined query can still get around it.</template>
                <template v-else>Changes are allowed. You and the Slaw assistant can edit data here. It will still ask you before every change.</template>
              </span>
            </span>
          </div>
          <div v-if="readOnly && remote && typeId !== 'ducklake'" class="note warn"><Icon name="alert" :size="16" /><span><b>For production data, also use a read-only database user.</b> Ask your database admin for a role that can only read. That is the only way to be certain nothing can change.</span></div>
        </template>
      </div>

      <!-- Scope -->
      <div class="form-sec">
        <h3>Who can use this connection</h3>
        <div v-if="metadataDriven" class="note warn" data-testid="trust-note"><Icon name="alert" :size="16" /><span><b>Only point this at data you trust.</b> Table metadata in this {{ spec?.label ?? 'lake or table' }} decides which files DuckDB reads. Anyone who can write it can make a query read local files, fetch any web or local-network URL, or read other storage your connections cover.<template v-if="connScope === 'shared'">{{ ' ' }}Saved in the team's slaw.yaml, this warning applies to everyone who opens the workspace.</template><template v-if="insideWorkspace">{{ ' ' }}<b>This location is inside the workspace folder</b>, where files that came with a cloned repository may be crafted tables.</template></span></div>
        <ScopePicker v-model="connScope" :name="f.name" />
      </div>

      <!-- Result and actions -->
      <div v-if="saveError" ref="errorEl" tabindex="-1" class="note attn" role="alert" style="margin: 16px 24px 0;" data-testid="save-error"><Icon name="alert" :size="16" /><span>{{ saveError }}</span></div>
      <div v-if="savedNote" class="note" role="status" style="margin: 16px 24px 0;" data-testid="saved-note"><Icon name="circleCheck" :size="16" /><span>{{ savedNote }}</span></div>
      <div style="padding: 16px 24px 0;">
        <TestResult ref="resultEl" :result="result" :running="testing" :read-only="readOnly && !isMD" @install="installFor" />
      </div>
      <div class="test-bar">
        <button type="button" class="btn" :disabled="testing" data-testid="test-btn" @click="runTest"><Icon name="zap" :size="14" /> {{ testing ? 'Testing…' : 'Test connection' }}</button>
        <span class="help">Tries these settings without saving. Stops after 10 seconds.<template v-if="isMD"> MotherDuck is tested in a separate process.</template> <kbd>{{ shortcutLabel('Enter') }}</kbd></span>
        <button type="button" class="btn" @click="cancel">Cancel</button>
        <button type="submit" :class="failedTest ? 'btn' : 'btn btn-primary'" :disabled="saving" data-testid="save-btn">{{ saving ? 'Saving…' : failedTest ? 'Save anyway' : 'Save' }}</button>
      </div>
    </form>

    <!-- A2: keychain unreachable blocks saving a password -->
    <div v-if="keychainBlocked" class="modal-overlay" role="presentation">
      <div class="dialog" role="alertdialog" aria-modal="true" aria-labelledby="kb-h" data-testid="keychain-dialog">
        <div class="d-head"><h2 id="kb-h">Can't reach your keychain</h2></div>
        <div class="d-body">
          <p>Slaw stores passwords in this computer's keychain, but it isn't answering. That's common over SSH or when no one is signed in to the desktop.</p>
          <p v-if="keychainDetail" class="help">{{ keychainDetail }}</p>
          <div class="note info"><Icon name="info" :size="16" /><span>You can still connect. Set the variables in the environment Slaw starts from, then choose <b>Environment variables</b> for this connection. Slaw never writes passwords to a file instead.</span></div>
        </div>
        <div class="d-foot">
          <button class="btn" type="button" @click="keychainBlocked = false">Try again</button>
          <button class="btn btn-primary" type="button" @click="useEnvInstead">Use environment variables</button>
        </div>
      </div>
    </div>
  </div>
</template>
