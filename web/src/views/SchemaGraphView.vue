<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import Button from "primevue/button";
import InputText from "primevue/inputtext";
import Message from "primevue/message";
import ProgressSpinner from "primevue/progressspinner";
import Select from "primevue/select";
import Tag from "primevue/tag";

import {
  api,
  UnauthenticatedError,
  type DataSource,
  type RelationEdge,
  type RelationGraph,
  type TableDetail,
  type TableSummary,
} from "@/api/client";
import { conditionNodeIds, describeCondition } from "@/lib/conditions";

const workspaceId = ref("");
const dataSources = ref<DataSource[]>([]);
const selectedSourceId = ref("");

const tables = ref<TableSummary[]>([]);
const filter = ref("");
const graph = ref<RelationGraph>({ tables: [], edges: [], truncated: false });
const tablesTruncated = ref(false);
// The whole source's table count, held separately from the filtered list so the
// empty state never quotes "2 tables" at someone who has typed a filter.
const sourceTableCount = ref(0);

const loading = ref(true);
const loadingTables = ref(false);
const failure = ref("");
const focusedTableId = ref("");
const detail = ref<TableDetail | null>(null);
const loadingDetail = ref(false);

// Hover is a preview and click is a selection: pointing at something says what
// it is, clicking keeps it on screen while you read the panel below.
const hoveredTableId = ref("");
const hoveredEdgeId = ref("");
const selectedEdge = ref<RelationEdge | null>(null);
// Guards name columns by id. Resolved on demand when an edge is opened, and
// kept, because the same columns recur across a source's relations.
const columnNames = ref<Record<string, string>>({});

/** Tables that actually take part in a relation, which is what the graph draws. */
const connected = computed(() => new Set(graph.value.tables.map((table) => table.id)));

async function loadWorkspace(): Promise<void> {
  loading.value = true;
  failure.value = "";
  try {
    const [projects, sources] = await Promise.all([api.listProjects(), api.listAllDataSources()]);
    workspaceId.value = projects[0]?.id ?? "";
    dataSources.value = sources;
    if (!selectedSourceId.value) selectedSourceId.value = sources[0]?.id ?? "";
  } catch (error) {
    if (error instanceof UnauthenticatedError) return;
    failure.value = error instanceof Error ? error.message : "Could not load data sources.";
  } finally {
    loading.value = false;
  }
}

async function loadSource(): Promise<void> {
  if (!workspaceId.value || !selectedSourceId.value) return;
  loadingTables.value = true;
  failure.value = "";
  focusedTableId.value = "";
  filter.value = "";
  try {
    const [imported, relations] = await Promise.all([
      api.listTables(workspaceId.value, selectedSourceId.value, filter.value),
      api.relationGraph(workspaceId.value, selectedSourceId.value),
    ]);
    tables.value = imported.tables;
    tablesTruncated.value = imported.truncated;
    sourceTableCount.value = imported.tables.length;
    graph.value = relations;
    layout();
  } catch (error) {
    if (error instanceof UnauthenticatedError) return;
    failure.value = error instanceof Error ? error.message : "Could not load this data source.";
  } finally {
    loadingTables.value = false;
  }
}

async function refilter(): Promise<void> {
  if (!workspaceId.value || !selectedSourceId.value) return;
  loadingTables.value = true;
  try {
    const listed = await api.listTables(workspaceId.value, selectedSourceId.value, filter.value);
    tables.value = listed.tables;
    tablesTruncated.value = listed.truncated;
  } catch (error) {
    if (error instanceof UnauthenticatedError) return;
    failure.value = error instanceof Error ? error.message : "Could not filter tables.";
  } finally {
    loadingTables.value = false;
  }
}

// --- Layout -----------------------------------------------------------------
// A small force simulation, run once per graph rather than animated: edges pull
// their two tables together, every pair pushes apart, and the result is frozen.
// Deterministic, because the starting ring is by index rather than random — the
// same relations always draw the same picture.

