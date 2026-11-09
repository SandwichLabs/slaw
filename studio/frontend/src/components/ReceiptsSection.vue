<script setup lang="ts">
// ReceiptsSection — Settings > Workspace > Receipts: how long this computer keeps
// run receipts, whether they keep parameter values, what is kept now, and Delete
// all. Everything here is this person's own and never goes into slaw.yaml, git
// or a bundle. Text interpolation only.
import { onMounted, onUnmounted } from 'vue'
import Icon from './Icon.vue'
import { useReceiptSettings, usageLine } from '../composables/useReceiptSettings'
import { hasWailsRuntime } from '../lib/wails'
import { EventsOn } from '../wailsjs/runtime/runtime'

const { info, keepParams, days, maxCount, maxMB, busy, message, error, dirty, refresh, save, clearAll } = useReceiptSettings()

let off: (() => void) | null = null
onMounted(() => {
  void refresh()
  if (hasWailsRuntime()) off = EventsOn('workspace:changed', () => { void refresh() })
})
onUnmounted(() => off?.())
</script>

<template>
  <div data-testid="receipt-settings">
    <div class="sec-title"><h2 style="font-size: 22px;">Receipts</h2><span class="scope local">This computer</span></div>
    <div class="card card-pad col" style="gap: var(--sh-space-4);">
      <p class="help" style="margin: 0;">
        A receipt records that a question was asked and what it touched. Receipts stay in <code>.slaw-local/receipts/</code> on this computer, hold no result values, and never go into git or a bundle.
      </p>

      <template v-if="info && !info.available">
        <p class="help" role="status" style="margin: 0;" data-testid="receipt-unavailable">Receipts can't be kept for this workspace: its <code>.slaw-local/receipts</code> folder can't be used (a link or a file is in its place).</p>
      </template>
      <template v-else-if="info">
        <label class="row" style="gap: 10px; cursor: pointer; align-items: flex-start;">
          <input v-model="keepParams" type="checkbox" data-testid="receipt-keep-params" />
          <span>
            <span style="font-weight: 600; display: block;">Keep parameter values</span>
            <span class="sh-meta">A receipt keeps the values a question was run with (up to 256 bytes each) so the run can be repeated. Off keeps only their names and a fingerprint.</span>
          </span>
        </label>

        <div class="row" style="gap: var(--sh-space-4); flex-wrap: wrap; align-items: flex-end;">
          <div>
            <label class="field-label" for="receipt-days">Keep for</label>
            <div class="row" style="gap: var(--sh-space-2);">
              <input id="receipt-days" v-model="days" class="field-input" type="number" inputmode="numeric" min="1" style="width: 7rem;" aria-describedby="receipt-limits-h" data-testid="receipt-days" />
              <span class="sh-meta">days</span>
            </div>
          </div>
          <div>
            <label class="field-label" for="receipt-count">At most</label>
            <div class="row" style="gap: var(--sh-space-2);">
              <input id="receipt-count" v-model="maxCount" class="field-input" type="number" inputmode="numeric" min="1" style="width: 8rem;" aria-describedby="receipt-limits-h" data-testid="receipt-count" />
              <span class="sh-meta">receipts</span>
            </div>
          </div>
          <div>
            <label class="field-label" for="receipt-mb">Using at most</label>
            <div class="row" style="gap: var(--sh-space-2);">
              <input id="receipt-mb" v-model="maxMB" class="field-input" type="number" inputmode="numeric" min="1" style="width: 7rem;" aria-describedby="receipt-limits-h" data-testid="receipt-mb" />
              <span class="sh-meta">MB</span>
            </div>
          </div>
        </div>
        <div id="receipt-limits-h" class="help" data-testid="receipt-limits-help">
          Defaults: {{ info.defaults.days }} days, {{ info.defaults.maxCount.toLocaleString() }} receipts, {{ info.defaults.maxMB }} MB. Allowed: {{ info.floors.days }} to {{ info.ceilings.days }} days, {{ info.floors.maxCount.toLocaleString() }} to {{ info.ceilings.maxCount.toLocaleString() }} receipts, {{ info.floors.maxMB }} to {{ info.ceilings.maxMB.toLocaleString() }} MB. A number outside its range is moved to the nearest one when you save.
        </div>

        <div class="sh-body" role="status" style="font-weight: 600;" data-testid="receipt-usage">{{ usageLine(info.usage) }}</div>

        <div class="row" style="gap: var(--sh-space-2); flex-wrap: wrap;">
          <button class="btn btn-primary" type="button" :disabled="busy || !dirty" data-testid="receipt-save" @click="save">
            <Icon name="save" :size="16" /> {{ busy ? 'Saving…' : 'Save' }}
          </button>
          <button class="btn btn-destructive" type="button" :disabled="busy || info.usage.count === 0" data-testid="receipt-clear" @click="clearAll">Delete all receipts…</button>
          <span v-if="message" class="sh-body" role="status" style="font-weight: 600; color: var(--sh-teal-lo);" data-testid="receipt-message">{{ message }}</span>
        </div>

        <div class="note info">
          <Icon name="info" :size="16" />
          <span>Changes apply to new receipts. A lower limit removes the oldest receipts when you save. The console keeps none unless you type <code>.receipts on</code>. The same settings are available from a terminal with <code>slaw receipts settings</code>.</span>
        </div>
      </template>
      <div v-if="error" class="help" role="alert" style="color: var(--sh-error);" data-testid="receipt-error">{{ error }}</div>
    </div>
  </div>
</template>
