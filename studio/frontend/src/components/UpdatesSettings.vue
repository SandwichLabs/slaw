<script setup lang="ts">
// Settings > About > Updates: the weekly-check toggle, "Check now", the result (up to date, a newer
// release with its notes, an external install, an error) and the confirmed install + restart.
import { onMounted } from 'vue'
import Icon from './Icon.vue'
import {
  updateState, updateResult, updateChecking, updateError, updateSettingsError, updateApplying, updateApplyError, appliedInfo,
  refreshUpdateState, dismissNotice, setUpdateCheck, checkUpdateNow, skipUpdate, installUpdate, restartAfterUpdate,
} from '../composables/useUpdates'
import { BrowserOpenURL } from '../wailsjs/runtime/runtime'

// Opening About is where the weekly notice points, so it has done its job.
onMounted(() => {
  dismissNotice()
  void refreshUpdateState()
})

const when = (iso: string) => { const d = new Date(iso); return Number.isNaN(d.getTime()) ? '' : d.toLocaleString() }
const openRelease = (url: string) => { if (url.startsWith('https://')) BrowserOpenURL(url) }
</script>

<template>
  <div class="card card-pad col" style="gap: var(--sh-space-3);" data-testid="updates-block">
    <div style="font-weight: 600;">Updates</div>
    <p class="sh-meta" style="margin: 0;">
      Slaw never updates itself in the background. If you allow it, Studio makes one plain request to github.com when the last check is over a week old. Nothing is installed until you confirm.
    </p>

    <label class="row" style="gap: var(--sh-space-2); align-items: center;">
      <input type="checkbox" :checked="updateState?.check === 'weekly'" data-testid="updates-weekly" @change="setUpdateCheck(($event.target as HTMLInputElement).checked)" />
      <span>Check GitHub for new releases weekly</span>
    </label>

    <div class="row" style="gap: var(--sh-space-3); flex-wrap: wrap; align-items: center;">
      <button type="button" class="btn btn-sm" :disabled="updateChecking" data-testid="updates-check" @click="checkUpdateNow">
        <Icon name="refresh" :size="14" /> {{ updateChecking ? 'Checking…' : 'Check now' }}
      </button>
      <span v-if="updateState?.lastChecked" class="sh-meta" data-testid="updates-last">Last checked {{ when(updateState.lastChecked) }}</span>
      <span v-else class="sh-meta">Not checked yet.</span>
    </div>

    <p v-if="updateError" class="sh-meta" role="alert" style="color: var(--sh-error);" data-testid="updates-error">Could not check: {{ updateError }}</p>

    <p v-if="updateSettingsError" class="sh-meta" role="alert" style="color: var(--sh-error);" data-testid="updates-settings-error">Could not save that: {{ updateSettingsError }}</p>

    <div v-if="appliedInfo" class="banner" role="status" data-testid="updates-applied">
      <div class="body">
        <div class="ttl">Updated to v{{ appliedInfo.to }}</div>
        <div class="sub">This window is still running the old version until you restart.</div>
        <details v-if="appliedInfo.output" style="margin-top: 6px;"><summary>Installer output</summary><pre class="notes">{{ appliedInfo.output }}</pre></details>
      </div>
      <div class="row" style="gap: var(--sh-space-2);">
        <button v-if="appliedInfo.canRestart" type="button" class="btn btn-primary btn-sm" data-testid="updates-restart" @click="restartAfterUpdate">Restart now</button>
      </div>
    </div>

    <template v-else-if="updateResult">
      <p v-if="!updateResult.updateAvailable" class="sh-meta row" style="margin: 0; gap: var(--sh-space-2); align-items: center;" data-testid="updates-uptodate">
        <Icon name="circleCheck" :size="14" /> {{ updateResult.current === 'dev' ? `Development build. The latest release is ${updateResult.latest}.` : `Slaw ${updateResult.current} is up to date.` }}
      </p>
      <div v-else class="col" style="gap: var(--sh-space-2);" data-testid="updates-available">
        <div style="font-weight: 600;">{{ updateResult.latest }} is available <span class="sh-meta">(you have {{ updateResult.current }})</span></div>
        <p v-if="updateResult.skipped" class="sh-meta" style="margin: 0;" data-testid="updates-skipped">You skipped this version, so weekly checks don't mention it.</p>
        <pre v-if="updateResult.notes" class="notes" data-testid="updates-notes">{{ updateResult.notes }}</pre>
        <p v-if="!updateResult.updatable" class="sh-meta" style="margin: 0;" data-testid="updates-external">{{ updateResult.managedNote }}</p>
        <p v-if="updateApplyError" class="sh-meta" role="alert" style="color: var(--sh-error); margin: 0;" data-testid="updates-apply-error">Could not install: {{ updateApplyError }}</p>
        <div class="row" style="gap: var(--sh-space-2); flex-wrap: wrap;">
          <button v-if="updateResult.updatable" type="button" class="btn btn-primary btn-sm" :disabled="updateApplying" data-testid="updates-install" @click="installUpdate(updateResult.latest)">
            <Icon name="download" :size="14" /> {{ updateApplying ? 'Installing…' : `Update to ${updateResult.latest}` }}
          </button>
          <button type="button" class="btn btn-sm" data-testid="updates-release" @click="openRelease(updateResult.releaseUrl)">
            <Icon name="externalLink" :size="14" /> Release page
          </button>
          <button v-if="!updateResult.skipped" type="button" class="btn btn-sm" data-testid="updates-skip" @click="skipUpdate(updateResult.latest)">Skip this version</button>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.notes { margin: 0; padding: var(--sh-space-2) var(--sh-space-3); max-height: 14rem; overflow: auto; white-space: pre-wrap; overflow-wrap: anywhere; font-family: var(--sh-font-mono); font-size: 12.5px; background: var(--sh-paper-2); border: 1.5px solid var(--slaw-border-subtle); border-radius: var(--sh-radius-sm); }
</style>