// The canvas is wider than it is tall, so the coordinate space is too. A square
// viewBox letterboxes inside the panel and shrinks the drawing until the labels
// stop being readable.
const VIEW_WIDTH = 1400;
const VIEW_HEIGHT = 800;
const positions = ref<Record<string, { x: number; y: number }>>({});

function layout(): void {
  const nodes = graph.value.tables;
  if (!nodes.length) {
    positions.value = {};
    return;
  }
  const placed: Record<string, { x: number; y: number }> = {};
  nodes.forEach((table, index) => {
    const angle = (2 * Math.PI * index) / nodes.length;
    placed[table.id] = {
      x: VIEW_WIDTH / 2 + VIEW_WIDTH * 0.3 * Math.cos(angle),
      y: VIEW_HEIGHT / 2 + VIEW_HEIGHT * 0.3 * Math.sin(angle),
    };
  });

  const ideal = Math.max(120, Math.min(340, 1600 / Math.sqrt(nodes.length)));
  for (let pass = 0; pass < 220; pass += 1) {
    const cooling = 1 - pass / 220;
    const force: Record<string, { x: number; y: number }> = {};
    for (const table of nodes) force[table.id] = { x: 0, y: 0 };

    for (let a = 0; a < nodes.length; a += 1) {
      for (let b = a + 1; b < nodes.length; b += 1) {
        const first = placed[nodes[a].id];
        const second = placed[nodes[b].id];
        let dx = first.x - second.x;
        let dy = first.y - second.y;
        let distance = Math.hypot(dx, dy) || 0.01;
        // Two tables landing on the same point have no direction to separate
        // along, so give them one from their order rather than from a random.
        if (distance < 0.05) {
          dx = (a - b) * 0.05;
          dy = 0.05;
          distance = Math.hypot(dx, dy);
        }
        const push = (ideal * ideal) / distance;
        force[nodes[a].id].x += (dx / distance) * push;
        force[nodes[a].id].y += (dy / distance) * push;
        force[nodes[b].id].x -= (dx / distance) * push;
        force[nodes[b].id].y -= (dy / distance) * push;
      }
    }

    for (const edge of graph.value.edges) {
      const from = placed[edge.sourceTableId];
      const to = placed[edge.targetTableId];
      if (!from || !to || edge.sourceTableId === edge.targetTableId) continue;
      const dx = to.x - from.x;
      const dy = to.y - from.y;
      const distance = Math.hypot(dx, dy) || 0.01;
      const pull = (distance * distance) / ideal;
      force[edge.sourceTableId].x += (dx / distance) * pull;
      force[edge.sourceTableId].y += (dy / distance) * pull;
      force[edge.targetTableId].x -= (dx / distance) * pull;
      force[edge.targetTableId].y -= (dy / distance) * pull;
    }

    for (const table of nodes) {
      const point = placed[table.id];
      const shift = force[table.id];
      const magnitude = Math.hypot(shift.x, shift.y) || 1;
      const step = Math.min(magnitude, ideal * cooling);
      point.x = clamp(point.x + (shift.x / magnitude) * step, 80, VIEW_WIDTH - 80);
      point.y = clamp(point.y + (shift.y / magnitude) * step, 50, VIEW_HEIGHT - 50);
    }
  }
  positions.value = placed;
}

function clamp(value: number, low: number, high: number): number {
  return Math.min(high, Math.max(low, value));
}

// --- Pan and zoom ------------------------------------------------------------

const zoom = ref(1);
const pan = ref({ x: 0, y: 0 });
const dragging = ref(false);
let dragOrigin = { x: 0, y: 0, panX: 0, panY: 0 };

const viewBox = computed(() => {
  const width = VIEW_WIDTH / zoom.value;
  const height = VIEW_HEIGHT / zoom.value;
  return `${pan.value.x} ${pan.value.y} ${width} ${height}`;
});

function startDrag(event: PointerEvent): void {
  dragging.value = true;
  dragOrigin = { x: event.clientX, y: event.clientY, panX: pan.value.x, panY: pan.value.y };
  (event.currentTarget as Element).setPointerCapture(event.pointerId);
}

