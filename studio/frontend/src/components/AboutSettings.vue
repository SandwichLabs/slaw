<script setup lang="ts">
// Settings > About (#40): name, stamped version, licence, third-party notices, privacy, bug reports.
import { onMounted, ref } from 'vue'
import Icon from './Icon.vue'
import NoticesDialog from './NoticesDialog.vue'
import UpdatesSettings from './UpdatesSettings.vue'
import { useAbout } from '../composables/useAbout'

const about = useAbout()
const noticesOpen = ref(false)
onMounted(about.load)
</script>

<template>
  <section id="settings-panel-about" role="tabpanel" aria-labelledby="settings-tab-about" class="col" style="max-width: 52rem; gap: var(--sh-space-6);" data-testid="about-settings">
    <div class="sec-title">
      <h2 style="font-size: 22px;">About Slaw</h2>
      <span class="label">This build</span>
    </div>

    <div class="card card-pad col" style="gap: var(--sh-space-4);">
      <div>
        <div style="font-weight: 600;">Slaw, the SandwichLabs analysis workspace</div>
        <div class="sh-meta">Analyze your data in a secure private workspace.</div>
      </div>
      <dl class="build" data-testid="about-build">
        <dt>Version</dt><dd data-testid="about-version">{{ about.info.value.version }}</dd>
        <template v-if="about.info.value.commit"><dt>Commit</dt><dd data-testid="about-commit">{{ about.info.value.commit }}</dd></template>
        <template v-if="about.info.value.date"><dt>Built</dt><dd>{{ about.info.value.date }}</dd></template>
        <template v-if="about.info.value.platform"><dt>Platform</dt><dd>{{ about.info.value.platform }}</dd></template>
        <template v-if="about.info.value.goVersion"><dt>Go</dt><dd>{{ about.info.value.goVersion }}</dd></template>
        <template v-if="about.info.value.duckdbVersion"><dt>DuckDB</dt><dd>{{ about.info.value.duckdbVersion }}</dd></template>
      </dl>
      <div class="row" style="gap: var(--sh-space-2); flex-wrap: wrap;">
        <button type="button" class="btn btn-sm" data-testid="about-copy" @click="about.copyBuildInfo">
          <Icon :name="about.copied.value ? 'check' : 'copy'" :size="14" /> {{ about.copied.value ? 'Copied' : 'Copy build info' }}
        </button>
        <button type="button" class="btn btn-sm" data-testid="about-report" @click="about.reportBug">
          <Icon name="externalLink" :size="14" /> Report a bug
        </button>
      </div>
      <p v-if="about.error.value" class="sh-meta" role="alert" data-testid="about-error">{{ about.error.value }}</p>
    </div>

    <UpdatesSettings />

    <div class="card card-pad col" style="gap: var(--sh-space-3);">
      <div style="font-weight: 600;">Licence and privacy</div>
      <p class="sh-meta" style="margin: 0;">
        Slaw is open source under the Apache License 2.0. It sends no telemetry: it only talks to the services you connect it to, plus an opt-in check for new releases on github.com.
      </p>
      <div class="row" style="gap: var(--sh-space-2); flex-wrap: wrap;">
        <button type="button" class="btn btn-sm" data-testid="about-license" @click="about.openLicense">
          <Icon name="externalLink" :size="14" /> Apache-2.0 licence
        </button>
        <button type="button" class="btn btn-sm" data-testid="about-privacy" @click="about.openPrivacy">
          <Icon name="externalLink" :size="14" /> Privacy
        </button>
        <button type="button" class="btn btn-sm" data-testid="about-notices" @click="noticesOpen = true">
          <Icon name="fileText" :size="14" /> Third-party notices
        </button>
      </div>
    </div>
    <NoticesDialog :open="noticesOpen" @close="noticesOpen = false" />
  </section>
</template>

<style scoped>
.build { display: grid; grid-template-columns: max-content 1fr; gap: 4px var(--sh-space-4); margin: 0; font-size: 14px; }
.build dt { color: var(--sh-graphite); }
.build dd { margin: 0; font-family: var(--sh-font-mono); font-size: 13px; overflow-wrap: anywhere; }
</style>
