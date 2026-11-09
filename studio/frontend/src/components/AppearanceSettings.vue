<script setup lang="ts">
import { App, hasBinding } from '../lib/bindings'
/**
 * Settings → Appearance. App-wide (stored in the user's config dir by
 * internal/prefs), so it sits above the per-file settings and says so.
 */
import { computed, ref } from 'vue'
import Icon from './Icon.vue'
import ThemeCard from './ThemeCard.vue'
import { useTheme, THEME_SYSTEM } from '../theme/useTheme'
import { errorText } from '../lib/errors'

const theme = useTheme()
const followSystem = computed(() => theme.prefs.theme === THEME_SYSTEM)
const lights = computed(() => theme.listings.value.filter((l) => l.type === 'light' && l.resolved))
const darks = computed(() => theme.listings.value.filter((l) => l.type === 'dark' && l.resolved))
const themesDir = ref('')
const reloading = ref(false)
const opening = ref(false)
const notice = ref('')

function flash(msg: string) {
  notice.value = msg
  setTimeout(() => { if (notice.value === msg) notice.value = '' }, 2500)
}

async function pick(id: string) {
  await theme.setTheme(id)
}

async function setFollow(on: boolean) {
  if (on) await theme.setTheme(THEME_SYSTEM)
  else await theme.setTheme(theme.effectiveId.value)
}

async function reload() {
  reloading.value = true
  try {
    await theme.reloadUserThemes()
    const n = theme.listings.value.filter((l) => l.source === 'user').length
    flash(n === 1 ? '1 custom theme loaded' : `${n} custom themes loaded`)
  } finally {
    reloading.value = false
  }
}

async function openFolder() {
  opening.value = true
  try {
    themesDir.value = await App.OpenThemesDir()
  } catch (e: any) {
    flash("Couldn't open the themes folder: " + errorText(e))
  } finally {
    opening.value = false
  }
}

const hasWails = hasBinding('OpenThemesDir')
</script>