function drag(event: PointerEvent): void {
  if (!dragging.value) return;
  const scale = VIEW_WIDTH / zoom.value / 900;
  pan.value = {
    x: dragOrigin.panX - (event.clientX - dragOrigin.x) * scale,
    y: dragOrigin.panY - (event.clientY - dragOrigin.y) * scale,
  };
}

function endDrag(event: PointerEvent): void {
  dragging.value = false;
  (event.currentTarget as Element).releasePointerCapture(event.pointerId);
}

function changeZoom(factor: number): void {
  zoom.value = clamp(zoom.value * factor, 0.4, 4);
}

function resetView(): void {
  zoom.value = 1;
  pan.value = { x: 0, y: 0 };
}

// --- Reading the graph -------------------------------------------------------

const NODE_RADIUS = 11;
const ARROW_CLEARANCE = 16;

function edgePath(edge: RelationEdge): string {
  const from = positions.value[edge.sourceTableId];
  const to = positions.value[edge.targetTableId];
  if (!from || !to) return "";
  // Bow the line so two relations between the same pair stay distinguishable.
  const controlX = (from.x + to.x) / 2 + (to.y - from.y) * 0.12;
  const controlY = (from.y + to.y) / 2 - (to.x - from.x) * 0.12;
  // Stop short of both circles. Centre to centre would bury the arrowhead under
  // the target, and the edge would lose the one thing it has to show: direction.
  const start = pullBack(from, { x: controlX, y: controlY }, NODE_RADIUS + 2);
  const end = pullBack(to, { x: controlX, y: controlY }, NODE_RADIUS + ARROW_CLEARANCE);
  return `M ${start.x} ${start.y} Q ${controlX} ${controlY} ${end.x} ${end.y}`;
}

/** Moves an endpoint toward the curve's control point by a fixed distance. */
function pullBack(
  point: { x: number; y: number },
  toward: { x: number; y: number },
  distance: number,
): { x: number; y: number } {
  const dx = toward.x - point.x;
  const dy = toward.y - point.y;
  const length = Math.hypot(dx, dy) || 1;
  return { x: point.x + (dx / length) * distance, y: point.y + (dy / length) * distance };
}

function isFocused(tableId: string): boolean {
  if (!focusedTableId.value) return false;
  if (focusedTableId.value === tableId) return true;
  return graph.value.edges.some(
    (edge) =>
      (edge.sourceTableId === focusedTableId.value && edge.targetTableId === tableId) ||
      (edge.targetTableId === focusedTableId.value && edge.sourceTableId === tableId),
  );
}

function edgeFocused(edge: RelationEdge): boolean {
  if (!focusedTableId.value) return true;
  return (
    edge.sourceTableId === focusedTableId.value || edge.targetTableId === focusedTableId.value
  );
}

function columnName(nodeId: string): string {
  return columnNames.value[nodeId] ?? nodeId;
}

const selectedGuard = computed(() =>
  selectedEdge.value?.guard ? describeCondition(selectedEdge.value.guard, columnName) : "",
);

async function openEdge(edge: RelationEdge): Promise<void> {
  if (selectedEdge.value?.relationId === edge.relationId) {
    selectedEdge.value = null;
    return;
  }
  selectedEdge.value = edge;
  focusedTableId.value = "";
  detail.value = null;
  const wanted = [...conditionNodeIds(edge.guard)].filter((id) => !(id in columnNames.value));
  if (!wanted.length) return;
  const resolved = await Promise.all(
    wanted.map(async (id) => {
      try {
        const node = await api.node(workspaceId.value, id);
        return [id, node.qualifiedName] as const;
      } catch {
        // A name is a convenience; the guard is still readable without it.
        return [id, id] as const;
      }
    }),
  );
  columnNames.value = { ...columnNames.value, ...Object.fromEntries(resolved) };
}

