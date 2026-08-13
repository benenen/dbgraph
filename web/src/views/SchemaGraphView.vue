<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import Button from "primevue/button";
import Drawer from "primevue/drawer";
import InputText from "primevue/inputtext";
import Message from "primevue/message";
import ProgressSpinner from "primevue/progressspinner";
import Select from "primevue/select";
import Tab from "primevue/tab";
import TabList from "primevue/tablist";
import TabPanel from "primevue/tabpanel";
import TabPanels from "primevue/tabpanels";
import Tabs from "primevue/tabs";
import Tag from "primevue/tag";

import RelationSphere from "@/components/RelationSphere.vue";
import {
  api,
  UnauthenticatedError,
  type Cardinality,
  type DataSource,
  type RelationEdge,
  type RelationGraph,
  type TableDetail,
  type TableSummary,
} from "@/api/client";
import { conditionNodeIds, describeCondition } from "@/lib/conditions";

const dataSources = ref<DataSource[]>([]);
const selectedSourceId = ref("");

const tables = ref<TableSummary[]>([]);
const filter = ref("");
// The list is a way in, not the subject: it starts folded to a spine so the
// drawing opens at full width, and unfolds when someone goes looking for a
// table by name.
const tablesCollapsed = ref(true);
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
const tableDetailFailure = ref("");
// Table metadata is a reference sheet rather than part of the drawing. It
// opens beside the graph so selecting a table never pushes the sphere down.
const tableDrawerOpen = ref(false);
const tableDrawerTab = ref<"table" | "index" | "relations">("table");

const selectedEdge = ref<RelationEdge | null>(null);
interface RelationSphereHandle {
  focusGraph(): void;
}
const relationSphere = ref<RelationSphereHandle | null>(null);
// Guards name columns by id. Resolved on demand when an edge is opened, and
// kept, because the same columns recur across a source's relations.
const columnNames = ref<Record<string, string>>({});
let tableDetailRequestGeneration = 0;
let sourceLoadGeneration = 0;
let focusGraphAfterDrawerClose = false;

/** Tables that actually take part in a relation, which is what the graph draws. */
const connected = computed(() => new Set(graph.value.tables.map((table) => table.id)));

