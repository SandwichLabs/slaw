<script setup lang="ts">
// The Import wizard (#82): one card that covers CSV and xlsx. Pick a file,
// review the full-file type inference and suggested primary key, and either
// create a new table or choose how to reconcile with one that already
// exists (update matching rows, replace, add rows, or start a new table).
// Every option change re-runs a debounced dry run so the summary line and
// bad-value list always describe what Import is about to do before it does
// it. The state and actions are useImport.
import Icon from '../components/Icon.vue'
import { useImport } from '../composables/useImport'
import { IMPORT_TYPES, KEY_AUTO, KEY_NONE, typeLabel, summarySentence } from '../lib/importTypes'

const {
  path,
  sheet,
  hasHeader,
  tableName,
  mode,
  key,
  typeOverrides,
  inspection,
  summary,
  successSummary,
  importing,
  error,
  errorContext,
  tableExists,
  keyCandidates,
  showKeySelect,
  MODE_LABELS,
  MODE_DISABLED_REASON,
  MODE_ORDER,
  mismatchText,
  modeNotChosen,
  hasProblems,
  canImport,
  isKeyColumn,
  typeSelectDisabled,
  setColumnType,
  selectFile,
  inspect,
  chooseNewTable,
  runImport,
  openTable,
  importAnother,
} = useImport()
</script>