/** True while an edge should be drawn lit: hovered, selected, or on a hovered table. */
function edgeLit(edge: RelationEdge): boolean {
  if (hoveredEdgeId.value === edge.relationId) return true;
  if (selectedEdge.value?.relationId === edge.relationId) return true;
  if (!hoveredTableId.value) return false;
  return edge.sourceTableId === hoveredTableId.value || edge.targetTableId === hoveredTableId.value;
}

/**
 * True while a table is lit: the one being pointed at, a table it reaches, or
 * an endpoint of the edge being pointed at. Pointing at a table should answer
 * "what does this connect to", which means lighting the far end too.
 */
function tableLit(tableId: string): boolean {
  if (hoveredTableId.value === tableId) return true;
  const edge = graph.value.edges.find((candidate) => candidate.relationId === hoveredEdgeId.value);
  if (edge && (edge.sourceTableId === tableId || edge.targetTableId === tableId)) return true;
  if (!hoveredTableId.value) return false;
  return graph.value.edges.some(
    (candidate) =>
      (candidate.sourceTableId === hoveredTableId.value && candidate.targetTableId === tableId) ||
      (candidate.targetTableId === hoveredTableId.value && candidate.sourceTableId === tableId),
  );
}

async function focusTable(table: TableSummary): Promise<void> {
  selectedEdge.value = null;
  if (focusedTableId.value === table.id) {
    focusedTableId.value = "";
    detail.value = null;
    return;
  }
  focusedTableId.value = table.id;
  detail.value = null;
  loadingDetail.value = true;
  try {
    detail.value = await api.tableDetail(workspaceId.value, table.id);
  } catch (error) {
    if (error instanceof UnauthenticatedError) return;
    failure.value = error instanceof Error ? error.message : "Could not read that table.";
  } finally {
    loadingDetail.value = false;
  }
}

/** Marks the columns a relation joins on, so they stand out in the column list. */
const joinedColumns = computed(() => {
  const names = new Set<string>();
  for (const edge of graph.value.edges) {
    if (edge.sourceTableId === focusedTableId.value) names.add(edge.sourceColumn);
    if (edge.targetTableId === focusedTableId.value) names.add(edge.targetColumn);
  }
  return names;
});

const focusedEdges = computed(() =>
  graph.value.edges.filter((edge) => edgeFocused(edge) && focusedTableId.value),
);

function tableName(tableId: string): string {
  return graph.value.tables.find((table) => table.id === tableId)?.name ?? tableId;
}

/** The midpoint of an edge's curve, where its hover label sits. */
function edgeLabelPoint(edge: RelationEdge): { x: number; y: number } {
  const from = positions.value[edge.sourceTableId];
  const to = positions.value[edge.targetTableId];
  if (!from || !to) return { x: 0, y: 0 };
  const controlX = (from.x + to.x) / 2 + (to.y - from.y) * 0.12;
  const controlY = (from.y + to.y) / 2 - (to.x - from.x) * 0.12;
  // A quadratic curve at t=0.5 sits midway between its control point and the
  // straight chord, not on either.
  return {
    x: 0.25 * from.x + 0.5 * controlX + 0.25 * to.x,
    y: 0.25 * from.y + 0.5 * controlY + 0.25 * to.y,
  };
}

onMounted(async () => {
  await loadWorkspace();
  await loadSource();
});
watch(selectedSourceId, loadSource);
</script>