<template>
  <section class="appearance" data-testid="appearance-settings">
    <div class="sec-title">
      <h2 style="font-size: 22px;">Appearance</h2>
      <span class="label">Every workspace · this computer</span>
    </div>

    <div class="card card-pad col" style="gap: var(--sh-space-5);">
      <!-- Follow system -->
      <div class="row" style="justify-content: space-between; gap: var(--sh-space-4); flex-wrap: wrap;">
        <div style="min-width: 0; flex: 1 1 260px;">
          <div style="font-weight: 600;">Follow system appearance</div>
          <div class="sh-meta">Switches between your light and dark picks when the OS does. Currently {{ theme.systemDark.value ? 'dark' : 'light' }}.</div>
        </div>
        <div class="seg" role="group" aria-label="Theme mode">
          <button type="button" :class="{ on: !followSystem }" :aria-pressed="!followSystem" data-testid="theme-mode-manual" @click="setFollow(false)">
            <Icon name="palette" :size="14" /> Pick one
          </button>
          <button type="button" :class="{ on: followSystem }" :aria-pressed="followSystem" data-testid="theme-mode-system" @click="setFollow(true)">
            <Icon name="monitor" :size="14" /> System
          </button>
        </div>
      </div>

      <!-- Manual pick: every theme in one grid -->
      <div v-if="!followSystem">
        <div class="field-label">Theme</div>
        <div class="theme-grid">
          <ThemeCard
            v-for="l in theme.listings.value"
            :key="l.id"
            :listing="l"
            :selected="theme.prefs.theme === l.id"
            @select="pick"
          />
        </div>
      </div>

      <!-- System pairing: two grids -->
      <div v-else class="col" style="gap: var(--sh-space-4);">
        <div>
          <div class="field-label"><Icon name="sun" :size="12" /> When the system is light</div>
          <div class="theme-grid">
            <ThemeCard
              v-for="l in lights"
              :key="l.id"
              :listing="l"
              :selected="theme.prefs.systemLight === l.id"
              :active-via="!theme.systemDark.value && theme.prefs.systemLight === l.id ? 'system' : null"
              @select="(id) => theme.setSystemPair(id, theme.prefs.systemDark)"
            />
          </div>
        </div>
        <div>
          <div class="field-label"><Icon name="moon" :size="12" /> When the system is dark</div>
          <div class="theme-grid">
            <ThemeCard
              v-for="l in darks"
              :key="l.id"
              :listing="l"
              :selected="theme.prefs.systemDark === l.id"
              :active-via="theme.systemDark.value && theme.prefs.systemDark === l.id ? 'system' : null"
              @select="(id) => theme.setSystemPair(theme.prefs.systemLight, id)"
            />
          </div>
        </div>
      </div>

      <!-- Custom themes -->
      <div class="custom-row">
        <div>
          <div style="font-weight: 600;">Custom themes</div>
          <div class="sh-meta">
            Drop a <code>.json</code> theme in the themes folder and reload. Start from any built-in with <code>"extends"</code>.
            <span v-if="themesDir" class="sh-mono" style="display: block; margin-top: 4px; word-break: break-all;">{{ themesDir }}</span>
          </div>
        </div>
        <div class="row" style="gap: var(--sh-space-2); flex: none;">
          <button type="button" class="btn btn-sm" :disabled="reloading" data-testid="reload-themes" @click="reload">
            <Icon name="refresh" :size="14" /> {{ reloading ? 'Reloading…' : 'Reload themes' }}
          </button>
          <button v-if="hasWails" type="button" class="btn btn-sm" :disabled="opening" data-testid="open-themes-dir" @click="openFolder">
            <Icon name="folder" :size="14" /> Open folder
          </button>
        </div>
      </div>
      <ul v-if="theme.problems.value.length" class="problems" role="alert" data-testid="theme-problems">
        <li v-for="p in theme.problems.value" :key="p">{{ p }}</li>
      </ul>

      <!-- Motion + scale -->
      <div class="row" style="justify-content: space-between; gap: var(--sh-space-4); flex-wrap: wrap;">
        <label class="row" style="gap: 10px; cursor: pointer;">
          <input type="checkbox" :checked="theme.prefs.reduceMotion" data-testid="reduce-motion" @change="theme.setReduceMotion(($event.target as HTMLInputElement).checked)" />
          <span>
            <span style="font-weight: 600; display: block;">Reduce motion</span>
            <span class="sh-meta">No hover lifts, slide-ins or pulses.</span>
          </span>
        </label>
        <label class="row" style="gap: 10px;">
          <span style="font-weight: 600;">UI scale</span>
          <select class="field-input" style="width: auto; min-height: 36px; padding: 6px 10px;" :value="theme.prefs.uiScale" data-testid="ui-scale" @change="theme.setUiScale(Number(($event.target as HTMLSelectElement).value))">
            <option :value="90">90%</option>
            <option :value="100">100%</option>
            <option :value="110">110%</option>
            <option :value="125">125%</option>
          </select>
        </label>
      </div>

      <p v-if="notice || theme.notice.value" class="sh-meta sh-mono" role="status" style="margin: 0;">{{ notice || theme.notice.value }}</p>
    </div>
  </section>
</template>

<style scoped>
.theme-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
  gap: 12px;
}
.custom-row {
  display: flex; align-items: flex-start; justify-content: space-between; gap: var(--sh-space-4);
  padding-top: var(--sh-space-4);
  border-top: 1.5px dashed var(--slaw-border-subtle);
  flex-wrap: wrap;
}
.problems {
  margin: 0; padding: 10px 14px 10px 28px;
  background: var(--sh-error-8);
  border: 1.5px solid var(--sh-error);
  border-radius: var(--sh-radius-sm);
  font-family: var(--sh-font-mono); font-size: 12px; line-height: 1.5;
  color: var(--sh-ink);
}
.field-label { display: flex; align-items: center; gap: 6px; }
</style>
