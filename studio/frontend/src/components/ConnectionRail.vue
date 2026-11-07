<script setup lang="ts">
// ConnectionRail — the DataGrip/DBeaver-style explorer under the nav (SPEC 05):
// connection > schema > table or view > column, loaded lazily. Opening a
// workspace dials nothing; expanding a connection connects it. One tab stop with
// a roving tabindex (WAI-ARIA tree), a context menu, status shapes, and under
// 1000px a drawer behind the plug button.
//
// The tree is rendered as one flat list of treeitems (aria-level / posinset),
// built from the lazily loaded levels (lib/railTree), which keeps keyboard
// movement simple. The state and behaviour are the useConnectionTree,
// useRailActions, useRailMenu, useRailKeys and useRailDrawer composables.
import { ref, computed } from 'vue'
import Icon from './Icon.vue'
import StatusIcon from './StatusIcon.vue'
import ScopeBadge from './ScopeBadge.vue'
import ConnTypeTile from './ConnTypeTile.vue'
import { connectionList, connecting } from '../composables/useConnections'
import { useConnectionTree } from '../composables/useConnectionTree'
import { useRailDrawer } from '../composables/useRailDrawer'
import { useRailActions } from '../composables/useRailActions'
import { useRailMenu } from '../composables/useRailMenu'
import { useRailKeys } from '../composables/useRailKeys'
import { statusKey, scopeLabel, typeLabel } from '../lib/connections'
import { colMeta, domId, rowsText, type Row } from '../lib/railTree'

const emit = defineEmits<{ error: [message: string] }>()

const tree = useConnectionTree()
const { expanded, filter, treeEl, rows, tabKey } = tree
const filterEl = ref<HTMLInputElement | null>(null)

// The drawer's Escape defers to an open context menu (declared below).
const { open, closeDrawer, toggleDrawer } = useRailDrawer({ treeEl, filterEl, menuOpen: () => !!menu.value })
const actions = useRailActions(tree, { closeDrawer, error: (m) => emit('error', m) })
const { refreshConn } = actions
const { menu, menuEl, menuIndex, openMenu, runItem, onMenuKey } = useRailMenu(tree, actions)
const { onTreeKey, onRowClick } = useRailKeys(tree, actions, { openMenu, filterEl })
const { onDetailAction } = actions

const onlyLocal = computed(() => connectionList.value.length === 1 && !connectionList.value[0].remote)
const rowDescribedBy = (r: Row) => (r.detail ? `${domId(r.key)}-d` : undefined)
defineExpose({ rows, toggleDrawer, refreshConn, expanded })
</script>