<template>
  <header class="page-head">
    <div>
      <h1>Relation graph</h1>
      <p>
        The tables one data source imported, and the approved relations between them. Relations join
        columns; the graph draws the tables that own them.
      </p>
    </div>
    <Select
      v-model="selectedSourceId"
      :options="dataSources"
      option-label="name"
      option-value="id"
      placeholder="Choose a data source"
      class="source-picker"
    />
  </header>

  <Message v-if="failure" severity="error" :closable="false">{{ failure }}</Message>

  <div v-if="loading" class="loading"><ProgressSpinner style="width: 2rem; height: 2rem" /></div>

  <Message v-else-if="!dataSources.length" severity="warn" :closable="false">
    No data sources registered yet.
  </Message>

  <div v-else class="split">
    <aside class="tables">
      <div class="tables-head">
        <InputText
          v-model="filter"
          placeholder="Filter tables"
          size="small"
          fluid
          @keyup.enter="refilter"
        />
        <span class="count">
          <template v-if="tablesTruncated">first </template>{{ tables.length }} table{{
            tables.length === 1 ? "" : "s"
          }}<template v-if="tablesTruncated"> — filter to see the rest</template>
          <template v-if="connected.size">&nbsp;· {{ connected.size }} in the graph</template>
        </span>
      </div>
      <div v-if="loadingTables" class="loading small">
        <ProgressSpinner style="width: 1.5rem; height: 1.5rem" />
      </div>
      <ul v-else class="table-list">
        <li v-if="!tables.length" class="muted empty-row">No table matches that filter.</li>
        <li
          v-for="table in tables"
          :key="table.id"
          :class="{ related: connected.has(table.id), focused: focusedTableId === table.id }"
        >
          <button type="button" @click="focusTable(table)">
            <span class="table-name">{{ table.name }}</span>
            <span v-if="connected.has(table.id)" class="dot" aria-label="in the graph" />
          </button>
        </li>
      </ul>
    </aside>

    <section class="canvas">
      <div class="canvas-head">
        <div class="legend">
          <span><i class="swatch solid" /> unconditional</span>
          <span><i class="swatch dashed" /> conditional</span>
        </div>
        <div class="canvas-actions">
          <Tag v-if="graph.truncated" value="truncated" severity="warn" />
          <Button icon="pi pi-search-minus" text size="small" @click="changeZoom(1 / 1.25)" />
          <Button icon="pi pi-search-plus" text size="small" @click="changeZoom(1.25)" />
          <Button label="Reset" text size="small" @click="resetView" />
        </div>
      </div>

      <div v-if="!graph.edges.length" class="empty-graph">
        <p class="empty-title">No relations yet</p>
        <p class="muted">
          This source has {{ sourceTableCount }} table{{ sourceTableCount === 1 ? "" : "s" }} and no
          approved relations. dbgraph does not infer them: an agent reads the application source and
          proposes them over MCP, and a reviewer approves. Declared foreign keys, where a database
          has them, arrive with a scan.
        </p>
      </div>

      <svg
        v-else
        class="graph"
        :viewBox="viewBox"
        :class="{ dragging }"
        role="img"
        aria-label="Relation graph"
        @pointerdown="startDrag"
        @pointermove="drag"
        @pointerup="endDrag"
        @pointercancel="endDrag"
      >
        <defs>
          <marker
            id="arrow"
            viewBox="0 0 10 10"
            refX="8"
            refY="5"
            markerWidth="5"
            markerHeight="5"
            orient="auto-start-reverse"
          >
            <path d="M 0 0 L 10 5 L 0 10 z" class="arrow-head" />
          </marker>
        </defs>

        <g v-for="edge in graph.edges" :key="edge.relationId" class="edge-group">
          <path
            :d="edgePath(edge)"
            class="edge"
            :class="{
              conditional: edge.conditional,
              dimmed: !edgeFocused(edge),
              lit: edgeLit(edge),
              selected: selectedEdge?.relationId === edge.relationId,
            }"
            marker-end="url(#arrow)"
          />
          <!-- A 2px line is nearly impossible to point at, so the pointer
               target is a wide invisible stroke following the same curve. -->
          <path
            :d="edgePath(edge)"
            class="edge-hit"
            @pointerenter="hoveredEdgeId = edge.relationId"
            @pointerleave="hoveredEdgeId = ''"
            @pointerdown.stop
            @click.stop="openEdge(edge)"
          >
            <title>
              {{ tableName(edge.sourceTableId) }}.{{ edge.sourceColumn }} →
              {{ tableName(edge.targetTableId) }}.{{ edge.targetColumn }}
            </title>
          </path>
          <text
            v-if="edgeLit(edge)"
            class="edge-label"
            :x="edgeLabelPoint(edge).x"
            :y="edgeLabelPoint(edge).y"
          >
            {{ edge.sourceColumn }} → {{ edge.targetColumn }}
          </text>
        </g>

        <g
          v-for="table in graph.tables"
          :key="table.id"
          class="node"
          :class="{ dimmed: focusedTableId && !isFocused(table.id), lit: tableLit(table.id) }"
          @pointerenter="hoveredTableId = table.id"
          @pointerleave="hoveredTableId = ''"
          @pointerdown.stop
          @click.stop="focusTable(table)"
        >
          <circle
            class="node-hit"
            :cx="positions[table.id]?.x"
            :cy="positions[table.id]?.y"
            r="22"
          />
          <circle
            class="node-dot"
            :cx="positions[table.id]?.x"
            :cy="positions[table.id]?.y"
            r="11"
          />
          <text :x="positions[table.id]?.x" :y="(positions[table.id]?.y ?? 0) - 20">
            {{ table.name }}
          </text>
          <title>{{ table.qualifiedName }}</title>
        </g>
      </svg>

      <div v-if="selectedEdge" class="detail">
        <p class="detail-title">
          <code>{{ tableName(selectedEdge.sourceTableId) }}.{{ selectedEdge.sourceColumn }}</code>
          <span class="arrow">→</span>
          <code>{{ tableName(selectedEdge.targetTableId) }}.{{ selectedEdge.targetColumn }}</code>
          <span class="detail-count">
            {{ Math.round(selectedEdge.confidence * 100) }}% confident
          </span>
        </p>
        <dl class="edge-clauses">
          <template v-if="selectedGuard">
            <dt>Applies when</dt>
            <dd><code>{{ selectedGuard }}</code></dd>
          </template>
          <template v-else>
            <dt>Applies</dt>
            <dd>always — this relation carries no guard</dd>
          </template>
          <dt>Relation</dt>
          <dd><code>{{ selectedEdge.relationId }}</code></dd>
        </dl>
      </div>

      <div v-else-if="focusedTableId" class="detail">
        <p class="detail-title">
          {{ detail?.qualifiedName ?? tableName(focusedTableId) }}
          <span v-if="detail" class="detail-count">
            {{ detail.columns.length }} column{{ detail.columns.length === 1 ? "" : "s" }} ·
            {{ detail.indexes.length }} index{{ detail.indexes.length === 1 ? "" : "es" }}
          </span>
        </p>

        <div v-if="loadingDetail" class="loading small">
          <ProgressSpinner style="width: 1.5rem; height: 1.5rem" />
        </div>

        <div v-else class="detail-grid">
          <section>
            <h3>Columns</h3>
            <table class="fields">
              <tbody>
                <tr
                  v-for="column in detail?.columns ?? []"
                  :key="column.id"
                  :class="{ joined: joinedColumns.has(column.name) }"
                >
                  <td class="field-name">{{ column.name }}</td>
                  <td class="field-type">{{ column.dataType }}</td>
                  <td class="field-flag">
                    <span v-if="!column.nullable" class="not-null">NOT NULL</span>
                  </td>
                </tr>
              </tbody>
            </table>
            <p v-if="!detail?.columns.length" class="muted small-note">No columns recorded.</p>
          </section>

          <section>
            <h3>Indexes</h3>
            <ul class="indexes">
              <li v-for="index in detail?.indexes ?? []" :key="index.name">
                <span class="index-name">{{ index.name }}</span>
                <Tag v-if="index.primary" value="primary" severity="success" />
                <Tag v-else-if="index.unique" value="unique" severity="info" />
                <code>{{ index.columns.join(", ") }}</code>
              </li>
            </ul>
            <p v-if="!detail?.indexes.length" class="muted small-note">
              No indexes recorded. They arrive with a scan — re-scan this source if it was imported
              before dbgraph read them.
            </p>
          </section>

          <section v-if="focusedEdges.length">
            <h3>Relations</h3>
            <ul class="relations">
              <li v-for="edge in focusedEdges" :key="edge.relationId">
                <code>{{ tableName(edge.sourceTableId) }}.{{ edge.sourceColumn }}</code>
                <span class="arrow">→</span>
                <code>{{ tableName(edge.targetTableId) }}.{{ edge.targetColumn }}</code>
                <Tag v-if="edge.conditional" value="conditional" severity="secondary" />
              </li>
            </ul>
          </section>
        </div>
      </div>
    </section>
  </div>