async function loadWorkspace(): Promise<void> {
  loading.value = true;
  failure.value = "";
  try {
    const sources = await api.listAllDataSources();
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
  const sourceId = selectedSourceId.value;
  if (!sourceId) return;
  const requestGeneration = ++sourceLoadGeneration;
  loadingTables.value = true;
  failure.value = "";
  tables.value = [];
  graph.value = { tables: [], edges: [], truncated: false };
  sourceTableCount.value = 0;
  focusedTableId.value = "";
  selectedEdge.value = null;
  detail.value = null;
  tableDetailFailure.value = "";
  tableDetailRequestGeneration += 1;
  loadingDetail.value = false;
  tableDrawerOpen.value = false;
  tableDrawerTab.value = "table";
  columnNames.value = {};
  filter.value = "";
  try {
    const [imported, relations] = await Promise.all([
      api.listTables(sourceId, filter.value),
      api.relationGraph(sourceId),
    ]);
    if (requestGeneration !== sourceLoadGeneration || selectedSourceId.value !== sourceId) return;
    tables.value = imported.tables;
    tablesTruncated.value = imported.truncated;
    sourceTableCount.value = imported.tables.length;
    graph.value = relations;
  } catch (error) {
    if (error instanceof UnauthenticatedError) return;
    if (requestGeneration !== sourceLoadGeneration || selectedSourceId.value !== sourceId) return;
    failure.value = error instanceof Error ? error.message : "Could not load this data source.";
  } finally {
    if (requestGeneration === sourceLoadGeneration) loadingTables.value = false;
  }
}

async function refilter(): Promise<void> {
  const sourceId = selectedSourceId.value;
  const sourceGeneration = sourceLoadGeneration;
  if (!sourceId) return;
  loadingTables.value = true;
  try {
    const listed = await api.listTables(sourceId, filter.value);
    if (sourceGeneration !== sourceLoadGeneration || selectedSourceId.value !== sourceId) return;
    tables.value = listed.tables;
    tablesTruncated.value = listed.truncated;
  } catch (error) {
    if (error instanceof UnauthenticatedError) return;
    if (sourceGeneration !== sourceLoadGeneration || selectedSourceId.value !== sourceId) return;
    failure.value = error instanceof Error ? error.message : "Could not filter tables.";
  } finally {
    if (sourceGeneration === sourceLoadGeneration) loadingTables.value = false;
  }
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

/** Names the columns a guard mentions, so a condition reads as words not ids. */
async function resolveColumnNames(ids: Iterable<string>): Promise<void> {
  const wanted = [...new Set(ids)].filter((id) => !(id in columnNames.value));
  if (!wanted.length) return;
  const resolved = await Promise.all(
    wanted.map(async (id) => {
      try {
        const node = await api.node(id);
        return [id, node.qualifiedName] as const;
      } catch {
        // A name is a convenience; the guard is still readable without it.
        return [id, id] as const;
      }
    }),
  );
  columnNames.value = { ...columnNames.value, ...Object.fromEntries(resolved) };
}

/** Loads drawer metadata without allowing an older request to replace a newer table. */
async function loadTableDetail(tableId: string): Promise<void> {
  const sourceId = selectedSourceId.value;
  detail.value = null;
  tableDetailFailure.value = "";
  loadingDetail.value = true;
  const requestGeneration = ++tableDetailRequestGeneration;
  try {
    const loaded = await api.tableDetail(tableId);
    if (
      requestGeneration !== tableDetailRequestGeneration ||
      focusedTableId.value !== tableId ||
      selectedSourceId.value !== sourceId
    ) return;
    detail.value = loaded;
  } catch (error) {
    if (error instanceof UnauthenticatedError) return;
    if (
      requestGeneration !== tableDetailRequestGeneration ||
      focusedTableId.value !== tableId ||
      selectedSourceId.value !== sourceId
    ) return;
    tableDetailFailure.value = error instanceof Error ? error.message : "Could not read that table.";
  } finally {
    if (
      requestGeneration === tableDetailRequestGeneration &&
      selectedSourceId.value === sourceId
    ) loadingDetail.value = false;
  }
}

/** Opens a graph relation in the drawer and selects it in both views. */
async function openEdge(edge: RelationEdge): Promise<void> {
  selectedEdge.value = edge;
  const endpointIds = [edge.sourceTableId, edge.targetTableId];
  const tableId = endpointIds.includes(focusedTableId.value)
    ? focusedTableId.value
    : edge.sourceTableId;
  const needsDetail = detail.value?.id !== tableId || Boolean(tableDetailFailure.value);
  focusedTableId.value = tableId;
  tableDrawerTab.value = "relations";
  tableDrawerOpen.value = true;
  await Promise.all([
    resolveColumnNames(conditionNodeIds(edge.guard)),
    needsDetail ? loadTableDetail(tableId) : Promise.resolve(),
  ]);
}

/**
 * The cardinality as the notation people write on a schema diagram. Unknown
 * stays a word rather than a symbol: it is the absence of an answer, and "?:?"
 * would read as one.
 */
const CARDINALITY_LABELS: Record<Cardinality, string> = {
  ONE_TO_ONE: "1:1",
  ONE_TO_MANY: "1:N",
  MANY_TO_ONE: "N:1",
  MANY_TO_MANY: "N:N",
  UNKNOWN: "unknown",
};

/** Says what the notation is based on, since it is inferred and not measured. */
function cardinalityHint(edge: RelationEdge): string {
  const ends = `${edge.sourceColumn} → ${edge.targetColumn}`;
  switch (edge.cardinality) {
    case "ONE_TO_ONE":
      return `One to one: a unique index covers each side (${ends}).`;
    case "ONE_TO_MANY":
      return `One to many: ${edge.sourceColumn} is unique, ${edge.targetColumn} repeats.`;
    case "MANY_TO_ONE":
      return `Many to one: ${edge.sourceColumn} repeats, ${edge.targetColumn} is unique.`;
    case "MANY_TO_MANY":
      return `Many to many: no unique index covers either column on its own.`;
    default:
      return "Unknown: one of these tables was scanned without index metadata. Re-scan the source to find out.";
  }
}

/** The guard as a sentence, for a relation listed in the drawer tab. */
function guardText(edge: RelationEdge): string {
  return edge.guard ? describeCondition(edge.guard, columnName) : "";
}

/** Picks a relation out of the drawer tab and lights it in the drawing behind it. */
async function selectFromDrawer(edge: RelationEdge): Promise<void> {
  selectedEdge.value = edge;
  focusGraphAfterDrawerClose = true;
  tableDrawerOpen.value = false;
  await resolveColumnNames(conditionNodeIds(edge.guard));
}

function restoreGraphFocus(): void {
  if (!focusGraphAfterDrawerClose) return;
  focusGraphAfterDrawerClose = false;
  relationSphere.value?.focusGraph();
}

async function selectDrawerTab(value: string | number): Promise<void> {
  const selected = String(value);
  if (selected !== "table" && selected !== "index" && selected !== "relations") return;
  tableDrawerTab.value = selected;
  if (selected === "relations") {
    await resolveColumnNames(focusedEdges.value.flatMap((edge) => [...conditionNodeIds(edge.guard)]));
  }
}


async function focusTable(table: TableSummary): Promise<void> {
  selectedEdge.value = null;
  if (focusedTableId.value === table.id && tableDrawerOpen.value) {
    tableDrawerOpen.value = false;
    focusedTableId.value = "";
    detail.value = null;
    tableDetailFailure.value = "";
    tableDetailRequestGeneration += 1;
    loadingDetail.value = false;
    return;
  }
  focusedTableId.value = table.id;
  tableDrawerOpen.value = true;
  tableDrawerTab.value = "table";
  await loadTableDetail(table.id);
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

  <div v-else class="split" :class="{ 'tables-hidden': tablesCollapsed }">
    <aside class="tables" :class="{ collapsed: tablesCollapsed }">
      <div class="tables-head">
        <div class="head-row">
          <InputText
            v-if="!tablesCollapsed"
            v-model="filter"
            placeholder="Filter tables"
            size="small"
            fluid
            @keyup.enter="refilter"
          />
          <Button
            :icon="tablesCollapsed ? 'pi pi-angle-right' : 'pi pi-angle-left'"
            text
            size="small"
            class="tables-toggle"
            :aria-label="tablesCollapsed ? 'Show the table list' : 'Hide the table list'"
            :aria-expanded="!tablesCollapsed"
            @click="tablesCollapsed = !tablesCollapsed"
          />
        </div>
        <span v-if="!tablesCollapsed" class="count">
          <template v-if="tablesTruncated">first </template>{{ tables.length }} table{{
            tables.length === 1 ? "" : "s"
          }}<template v-if="tablesTruncated"> — filter to see the rest</template>
          <template v-if="connected.size">&nbsp;· {{ connected.size }} in the graph</template>
        </span>
      </div>

      <!-- Collapsed, the spine still says what is behind it and reopens on a
           click, so the list is folded away rather than hidden. -->
      <button
        v-if="tablesCollapsed"
        type="button"
        class="rail-label"
        @click="tablesCollapsed = false"
      >
        {{ tables.length }} table{{ tables.length === 1 ? "" : "s" }}
      </button>

      <div v-else-if="loadingTables" class="loading small">
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
      <div v-if="loadingTables" class="loading graph-loading">
        <ProgressSpinner style="width: 2rem; height: 2rem" />
      </div>
      <div v-else-if="!graph.edges.length" class="empty-graph">
        <p class="empty-title">No relations yet</p>
        <p class="muted">
          This source has {{ sourceTableCount }} table{{ sourceTableCount === 1 ? "" : "s" }} and no
          approved relations. dbgraph does not infer them: an agent reads the application source and
          proposes them over MCP, and a reviewer approves. Declared foreign keys, where a database
          has them, arrive with a scan.
        </p>
      </div>

      <RelationSphere
        v-else
        ref="relationSphere"
        :graph="graph"
        :focused-table-id="focusedTableId"
        :selected-edge-id="selectedEdge?.relationId ?? ''"
        :view-key="selectedSourceId"
        @select-table="focusTable"
        @select-edge="openEdge"
      />
    </section>
  </div>

  <Drawer
    v-model:visible="tableDrawerOpen"
    position="right"
    class="table-detail-drawer"
    style="width: min(42rem, 92vw)"
    :header="detail?.qualifiedName ?? tableName(focusedTableId)"
    :aria-label="detail?.qualifiedName ?? tableName(focusedTableId)"
    @after-hide="restoreGraphFocus"
  >
    <div class="table-detail">
      <div class="table-detail-meta">
        <span v-if="detail" class="detail-count">
          {{ detail.columns.length }} column{{ detail.columns.length === 1 ? "" : "s" }} ·
          {{ detail.indexes.length }} index{{ detail.indexes.length === 1 ? "" : "es" }}
        </span>
      </div>

      <Tabs :value="tableDrawerTab" lazy @update:value="selectDrawerTab">
        <TabList>
          <Tab value="table">Table</Tab>
          <Tab value="index">Index</Tab>
          <Tab value="relations">Relations</Tab>
        </TabList>
        <TabPanels class="table-tab-panels">
          <TabPanel value="table">
            <p v-if="detail?.comment" class="table-comment">{{ detail.comment }}</p>
            <div v-if="loadingDetail" class="loading small">
              <ProgressSpinner style="width: 1.5rem; height: 1.5rem" />
            </div>
            <Message v-else-if="tableDetailFailure" severity="error" :closable="false">
              {{ tableDetailFailure }}
            </Message>
            <section v-else-if="detail">
              <h3>Columns</h3>
              <table class="fields">
                <tbody>
                  <tr
                    v-for="column in detail.columns"
                    :key="column.id"
                    :class="{ joined: joinedColumns.has(column.name) }"
                  >
                    <td class="field-name">{{ column.name }}</td>
                    <td class="field-type" :title="column.dataType">{{ column.dataType }}</td>
                    <td class="field-flag">
                      <span v-if="!column.nullable" class="not-null">NOT NULL</span>
                    </td>
                    <td class="field-comment" :title="column.comment">{{ column.comment }}</td>
                  </tr>
                </tbody>
              </table>
              <p v-if="!detail.columns.length" class="muted small-note">No columns recorded.</p>
            </section>
          </TabPanel>

          <TabPanel value="index">
            <div v-if="loadingDetail" class="loading small">
              <ProgressSpinner style="width: 1.5rem; height: 1.5rem" />
            </div>
            <Message v-else-if="tableDetailFailure" severity="error" :closable="false">
              {{ tableDetailFailure }}
            </Message>
            <section v-else-if="detail">
              <h3>Indexes</h3>
              <ul class="indexes">
                <li v-for="index in detail.indexes" :key="index.name">
                  <span class="index-name">{{ index.name }}</span>
                  <Tag v-if="index.primary" value="primary" severity="success" />
                  <Tag v-else-if="index.unique" value="unique" severity="info" />
                  <code>{{ index.columns.join(", ") }}</code>
                </li>
              </ul>
              <p v-if="!detail.indexes.length" class="muted small-note">
                No indexes recorded. They arrive with a scan — re-scan this source if it was imported
                before dbgraph read them.
              </p>
            </section>
          </TabPanel>

          <TabPanel value="relations">
            <p class="drawer-lead muted">
              {{ focusedEdges.length }} approved relation{{ focusedEdges.length === 1 ? "" : "s" }}
              touching this table.
            </p>
            <ul class="drawer-relations">
              <li
                v-for="edge in focusedEdges"
                :key="edge.relationId"
                :class="{ current: selectedEdge?.relationId === edge.relationId }"
              >
                <button
                  type="button"
                  :aria-pressed="selectedEdge?.relationId === edge.relationId"
                  :aria-expanded="selectedEdge?.relationId === edge.relationId"
                  :aria-controls="`relation-details-${edge.relationId}`"
                  @click="selectFromDrawer(edge)"
                >
                  <span class="drawer-direction">
                    {{ edge.sourceTableId === focusedTableId ? "out" : "in" }}
                  </span>
                  <span class="drawer-ends">
                    <code>{{ tableName(edge.sourceTableId) }}.{{ edge.sourceColumn }}</code>
                    <span class="arrow">→</span>
                    <code>{{ tableName(edge.targetTableId) }}.{{ edge.targetColumn }}</code>
                  </span>
                  <span class="drawer-tags">
                    <span
                      class="cardinality"
                      :class="{ unknown: edge.cardinality === 'UNKNOWN' }"
                      :title="cardinalityHint(edge)"
                    >
                      {{ CARDINALITY_LABELS[edge.cardinality] }}
                    </span>
                    <Tag v-if="edge.conditional" value="conditional" severity="secondary" />
                    <span class="drawer-confidence">{{ Math.round(edge.confidence * 100) }}%</span>
                  </span>
                  <span v-if="guardText(edge)" class="drawer-guard">
                    when <code>{{ guardText(edge) }}</code>
                  </span>
                </button>
                <dl
                  v-if="selectedEdge?.relationId === edge.relationId"
                  :id="`relation-details-${edge.relationId}`"
                  class="drawer-relation-details"
                  role="region"
                  aria-label="Selected relation details"
                >
                  <dt>Cardinality</dt>
                  <dd>{{ cardinalityHint(edge) }}</dd>
                  <dt>Relation ID</dt>
                  <dd><code>{{ edge.relationId }}</code></dd>
                </dl>
              </li>
            </ul>
          </TabPanel>
        </TabPanels>
      </Tabs>
    </div>
  </Drawer>
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

/* Folding the list away hands its width to the drawing rather than leaving a
   gap where it was. */
.split.tables-hidden {
  grid-template-columns: auto 1fr;
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

.tables.collapsed .tables-head {
  padding: 0.3rem 0.15rem;
}

.head-row {
  display: flex;
  align-items: center;
  gap: 0.25rem;
}

.tables-toggle {
  flex: none;
}

/* Vertical, because a collapsed rail has height to spare and no width. */
.rail-label {
  writing-mode: vertical-rl;
  width: 100%;
  padding: 0.75rem 0;
  border: 0;
  background: none;
  color: var(--p-text-muted-color);
  font: inherit;
  font-size: 0.75rem;
  letter-spacing: 0.04em;
  white-space: nowrap;
  cursor: pointer;
}

.rail-label:hover {
  color: var(--p-text-color);
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

.graph-loading {
  min-height: 420px;
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

.table-detail {
  display: grid;
  gap: 1rem;
}

.table-detail-meta {
  display: flex;
  align-items: center;
  min-height: 2rem;
}

.table-detail-meta .detail-count {
  margin-left: 0;
}

.table-tab-panels {
  padding: 1rem 0 0;
}

.drawer-lead {
  margin: 0 0 0.75rem;
  font-size: 0.8rem;
}

.drawer-relations {
  display: grid;
  gap: 0.4rem;
  margin: 0;
  padding: 0;
  list-style: none;
}

.drawer-relations button {
  display: grid;
  gap: 0.3rem;
  width: 100%;
  padding: 0.55rem 0.6rem;
  border: 1px solid var(--p-content-border-color);
  border-radius: 6px;
  background: none;
  color: inherit;
  font: inherit;
  text-align: left;
  cursor: pointer;
}

.drawer-relations button:hover {
  background: var(--p-content-hover-background);
}

.drawer-relations li.current button {
  border-color: var(--p-primary-color);
}

/* Which way the relation points, from this table's side of it. */
.drawer-direction {
  font-size: 0.65rem;
  letter-spacing: 0.08em;
  text-transform: uppercase;
  color: var(--p-text-muted-color);
}

.drawer-ends {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 0.35rem;
  font-size: 0.8rem;
}

.drawer-tags {
  display: flex;
  align-items: center;
  gap: 0.4rem;
}

.drawer-confidence {
  font-size: 0.75rem;
  color: var(--p-text-muted-color);
}

/* Read as notation, so it stays monospaced and tight rather than becoming a
   third badge competing with the tags beside it. */
.cardinality {
  padding: 0.05rem 0.35rem;
  border: 1px solid var(--p-content-border-color);
  border-radius: 4px;
  font-family: var(--font-mono, ui-monospace, monospace);
  font-size: 0.75rem;
  font-weight: 600;
  white-space: nowrap;
}

.cardinality.unknown {
  font-family: inherit;
  font-weight: 400;
  font-style: italic;
  color: var(--p-text-muted-color);
}

.drawer-guard {
  font-size: 0.75rem;
  line-height: 1.5;
  color: var(--p-text-muted-color);
}

.drawer-relation-details {
  display: grid;
  grid-template-columns: 5.5rem 1fr;
  gap: 0.2rem 0.6rem;
  margin: 0 0.6rem 0.6rem;
  padding: 0.5rem 0.6rem 0;
  border-top: 1px solid var(--p-content-border-color);
  font-size: 0.75rem;
  line-height: 1.45;
}

.drawer-relation-details dt {
  color: var(--p-text-muted-color);
}

.drawer-relation-details dd {
  margin: 0;
}

.detail-count {
  margin-left: 0.5rem;
  font-weight: 400;
  color: var(--p-text-muted-color);
}

.table-detail h3 {
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

/* A type like "bigint(20) unsigned zerofill" wraps to three lines and drags
   its whole row out of alignment, so it stays on one line and truncates. */
.field-type {
  max-width: 16rem;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--p-text-muted-color);
}

.field-flag {
  width: 5.5rem;
  white-space: nowrap;
}

/* A comment is context, not identity: it reads quietly beside the column and
   truncates rather than pushing the name and type out of alignment. The zero
   max-width with a full width is the table-cell ellipsis trick — it lets the
   cell take whatever room the other columns leave and clip to exactly that,
   instead of clipping at a guessed width or overflowing the panel. */
.field-comment {
  width: 100%;
  max-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--p-text-muted-color);
}

.table-comment {
  margin: -0.2rem 0 0.6rem;
  max-width: 80ch;
  font-size: 0.82rem;
  color: var(--p-text-muted-color);
}

.not-null {
  white-space: nowrap;
  font-size: 0.65rem;
  letter-spacing: 0.04em;
  color: var(--p-text-muted-color);
}

/* A column a relation joins on is the reason this table is in the graph. */
.fields tr.joined .field-name {
  font-weight: 700;
  color: var(--p-primary-color);
}

.indexes {
  display: grid;
  gap: 0.3rem;
  margin: 0;
  padding: 0;
  list-style: none;
}

.indexes li {
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
  .split,
  .split.tables-hidden {
    grid-template-columns: 1fr;
  }

  /* One column wide, the rail is a bar across the top, so the label lies flat. */
  .rail-label {
    writing-mode: horizontal-tb;
    padding: 0.4rem 0.6rem;
    text-align: left;
  }
}
</style>
