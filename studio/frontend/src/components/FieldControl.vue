<script setup lang="ts">
// FieldControl — the input of one generated connection field, by its kind: a
// select for an enum, a picker over the workspace's connections of a type for
// a 'connection' field (an s3_tables catalog's storage connection), a plain
// text input otherwise. The label and help text stay with the caller.
import { computed } from 'vue'
import { connectionList } from '../composables/useConnections'
import { connectionChoices } from '../lib/connections'

const props = defineProps<{ field: ConnField; modelValue: string; invalid?: boolean }>()
const emit = defineEmits<{ (e: 'update:modelValue', v: string): void }>()

const id = computed(() => `f-${props.field.name}`)
const label = (c: string) => props.field.choiceLabels?.[c] ?? c
// A blank value is the field's default, so that choice is not listed twice.
const choiceList = computed(() => (props.field.choices ?? []).filter((c) => c !== props.field.default))
const choices = computed(() => connectionChoices(connectionList.value, props.field.connectionType))
// A value that names no connection of the type is still shown, marked, so it is not silently dropped.
const stale = computed(() => !!props.modelValue && !choices.value.includes(props.modelValue))
const none = computed(() => (props.field.connectionType ? `No ${props.field.connectionType} connection yet` : 'No connection yet'))
function onInput(e: Event) { emit('update:modelValue', (e.target as HTMLInputElement | HTMLSelectElement).value) }
</script>

<template>
  <select v-if="field.kind === 'enum'" :id="id" class="field-input" :value="modelValue" :aria-invalid="invalid || undefined" :data-testid="id" @change="onInput">
    <option value="">{{ field.default ? `Default (${label(field.default)})` : field.required ? 'Choose…' : 'Default' }}</option>
    <option v-for="c in choiceList" :key="c" :value="c">{{ label(c) }}</option>
  </select>
  <select v-else-if="field.kind === 'connection'" :id="id" class="field-input" :value="modelValue" :aria-invalid="invalid || undefined" :data-testid="id" @change="onInput">
    <option value="">{{ choices.length || stale ? 'Choose…' : none }}</option>
    <option v-for="c in choices" :key="c" :value="c">{{ c }}</option>
    <option v-if="stale" :value="modelValue">{{ modelValue }} (not found)</option>
  </select>
  <input v-else :id="id" class="field-input" :value="modelValue" autocomplete="off" autocapitalize="off" autocorrect="off" spellcheck="false" :aria-invalid="invalid || undefined" :data-testid="id" @input="onInput" />
</template>