</template>

<style scoped>
.page-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 1rem;
  margin-bottom: 1.25rem;
}

h1 {
  margin: 0 0 0.25rem;
  font-size: 1.35rem;
}

.page-head p {
  max-width: 70ch;
  margin: 0;
  color: var(--p-text-muted-color);
  font-size: 0.9rem;
}

.source-picker {
  min-width: 16rem;
}

.loading {
  display: grid;
  place-items: center;
  padding: 3rem;
}

.loading.small {
  padding: 1.5rem;
}

.muted {
  color: var(--p-text-muted-color);
}

.split {
  display: grid;
  grid-template-columns: minmax(14rem, 20rem) 1fr;
  gap: 1rem;
  align-items: start;
}

.tables {
  border: 1px solid var(--p-content-border-color);
  border-radius: 8px;
  overflow: hidden;
}

.tables-head {
  display: grid;
  gap: 0.4rem;
  padding: 0.6rem;
  border-bottom: 1px solid var(--p-content-border-color);
}

.count {
  font-size: 0.75rem;
  color: var(--p-text-muted-color);
}

.table-list {
  max-height: 60vh;
  overflow-y: auto;
  margin: 0;
  padding: 0.3rem;
  list-style: none;
}

.table-list button {
  display: flex;
  align-items: center;
  justify-content: space-between;
  width: 100%;
  padding: 0.3rem 0.5rem;
  border: 0;
  border-radius: 5px;
  background: none;
  color: inherit;
  font: inherit;
  font-size: 0.85rem;
  text-align: left;
  cursor: pointer;
}

