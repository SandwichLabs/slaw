<script setup lang="ts">
// ConnectionTypePicker — step 1 of the editor (SPEC 06): a grid generated from
// the type registry. A type the server marks `soon` would be shown greyed with
// a "Coming soon" badge, aria-disabled and not focusable (none is, since #119).
// The grid is a roving group: arrows move, Enter chooses.
import { ref, computed } from 'vue'
import ConnTypeTile from './ConnTypeTile.vue'

const props = defineProps<{ types: ConnTypeSpec[] }>()
const emit = defineEmits<{ choose: [id: string] }>()

const active = computed(() => props.types.filter((t) => !t.soon))
const cur = ref(0)
const grid = ref<HTMLElement | null>(null)

function onKey(e: KeyboardEvent) {
  const n = active.value.length
  const cols = 3
  let i = cur.value
  switch (e.key) {
    case 'ArrowRight': i = (i + 1) % n; break
    case 'ArrowLeft': i = (i + n - 1) % n; break
    case 'ArrowDown': i = Math.min(n - 1, i + cols); break
    case 'ArrowUp': i = Math.max(0, i - cols); break
    case 'Home': i = 0; break
    case 'End': i = n - 1; break
    default: return
  }
  e.preventDefault()
  cur.value = i
  grid.value?.querySelectorAll<HTMLElement>('button[data-type]')[i]?.focus()
}

const idx = (id: string) => active.value.findIndex((t) => t.id === id)
</script>

<template>
  <div>
    <div ref="grid" class="type-grid" role="group" aria-label="Connection type" @keydown="onKey">
      <template v-for="t in types" :key="t.id">
        <div v-if="t.soon" class="typecard card soon" aria-disabled="true" :data-testid="`type-${t.id}`">
          <ConnTypeTile :type="t.id" large />
          <span><span class="tt">{{ t.label }}</span></span>
          <span class="badge">Coming soon</span>
        </div>
        <button
          v-else
          type="button"
          class="typecard card"
          :data-type="t.id"
          :tabindex="idx(t.id) === cur ? 0 : -1"
          :data-testid="`type-${t.id}`"
          @click="emit('choose', t.id)"
          @focus="cur = idx(t.id)"
        >
          <ConnTypeTile :type="t.id" large />
          <span><span class="tt">{{ t.label }}</span><span class="td">{{ t.description }}</span></span>
        </button>
      </template>
    </div>
    <p class="help" style="margin-top: 16px;">Only have a CSV, Parquet or Excel file? No connection needed: put it in the workspace's <code>data/</code> folder or use Import, then query it from there.</p>
  </div>
</template>