<template>
  <div style="height: 100%; overflow-y: auto; padding: 32px;">
    <div class="headrow">
      <div>
        <div class="crumb">Studio <span class="sep">/</span> Import</div>
        <h1>Import data</h1>
      </div>
    </div>

    <div class="card card-pad" style="max-width: 60rem;">
      <h2 class="card-title" style="margin: 0 0 var(--sh-space-4);">Import a file</h2>
      <div class="col">
        <!-- Once an import has succeeded, the success banner below is the
             whole story: showing the wizard underneath it would keep
             re-running dry runs against a table that just changed shape
             (and briefly render the previous, now-stale summary). -->
        <template v-if="!successSummary">
        <div>
          <label class="field-label">File (.csv, .tsv, .xlsx)</label>
          <div class="row">
            <span
              v-if="path"
              style="flex: 1; font-weight: 600;"
              :title="path"
              data-testid="file-name"
            >{{ path.split(/[/\\]/).pop() }}</span>
            <input
              v-else
              readonly
              class="field-input"
              style="flex: 1; background: var(--sh-paper-2);"
              placeholder="No file selected"
            />
            <button data-testid="select-file" @click="selectFile" class="btn btn-sm">
              <Icon name="fileText" :size="14" /> Browse
            </button>
          </div>
        </div>

        <div v-if="path" class="row" style="gap: 24px; flex-wrap: wrap;">
          <div v-if="inspection?.sheets?.length">
            <label class="field-label">Sheet</label>
            <select data-testid="sheet-select" v-model="sheet" @change="inspect" class="field-input">
              <option v-for="s in inspection.sheets" :key="s" :value="s">{{ s }}</option>
            </select>
          </div>
          <div>
            <label class="field-label">First row is headers</label>
            <input data-testid="header-toggle" type="checkbox" v-model="hasHeader" @change="inspect" />
          </div>
          <div>
            <label class="field-label">Table name</label>
            <input data-testid="table-name" v-model="tableName" class="field-input" autocomplete="off" autocapitalize="off" autocorrect="off" spellcheck="false" placeholder="my_table" />
          </div>
          <div v-if="inspection" class="sh-meta" style="align-self: flex-end;">{{ inspection.row_count }} {{ inspection.row_count === 1 ? 'row' : 'rows' }}</div>
        </div>

        <div v-if="tableExists && summary" class="col" data-testid="mode-group" style="gap: var(--sh-space-3); padding: var(--sh-space-4); border: 1.5px dashed var(--slaw-border-subtle); border-radius: var(--sh-radius-md);">
          <div class="field-label" style="padding: 0;">
            {{ tableName }} already exists ({{ summary.existing_rows }} row{{ summary.existing_rows === 1 ? '' : 's' }})
          </div>
          <label
            v-for="m in MODE_ORDER"
            :key="m"
            class="row"
            style="align-items: flex-start; gap: 10px; cursor: pointer;"
          >
            <input
              type="radio"
              name="import-mode"
              :value="m"
              v-model="mode"
              :disabled="!summary.modes?.includes(m)"
              :data-testid="`mode-${m}`"
            />
            <span>
              <span style="font-weight: 600; display: block;">{{ MODE_LABELS[m] }}</span>
              <span v-if="!summary.modes?.includes(m)" class="sh-meta">{{ MODE_DISABLED_REASON[m] }}</span>
            </span>
          </label>
          <label class="row" style="align-items: flex-start; gap: 10px; cursor: pointer;">
            <input type="radio" name="import-mode" :checked="false" data-testid="mode-new" @change="chooseNewTable" />
            <span style="font-weight: 600;">Import as a new table</span>
          </label>
        </div>

        <div v-if="path && showKeySelect">
          <label class="field-label">Primary key</label>
          <select data-testid="key-select" v-model="key" class="field-input" style="min-width: 260px;">
            <option v-for="c in keyCandidates" :key="c.name" :value="c.name">
              {{ c.name }}{{ c.name === inspection?.suggested_key ? ' (suggested)' : '' }}
            </option>
            <option :value="KEY_AUTO">Add an auto-number id column</option>
            <option :value="KEY_NONE">No key: rows can't be edited in the datasheet</option>
          </select>
        </div>
        <div v-else-if="path && summary">
          <label class="field-label">Primary key</label>
          <div class="field-input" style="background: var(--sh-paper-2);">
            Key: {{ summary.table_key || 'none' }} (from the table)
          </div>
        </div>

        <!-- The action area (result, problems, Import) sits ABOVE the
             preview grid, not below it: the grid can run to 50 rows, and a
             reviewer caught this exact case — at a modest window height the
             summary/banner/button were only reachable by scrolling past the
             whole grid. The grid gets its own capped, independently
             scrollable box below instead. -->
        <div v-if="modeNotChosen" class="sh-meta" data-testid="mode-prompt">
          Choose what to do with the existing table above.
        </div>

        <div v-else-if="summary && !hasProblems" class="sh-meta" data-testid="summary-line">
          {{ summarySentence(summary) }}
        </div>

        <div v-else-if="hasProblems" class="banner attn" data-testid="problem-banner">
          <div class="ico"><Icon name="alert" :size="22" /></div>
          <div class="body">
            <div class="ttl">
              {{ summary?.blocked ? "Can't import yet"
                : (summary?.bad_values?.length || mismatchText) ? 'Some values need attention'
                : 'Some rows already match exactly' }}
            </div>
            <div class="sub">
              <div v-if="summary">{{ summarySentence(summary) }}</div>
              <div v-if="summary?.blocked">{{ summary.blocked }}</div>
              <div v-if="mismatchText">{{ mismatchText }}</div>
              <table v-if="summary?.bad_values?.length" style="margin-top: 8px; border-collapse: collapse; width: 100%;">
                <thead>
                  <tr>
                    <th style="text-align: left; padding: 4px 8px 4px 0;">Row</th>
                    <th style="text-align: left; padding: 4px 8px;">Column</th>
                    <th style="text-align: left; padding: 4px 8px;">Value</th>
                    <th style="text-align: left; padding: 4px 8px;">Expected</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="(b, i) in summary!.bad_values" :key="i">
                    <td style="padding: 4px 8px 4px 0;">{{ b.row }}</td>
                    <td style="padding: 4px 8px;">{{ b.column }}</td>
                    <td style="padding: 4px 8px; white-space: pre;">{{ b.value }}</td>
                    <td style="padding: 4px 8px;">{{ b.want }}</td>
                  </tr>
                </tbody>
              </table>
              <div v-if="summary && summary.bad_count > (summary.bad_values?.length || 0)">
                and {{ summary.bad_count - (summary.bad_values?.length || 0) }} more
              </div>
            </div>
          </div>
        </div>

        <button
          v-if="path"
          data-testid="import-btn"
          @click="runImport"
          :disabled="!canImport"
          class="btn btn-primary"
          style="justify-content: center;"
        >
          <Icon name="download" :size="16" /> {{ importing ? 'Importing…' : 'Import' }}
        </button>

        <div v-if="error" class="banner attn" data-testid="error-banner">
          <div class="ico"><Icon name="alert" :size="22" /></div>
          <div class="body">
            <div class="ttl">{{ errorContext === 'import' ? 'Import failed' : "Couldn't preview this file" }}</div>
            <div class="sub">{{ error }}</div>
          </div>
        </div>

        <!-- min-width: 0 overrides the flex item default (min-width: auto),
             which would otherwise let the wide table push this whole flex
             column wider instead of scrolling within its own box.
             max-height + overflow-y caps the grid's own height so a 50-row
             preview never pushes the action area above out of view. -->
        <div
          v-if="inspection?.columns?.length"
          data-testid="preview-grid"
          class="card"
          style="overflow: auto; min-width: 0; max-height: 22rem;"
        >
          <table style="border-collapse: collapse; width: 100%;">
            <thead>
              <tr>
                <th
                  v-for="c in inspection.columns"
                  :key="c.name"
                  style="text-align: left; padding: 8px; border-bottom: 1px solid var(--sh-line); vertical-align: top; position: sticky; top: 0; background: var(--sh-surface);"
                  :style="summary?.extra?.includes(c.name) ? { borderColor: 'var(--sh-coral)' } : {}"
                >
                  <div class="row" style="gap: 4px; align-items: center; margin-bottom: 4px; white-space: nowrap;">
                    <Icon v-if="isKeyColumn(c.name)" name="key" :size="12" />
                    <span>{{ c.name }}</span>
                  </div>
                  <select
                    :data-testid="`type-select-${c.name}`"
                    class="field-input"
                    :style="{ minWidth: '160px', fontSize: '12px', opacity: typeSelectDisabled() ? 0.55 : 1, cursor: typeSelectDisabled() ? 'not-allowed' : 'auto' }"
                    :disabled="typeSelectDisabled()"
                    :value="typeOverrides[c.name]?.type"
                    @change="setColumnType(c.name, ($event.target as HTMLSelectElement).value as ImportFriendlyType)"
                  >
                    <option v-for="t in IMPORT_TYPES" :key="t" :value="t">{{ typeLabel({ type: t }) }}</option>
                  </select>
                </th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="(row, ri) in inspection.rows" :key="ri">
                <td
                  v-for="(cell, ci) in row"
                  :key="ci"
                  style="padding: 8px; border-bottom: 1px solid var(--sh-line); white-space: pre;"
                >{{ cell }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        </template>

        <div v-if="successSummary" class="banner teal" data-testid="success-banner">
          <div class="ico"><Icon name="check" :size="22" /></div>
          <div class="body">
            <div class="ttl">Imported into {{ successSummary.table }}</div>
            <div class="sub">{{ summarySentence(successSummary, true) }}</div>
            <div class="row" style="margin-top: 10px; gap: 12px;">
              <button data-testid="open-table-btn" @click="openTable" class="btn btn-sm btn-primary">
                Open table <Icon name="arrowRight" :size="14" />
              </button>
              <button data-testid="import-another-btn" @click="importAnother" class="btn btn-sm">
                Import another file
              </button>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>