.table-list button:hover {
  background: var(--p-content-hover-background);
}

.table-list li.related button {
  font-weight: 600;
}

.table-list li.focused button {
  background: var(--p-highlight-background);
  color: var(--p-highlight-color);
}

.empty-row {
  padding: 0.5rem;
  font-size: 0.85rem;
}

.dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--p-primary-color);
}

.canvas {
  border: 1px solid var(--p-content-border-color);
  border-radius: 8px;
  overflow: hidden;
}

.canvas-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 1rem;
  padding: 0.4rem 0.6rem;
  border-bottom: 1px solid var(--p-content-border-color);
}

.legend {
  display: flex;
  gap: 1rem;
  font-size: 0.75rem;
  color: var(--p-text-muted-color);
}

.legend span {
  display: flex;
  align-items: center;
  gap: 0.35rem;
}

.swatch {
  width: 18px;
  height: 0;
  border-top: 2px solid var(--p-text-muted-color);
}

.swatch.dashed {
  border-top-style: dashed;
}

.canvas-actions {
  display: flex;
  align-items: center;
  gap: 0.2rem;
}

.empty-graph {
  display: grid;
  gap: 0.5rem;
  padding: 3.5rem 2rem;
  text-align: center;
}

.empty-title {
  margin: 0;
  font-size: 1rem;
  font-weight: 600;
}

.empty-graph .muted {
  max-width: 56ch;
  margin: 0 auto;
  font-size: 0.85rem;
  line-height: 1.5;
}

.graph {
  display: block;
  width: 100%;
  height: 60vh;
  cursor: grab;
  touch-action: none;
}

.graph.dragging {
  cursor: grabbing;
}

.edge {
  fill: none;
  stroke: var(--p-text-muted-color);
  stroke-width: 2.5;
  opacity: 0.75;
  transition:
    stroke 120ms ease,
    stroke-width 120ms ease,
    opacity 120ms ease;
  pointer-events: none;
}

/* The pointer target: invisible, wide, and on the same curve. Without it a
   2.5px line is a pixel-hunt. */
.edge-hit {
  fill: none;
  stroke: transparent;
  stroke-width: 22;
  cursor: pointer;
  pointer-events: stroke;
}