<template>
  <section class="conn-rail" :class="{ open }" aria-label="Connections" data-testid="conn-rail">
    <div class="rail-head">
      <button
        class="icon-btn rail-toggle"
        type="button"
        :aria-expanded="open"
        aria-controls="conn-tree-wrap"
        aria-label="Connections"
        title="Connections"
        data-testid="rail-toggle"
        @click="toggleDrawer"
      ><Icon name="plug" :size="16" /></button>
      <span class="side-section rail-title">Connections</span>
      <router-link class="icon-btn keep" to="/connections/new" aria-label="New connection" title="New connection" data-testid="rail-add" @click="closeDrawer"><Icon name="plus" :size="16" /></router-link>
      <button class="icon-btn keep" type="button" aria-label="Refresh all connections" title="Refresh all (F5)" data-testid="rail-refresh" @click="refreshConn()"><Icon name="refresh" :size="16" /></button>
    </div>

    <div id="conn-tree-wrap" class="tree-wrap">
      <!-- The drawer (narrow windows) carries its own header: title, new, refresh. -->
      <div class="rail-head drawer-head">
        <span class="side-section rail-title">Connections</span>
        <router-link class="icon-btn" to="/connections/new" aria-label="New connection" title="New connection" data-testid="drawer-add" @click="closeDrawer"><Icon name="plus" :size="16" /></router-link>
        <button class="icon-btn" type="button" aria-label="Refresh all connections" title="Refresh all (F5)" data-testid="drawer-refresh" @click="refreshConn()"><Icon name="refresh" :size="16" /></button>
      </div>
      <div class="rail-filter">
        <label class="sr" for="rail-filter-input">Filter tables and columns</label>
        <Icon name="search" :size="14" />
        <input id="rail-filter-input" ref="filterEl" v-model="filter" type="text" placeholder="Filter tables and columns" autocomplete="off" autocapitalize="off" autocorrect="off" spellcheck="false" data-testid="rail-filter" />
      </div>

      <div ref="treeEl" class="tree" role="tree" aria-label="Connections" data-testid="rail-tree">
        <div
          v-for="r in rows"
          :key="r.key"
          :data-key="r.key"
          :role="r.kind === 'note' ? 'none' : 'treeitem'"
          :aria-level="r.kind === 'note' ? undefined : r.level"
          :aria-setsize="r.kind === 'note' ? undefined : r.setsize"
          :aria-posinset="r.kind === 'note' ? undefined : r.pos"
          :aria-expanded="r.kind !== 'note' && r.expandable ? r.isOpen : undefined"
          :aria-selected="r.kind !== 'note' ? tabKey === r.key : undefined"
          :aria-busy="r.kind === 'conn' && connecting.has(r.conn.name) ? 'true' : undefined"
          :aria-describedby="r.kind === 'conn' ? rowDescribedBy(r) : undefined"
          :aria-label="r.kind === 'conn' ? `${r.label}, ${typeLabel(r.conn.type)}, ${scopeLabel(r.conn.scope)}, ${r.conn.status === 'ok' ? 'connected' : r.conn.status === 'not-connected' ? 'not connected' : r.conn.status}` : undefined"
          :tabindex="r.kind === 'note' ? undefined : tabKey === r.key ? 0 : -1"
          :data-testid="r.kind === 'conn' ? 'conn-row' : r.kind === 'note' ? 'rail-note' : 'tree-row'"
          :data-kind="r.kind"
          :data-name="r.label"
          @keydown="r.kind !== 'note' && onTreeKey($event, r)"
          @contextmenu.prevent="r.kind !== 'note' && openMenu(r, { x: $event.clientX, y: $event.clientY })"
        >
          <!-- A note row (loading, empty, error) is plain text under its parent. -->
          <div v-if="r.kind === 'note'" class="tn-note" :style="{ paddingLeft: 14 + r.level * 14 + 'px' }">{{ r.noteText }}</div>

          <template v-else>
            <div
              class="tn"
              :class="{ conn: r.kind === 'conn', sel: tabKey === r.key && false }"
              :style="{ paddingLeft: 4 + (r.level - 1) * 14 + 'px' }"
              :title="r.kind === 'conn' ? `${r.label} · ${typeLabel(r.conn.type)} · ${statusKey(r.conn.status)}` : undefined"
              @click="onRowClick($event, r)"
            >
              <Icon v-if="r.expandable" :name="r.isOpen ? 'chevronDown' : 'chevronRight'" :size="14" class="chev" />
              <Icon v-else name="chevronRight" :size="14" class="chev none" />

              <template v-if="r.kind === 'conn'">
                <ConnTypeTile :type="r.conn.type" />
                <span class="nm">{{ r.label }}</span>
                <Icon v-if="r.conn.readOnly && r.conn.type !== 'motherduck'" name="lock" :size="12" class="ro" aria-label="Read-only" title="Read-only" />
                <Icon v-if="r.conn.shadows?.length" name="layers" :size="12" class="ro" aria-label="Overrides another copy" title="This computer's copy overrides a Team or All-my-workspaces one of the same name" />
                <span class="rowtail">
                  <ScopeBadge :scope="r.conn.scope" />
                  <span v-if="connecting.has(r.conn.name)" class="spin" role="img" aria-label="Connecting"></span>
                  <StatusIcon v-else :status="r.conn.status" />
                  <button class="icon-btn row-menu" type="button" tabindex="-1" :aria-label="`Actions for ${r.label}`" @click.stop="openMenu(r)"><Icon name="more" :size="14" /></button>
                </span>
              </template>
              <template v-else-if="r.kind === 'schema'">
                <Icon name="layers" :size="14" class="ico" />
                <span class="nm">{{ r.label }}</span>
              </template>
              <template v-else-if="r.kind === 'table' || r.kind === 'view'">
                <Icon :name="r.kind === 'view' ? 'eye' : 'tableIcon'" :size="14" class="ico" />
                <span class="nm">{{ r.label }}</span>
                <span class="meta">{{ r.kind === 'view' ? 'view' : rowsText(r.node?.rows ?? -1) }}</span>
              </template>
              <template v-else>
                <Icon :name="r.node?.pk ? 'key' : r.node?.fk ? 'link' : 'columns'" :size="14" class="ico" />
                <span class="nm">{{ r.label }}</span>
                <span class="meta" :class="r.node?.pk ? 'pk' : r.node?.fk ? 'fk' : 'type'">{{ colMeta(r).type }}<template v-if="colMeta(r).tag"> &middot; {{ colMeta(r).tag }}</template></span>
              </template>
            </div>
            <div v-if="r.kind === 'conn' && r.conn.warning" class="tn-note warn" role="note" :style="{ paddingLeft: '34px' }" data-testid="conn-warning">{{ r.conn.warning }}</div>
            <div v-if="r.kind === 'conn' && r.detail" :id="rowDescribedBy(r)" class="tn-note" :class="statusKey(r.conn.status) === 'err' ? 'err' : 'warn'" role="note" :style="{ paddingLeft: '34px' }" data-testid="conn-detail">
              {{ r.detail.text }}
              <button class="linkbtn" type="button" tabindex="-1" @click.stop="onDetailAction(r)">{{ r.detail.actionLabel }}</button>
            </div>
          </template>
        </div>

        <div v-if="onlyLocal" class="rail-empty" data-testid="rail-empty">
          <b>Your data somewhere else?</b>
          Connect Postgres, MotherDuck, a SQLite file or cloud storage. Nothing connects until you use it.
          <div style="margin-top: 10px;"><router-link class="btn btn-sm" to="/connections/new" data-testid="rail-add-card" @click="closeDrawer"><Icon name="plus" :size="14" /> Add a connection</router-link></div>
        </div>
        <div v-else-if="connectionList.length === 0" class="rail-empty" data-testid="rail-empty-none">
          <b>No connections</b>
          Add a database, a bucket or a DuckDB file to query it here.
        </div>
      </div>
    </div>

    <!-- Context menu -->
    <div
      v-if="menu"
      ref="menuEl"
      class="menu floating"
      role="menu"
      :aria-label="`Actions for ${menu.title}`"
      :style="{ left: menu.x + 'px', top: menu.y + 'px' }"
      data-testid="rail-menu"
      @keydown="onMenuKey"
    >
      <div class="mhead">{{ menu.title }}</div>
      <template v-for="(it, i) in menu.items" :key="it.id">
        <div v-if="it.sep" class="msep" role="separator"></div>
        <button
          v-else
          type="button"
          role="menuitem"
          class="mi"
          :class="{ danger: it.danger, disabled: it.disabled }"
          :aria-disabled="it.disabled ? 'true' : undefined"
          :tabindex="menuIndex === i ? 0 : -1"
          :data-testid="`menu-${it.id}`"
          @click="runItem(it)"
          @mouseenter="menuIndex = i"
        >
          <Icon v-if="it.icon" :name="it.icon" :size="16" />
          <span class="grow"><span class="t">{{ it.label }}</span></span>
          <span v-if="it.kbd" class="end"><kbd>{{ it.kbd }}</kbd></span>
        </button>
      </template>
    </div>
  </section>
</template>
