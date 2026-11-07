<script setup lang="ts">
// ScopePicker — who can use a connection (SPEC 06): This computer (default),
// Team (committed to git) or All my workspaces. Choosing Team explains what is
// committed and what is not.
import { computed } from 'vue'
import Icon from './Icon.vue'
import { workspaceInfo } from '../composables/useWorkspace'

const props = defineProps<{ modelValue: ConnScope; name?: string; disabled?: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [v: ConnScope] }>()

const isGit = computed(() => !!workspaceInfo.value?.isGit)
const allOptions: { value: ConnScope; title: string; desc: string; file: string }[] = [
  { value: 'local', title: 'This computer', desc: 'Only you, on this computer, for this workspace. Never committed.', file: 'outside the folder' },
  { value: 'shared', title: 'Team', desc: 'Everyone on this workspace. Saved in slaw.yaml and shared through git.', file: 'slaw.yaml' },
  { value: 'user', title: 'All my workspaces', desc: 'Only you, in every workspace on this computer.', file: '~/.config/slaw/' },
]

function onKey(e: KeyboardEvent) {
  const i = allOptions.findIndex((o) => o.value === props.modelValue)
  let n = i
  if (e.key === 'ArrowRight' || e.key === 'ArrowDown') n = (i + 1) % allOptions.length
  else if (e.key === 'ArrowLeft' || e.key === 'ArrowUp') n = (i + allOptions.length - 1) % allOptions.length
  else return
  e.preventDefault()
  emit('update:modelValue', allOptions[n].value)
  ;(e.currentTarget as HTMLElement).querySelectorAll<HTMLElement>('[role=radio]')[n]?.focus()
}
</script>

<template>
  <div>
    <div class="opt-row" role="radiogroup" aria-label="Who can use this connection" @keydown="onKey">
      <button
        v-for="o in allOptions"
        :key="o.value"
        type="button"
        role="radio"
        class="opt-card"
        :class="{ on: modelValue === o.value }"
        :aria-checked="modelValue === o.value"
        :tabindex="modelValue === o.value ? 0 : -1"
        :disabled="disabled"
        :data-testid="`scope-${o.value}`"
        @click="emit('update:modelValue', o.value)"
      >
        <span>
          <span class="ot">{{ o.title }}</span>
          <span class="od">{{ o.desc }}</span>
          <span v-if="o.value === 'shared' && !isGit" class="od">(Share it by sharing the folder.)</span>
        </span>
      </button>
    </div>
    <div v-if="modelValue === 'shared'" class="note warn" style="margin-top: 12px;" data-testid="scope-team-note">
      <Icon name="gitBranch" :size="16" />
      <span><b>This file is committed to git.</b> The name, host, port and database are saved in <code>slaw.yaml</code>, so anyone who can see the repository can see them. Passwords and tokens are not saved there: they stay in each person's own keychain, and teammates enter their own. Your username is saved on this computer only.</span>
    </div>
    <div v-else-if="modelValue === 'local'" class="note" style="margin-top: 12px;">
      <Icon name="lock" :size="16" />
      <span>Saved on this computer only. It isn't committed and teammates won't see it.</span>
    </div>
    <div v-else class="note" style="margin-top: 12px;">
      <Icon name="user" :size="16" />
      <span>Saved for you in every workspace on this computer, in <code>~/.config/slaw/connections.yaml</code>. Not committed.</span>
    </div>
  </div>
</template>