.edge.lit {
  stroke: var(--p-primary-color);
  stroke-width: 3.5;
  opacity: 1;
}

.edge.selected {
  stroke-width: 4.5;
}

.edge-label {
  fill: var(--p-text-color);
  font-size: 18px;
  font-weight: 500;
  text-anchor: middle;
  pointer-events: none;
  paint-order: stroke;
  stroke: var(--p-content-background);
  stroke-width: 6px;
  stroke-linejoin: round;
}

.edge.conditional {
  stroke-dasharray: 10 7;
}

.edge.dimmed {
  opacity: 0.12;
}

.arrow-head {
  fill: var(--p-text-muted-color);
}

.node {
  cursor: pointer;
}

.node-dot {
  fill: var(--p-primary-color);
  stroke: var(--p-content-background);
  stroke-width: 2;
  transition:
    r 120ms ease,
    stroke-width 120ms ease;
}

/* Same reason as the edge hit path: the circle a person aims at is bigger
   than the dot they see. */
.node-hit {
  fill: transparent;
  pointer-events: fill;
}

.node.lit .node-dot {
  r: 15;
  stroke-width: 3;
}

.node.lit text {
  font-weight: 700;
}

.node text {
  pointer-events: none;
  fill: var(--p-text-color);
  font-size: 22px;
  font-weight: 500;
  text-anchor: middle;
  /* The label sits over edges, so outline it in the panel colour rather than
     boxing it: a stroke behind the glyphs keeps names legible on a crossing. */
  paint-order: stroke;
  stroke: var(--p-content-background);
  stroke-width: 5px;
  stroke-linejoin: round;
}

.node.dimmed {
  opacity: 0.25;
}

.detail {
  padding: 0.6rem 0.8rem;
  border-top: 1px solid var(--p-content-border-color);
}

.detail-title {
  margin: 0 0 0.4rem;
  font-size: 0.85rem;
  font-weight: 600;
}

.edge-clauses {
  display: grid;
  grid-template-columns: 8rem 1fr;
  gap: 0.25rem 0.75rem;
  margin: 0;
  font-size: 0.82rem;
}

.edge-clauses dt {
  color: var(--p-text-muted-color);
}

.edge-clauses dd {
  margin: 0;
}

.detail-title .arrow {
  margin: 0 0.4rem;
}

.detail-count {
  margin-left: 0.5rem;
  font-weight: 400;
  color: var(--p-text-muted-color);
}

.detail-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(15rem, 1fr));
  gap: 1.25rem;
  align-items: start;
}

.detail h3 {
  margin: 0 0 0.4rem;
  font-size: 0.7rem;
  font-weight: 600;
  letter-spacing: 0.06em;
  text-transform: uppercase;
  color: var(--p-text-muted-color);
}

.fields {
  width: 100%;
  border-collapse: collapse;
  font-size: 0.8rem;
}

.fields td {
  padding: 0.15rem 0.5rem 0.15rem 0;
  vertical-align: top;
}

.field-name {
  font-family: var(--font-mono, ui-monospace, monospace);
}

.field-type {
  color: var(--p-text-muted-color);
}

.field-flag {
  width: 5rem;
}

.not-null {
  font-size: 0.65rem;
  letter-spacing: 0.04em;
  color: var(--p-text-muted-color);
}

/* A column a relation joins on is the reason this table is in the graph. */
.fields tr.joined .field-name {
  font-weight: 700;
  color: var(--p-primary-color);
}

.indexes,
.relations {
  display: grid;
  gap: 0.3rem;
  margin: 0;
  padding: 0;
  list-style: none;
}

.indexes li,
.relations li {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 0.4rem;
  font-size: 0.8rem;
}

.index-name {
  font-weight: 600;
}

.small-note {
  margin: 0;
  font-size: 0.75rem;
  line-height: 1.45;
}

.arrow {
  color: var(--p-text-muted-color);
}

@media (max-width: 900px) {
  .split {
    grid-template-columns: 1fr;
  }
}
</style>
