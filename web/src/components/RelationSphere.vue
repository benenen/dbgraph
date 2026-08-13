<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import Button from "primevue/button";
import Tag from "primevue/tag";
import ForceGraph3D, {
  type ForceGraph3DInstance,
  type LinkObject,
  type NodeObject,
} from "3d-force-graph";
import {
  CanvasTexture,
  LinearFilter,
  Object3D,
  Sprite,
  SpriteMaterial,
  SRGBColorSpace,
} from "three";

import type { RelationEdge, RelationGraph, TableSummary } from "@/api/client";
import {
  distributeOnSphere,
  selectPersistentLabelIds,
  truncateGraphLabel,
} from "@/lib/sphericalGraph";

const props = defineProps<{
  graph: RelationGraph;
  focusedTableId: string;
  selectedEdgeId: string;
  viewKey: string;
}>();

const emit = defineEmits<{
  selectTable: [table: TableSummary];
  selectEdge: [edge: RelationEdge];
}>();

interface GraphNode extends NodeObject {
  id: string;
  table: TableSummary;
  degree: number;
  fx: number;
  fy: number;
  fz: number;
}

type GraphLink = LinkObject<GraphNode> & {
  source: string | GraphNode;
  target: string | GraphNode;
  edge: RelationEdge;
};

interface OrbitControls {
  addEventListener(type: "change", listener: () => void): void;
  removeEventListener(type: "change", listener: () => void): void;
}

const GRAPH_RADIUS = 190;
const MIN_CAMERA_DISTANCE = 120;
const MAX_CAMERA_DISTANCE = 1200;
const MAX_PERSISTENT_LABELS = 80;
const MAX_LABEL_CANVAS_WIDTH = 840;

const host = ref<HTMLElement | null>(null);
const fallback = ref<HTMLElement | null>(null);
const zoomPercent = ref(100);
const horizontalDegrees = ref(0);
const verticalDegrees = ref(0);
const hoveredNodeId = ref("");
const hoveredLinkId = ref("");
const graphFailure = ref("");
const accessibleStatus = ref("");

let instance: ForceGraph3DInstance<GraphNode, GraphLink> | null = null;
let controls: OrbitControls | null = null;
let resizeObserver: ResizeObserver | null = null;
let baselineCameraDistance = 0;
let statusAnnouncementTimer = 0;
let adjacentTableIds = new Map<string, ReadonlySet<string>>();
let edgesById = new Map<string, RelationEdge>();
let persistentLabelIds = new Set<string>();

function buildGraphData(): {
  nodes: GraphNode[];
  links: GraphLink[];
  adjacency: Map<string, ReadonlySet<string>>;
  edgeIndex: Map<string, RelationEdge>;
} {
  const points = distributeOnSphere(props.graph.tables.map((table) => table.id));
  const degrees = new Map<string, number>();
  const neighbors = new Map<string, Set<string>>();
  for (const edge of props.graph.edges) {
    degrees.set(edge.sourceTableId, (degrees.get(edge.sourceTableId) ?? 0) + 1);
    degrees.set(edge.targetTableId, (degrees.get(edge.targetTableId) ?? 0) + 1);
    neighbors.set(edge.sourceTableId, new Set(neighbors.get(edge.sourceTableId)).add(edge.targetTableId));
    neighbors.set(edge.targetTableId, new Set(neighbors.get(edge.targetTableId)).add(edge.sourceTableId));
  }

  return {
    nodes: props.graph.tables.map((table) => {
      const point = points[table.id] ?? { x: 0, y: 0, z: 1 };
      return {
        id: table.id,
        table,
        degree: degrees.get(table.id) ?? 0,
        fx: point.x * GRAPH_RADIUS,
        fy: point.y * GRAPH_RADIUS,
        fz: point.z * GRAPH_RADIUS,
      };
    }),
    links: props.graph.edges.map((edge) => ({
      source: edge.sourceTableId,
      target: edge.targetTableId,
      edge,
    })),
    adjacency: new Map(neighbors),
    edgeIndex: new Map(props.graph.edges.map((edge) => [edge.relationId, edge])),
  };
}

function configureGraph(graph: ForceGraph3DInstance<GraphNode, GraphLink>): void {
  graph
    .showNavInfo(false)
    .backgroundColor("#071019")
    .nodeRelSize(4.4)
    .nodeResolution(16)
    .nodeVal((node) => 1 + Math.min(node.degree, 12) * 0.12)
    .nodeColor(nodeColor)
    .nodeOpacity(0.92)
    .nodeLabel((node) => tooltip(node.table.qualifiedName))
    .nodeThreeObject((node) => persistentLabel(node))
    .nodeThreeObjectExtend(true)
    .enableNodeDrag(false)
    .linkColor(linkColor)
    .linkOpacity(0.58)
    .linkWidth(linkWidth)
    .linkCurvature((link) => stableCurve(link.edge.relationId))
    .linkDirectionalArrowLength((link) => (link.edge.relationId === props.selectedEdgeId ? 7 : 5))
    .linkDirectionalArrowRelPos(0.88)
    .linkDirectionalArrowColor(linkColor)
    .linkLabel((link) => tooltip(edgeDescription(link.edge)))
    .linkHoverPrecision(7)
    .onNodeClick((node) => emit("selectTable", node.table))
    .onLinkClick((link) => emit("selectEdge", link.edge))
    .onNodeHover((node) => {
      hoveredNodeId.value = node?.id ?? "";
      restyleGraph(false);
    })
    .onLinkHover((link) => {
      hoveredLinkId.value = link?.edge.relationId ?? "";
      restyleGraph(false);
    })
    .cooldownTicks(1)
    .warmupTicks(0);
}

function installGraph(): void {
  if (!host.value) return;
  try {
    const graph = new ForceGraph3D(host.value, {
      controlType: "orbit",
      rendererConfig: { antialias: true, alpha: false },
    }) as unknown as ForceGraph3DInstance<GraphNode, GraphLink>;
    instance = graph;
    configureGraph(graph);
    controls = graph.controls() as OrbitControls;
    controls.addEventListener("change", syncCameraStatus);
    resizeObserver = new ResizeObserver(resizeGraph);
    resizeObserver.observe(host.value);
    resizeGraph();
    renderData(true);
  } catch {
    graphFailure.value = "The 3D graph is unavailable in this browser. Use the table list to inspect relations.";
    if (controls) controls.removeEventListener("change", syncCameraStatus);
    resizeObserver?.disconnect();
    instance?._destructor();
    instance = null;
    controls = null;
    host.value.replaceChildren();
  }
}

function renderData(resetCamera: boolean): void {
  if (!instance) return;
  const model = buildGraphData();
  adjacentTableIds = model.adjacency;
  edgesById = model.edgeIndex;
  persistentLabelIds = new Set(
    selectPersistentLabelIds(
      model.nodes.map((node) => ({ id: node.id, name: node.table.name, degree: node.degree })),
      MAX_PERSISTENT_LABELS,
    ),
  );
  instance.graphData({ nodes: model.nodes, links: model.links });
  if (resetCamera) resetView();
}

/** Re-evaluates presentation accessors without flushing the Three.js scene. */
function restyleGraph(includeGeometry: boolean): void {
  if (!instance) return;
  instance
    .nodeColor(instance.nodeColor())
    .linkColor(instance.linkColor())
    .linkDirectionalArrowColor(instance.linkDirectionalArrowColor());
  if (includeGeometry) {
    instance
      .linkWidth(instance.linkWidth())
      .linkDirectionalArrowLength(instance.linkDirectionalArrowLength());
  }
}

async function resetView(): Promise<void> {
  if (!instance) return;
  zoomPercent.value = 100;
  horizontalDegrees.value = 0;
  verticalDegrees.value = 0;
  baselineCameraDistance = 520;
  instance.cameraPosition({ x: 0, y: 0, z: 520 }, { x: 0, y: 0, z: 0 }, 0);
  await nextTick();
  window.requestAnimationFrame(() => {
    if (!instance) return;
    baselineCameraDistance = instance.camera().position.length();
    syncCameraStatus();
  });
}

function changeZoom(factor: number): void {
  if (!instance) return;
  const camera = instance.camera();
  const currentDistance = camera.position.length();
  const targetDistance = clamp(currentDistance / factor, MIN_CAMERA_DISTANCE, MAX_CAMERA_DISTANCE);
  if (currentDistance === 0 || targetDistance === currentDistance) return;
  const scale = targetDistance / currentDistance;
  instance.cameraPosition(
    {
      x: camera.position.x * scale,
      y: camera.position.y * scale,
      z: camera.position.z * scale,
    },
    { x: 0, y: 0, z: 0 },
    180,
  );
  zoomPercent.value = Math.round((baselineCameraDistance / targetDistance) * 100);
}

function syncCameraStatus(): void {
  if (!instance) return;
  const position = instance.camera().position;
  const distance = position.length();
  if (!baselineCameraDistance) baselineCameraDistance = distance;
  if (distance > 0) {
    horizontalDegrees.value = Math.round((Math.atan2(position.x, position.z) * 180) / Math.PI);
    verticalDegrees.value = Math.round((Math.asin(position.y / distance) * 180) / Math.PI);
    zoomPercent.value = Math.round((baselineCameraDistance / distance) * 100);
    scheduleStatusAnnouncement();
  }
}

function scheduleStatusAnnouncement(): void {
  window.clearTimeout(statusAnnouncementTimer);
  statusAnnouncementTimer = window.setTimeout(() => {
    accessibleStatus.value = `${zoomPercent.value}% zoom, ${horizontalDegrees.value} degrees horizontal, ${verticalDegrees.value} degrees vertical`;
  }, 300);
}

function resizeGraph(): void {
  if (!host.value || !instance) return;
  const width = Math.max(320, host.value.clientWidth);
  const height = Math.max(420, Math.min(window.innerHeight * 0.68, 720));
  instance.width(width).height(height);
}

function nodeColor(node: GraphNode): string {
  if (node.table.id === props.focusedTableId) return "#f7c873";
  const hoveredEdge = edgesById.get(hoveredLinkId.value);
  if (hoveredEdge && edgeTouches(hoveredEdge, node.table.id)) return "#8af4df";
  if (node.table.id === hoveredNodeId.value || tablesConnected(node.table.id, hoveredNodeId.value)) {
    return "#8af4df";
  }
  if (props.focusedTableId && !tablesConnected(node.table.id, props.focusedTableId)) return "#334a5e";
  return "#62d6c2";
}

function linkColor(link: GraphLink): string {
  if (link.edge.relationId === props.selectedEdgeId) return "#f7c873";
  if (
    link.edge.relationId === hoveredLinkId.value ||
    (hoveredNodeId.value && edgeTouches(link.edge, hoveredNodeId.value))
  ) {
    return "#75e4d3";
  }
  if (props.focusedTableId && !edgeTouches(link.edge, props.focusedTableId)) return "#283b4c";
  return link.edge.conditional ? "#aa91d6" : "#718fa8";
}

function linkWidth(link: GraphLink): number {
  if (link.edge.relationId === props.selectedEdgeId) return 2.7;
  if (props.focusedTableId && edgeTouches(link.edge, props.focusedTableId)) return 1.8;
  return 0.9;
}

function onHostKeydown(event: KeyboardEvent): void {
  const rotationStep = Math.PI / 36;
  const actions: Partial<Record<string, () => void>> = {
    ArrowLeft: () => orbitCamera(-rotationStep, 0),
    ArrowRight: () => orbitCamera(rotationStep, 0),
    ArrowUp: () => orbitCamera(0, rotationStep),
    ArrowDown: () => orbitCamera(0, -rotationStep),
    "+": () => changeZoom(1.25),
    "=": () => changeZoom(1.25),
    "-": () => changeZoom(1 / 1.25),
    "0": () => void resetView(),
  };
  const action = actions[event.key];
  if (!action) return;
  event.preventDefault();
  action();
}

function focusGraph(): void {
  (graphFailure.value ? fallback.value : host.value)?.focus();
}

defineExpose({ focusGraph });

function orbitCamera(yawDelta: number, pitchDelta: number): void {
  if (!instance) return;
  const position = instance.camera().position;
  const distance = position.length();
  const yaw = Math.atan2(position.x, position.z) + yawDelta;
  const pitch = clamp(
    Math.asin(position.y / distance) + pitchDelta,
    -Math.PI / 2 + 0.08,
    Math.PI / 2 - 0.08,
  );
  const horizontalDistance = Math.cos(pitch) * distance;
  instance.cameraPosition(
    {
      x: Math.sin(yaw) * horizontalDistance,
      y: Math.sin(pitch) * distance,
      z: Math.cos(yaw) * horizontalDistance,
    },
    { x: 0, y: 0, z: 0 },
    140,
  );
}

function tablesConnected(first: string, second: string): boolean {
  if (first === second) return true;
  return adjacentTableIds.get(first)?.has(second) ?? false;
}

function edgeTouches(edge: RelationEdge, tableId: string): boolean {
  return !tableId || edge.sourceTableId === tableId || edge.targetTableId === tableId;
}

function edgeDescription(edge: RelationEdge): string {
  const source = tableName(edge.sourceTableId);
  const target = tableName(edge.targetTableId);
  const condition = edge.conditional ? " · conditional" : "";
  return `${source}.${edge.sourceColumn} → ${target}.${edge.targetColumn}${condition}`;
}

function tableName(tableId: string): string {
  return props.graph.tables.find((table) => table.id === tableId)?.name ?? tableId;
}

function tooltip(text: string): HTMLElement {
  const element = document.createElement("span");
  element.className = "graph-tooltip";
  element.textContent = text;
  return element;
}

function persistentLabel(node: GraphNode): Object3D {
  return persistentLabelIds.has(node.id) ? tableLabel(node.table.name) : new Object3D();
}

/** A canvas-backed Three.js sprite keeps labels readable without HTML injection. */
function tableLabel(text: string): Sprite {
  const canvas = document.createElement("canvas");
  const context = canvas.getContext("2d");
  if (!context) return new Sprite();

  const label = truncateGraphLabel(text);
  context.font = "600 28px ui-monospace, SFMono-Regular, Menlo, monospace";
  const width = Math.min(MAX_LABEL_CANVAS_WIDTH, Math.ceil(context.measureText(label).width) + 28);
  canvas.width = width;
  canvas.height = 54;
  context.font = "600 28px ui-monospace, SFMono-Regular, Menlo, monospace";
  context.textAlign = "center";
  context.textBaseline = "middle";
  context.lineWidth = 7;
  context.strokeStyle = "rgba(7, 16, 25, 0.94)";
  context.strokeText(label, width / 2, 27);
  context.fillStyle = "#e6edf5";
  context.fillText(label, width / 2, 27);

  const texture = new CanvasTexture(canvas);
  texture.colorSpace = SRGBColorSpace;
  texture.minFilter = LinearFilter;
  const sprite = new Sprite(
    new SpriteMaterial({ map: texture, transparent: true, depthWrite: false }),
  );
  sprite.position.set(0, 13, 0);
  sprite.scale.set(width / 6, 9, 1);
  return sprite;
}

function stableCurve(relationId: string): number {
  let hash = 0;
  for (const character of relationId) hash = (hash * 31 + character.charCodeAt(0)) | 0;
  return (hash % 2 === 0 ? 1 : -1) * 0.08;
}

function clamp(value: number, low: number, high: number): number {
  return Math.min(high, Math.max(low, value));
}

watch(
  () => props.graph,
  () => renderData(false),
  { deep: false },
);
watch(
  () => props.viewKey,
  () => void resetView(),
);
watch(
  () => [props.focusedTableId, props.selectedEdgeId],
  () => restyleGraph(true),
);
watch(
  () => props.selectedEdgeId,
  (relationId) => {
    if (!relationId) return;
    const edge = props.graph.edges.find((candidate) => candidate.relationId === relationId);
    if (edge) accessibleStatus.value = `Selected relation ${edgeDescription(edge)}.`;
  },
);

onMounted(installGraph);
onBeforeUnmount(() => {
  if (controls) controls.removeEventListener("change", syncCameraStatus);
  window.clearTimeout(statusAnnouncementTimer);
  resizeObserver?.disconnect();
  instance?._destructor();
  instance = null;
  controls = null;
});
</script>

<template>
  <div class="sphere-head">
    <div class="legend" aria-label="Graph legend">
      <span><i class="swatch solid" /> approved</span>
      <span><i class="swatch conditional" /> conditional</span>
      <span id="sphere-instructions" class="interaction-hint">
        Drag to rotate · Scroll to zoom · Arrow keys rotate · +/- zoom · 0 resets
      </span>
    </div>
    <div class="sphere-actions">
      <span data-testid="graph-view-status" class="view-status">
        {{ zoomPercent }}% zoom · {{ horizontalDegrees }}° horizontal · {{ verticalDegrees }}° vertical
      </span>
      <span
        class="sr-only"
        role="status"
        aria-live="polite"
        aria-atomic="true"
        data-testid="graph-accessible-status"
      >{{ accessibleStatus }}</span>
      <Tag v-if="graph.truncated" value="truncated" severity="warn" />
      <Button icon="pi pi-search-minus" text size="small" aria-label="Zoom out" @click="changeZoom(1 / 1.25)" />
      <Button icon="pi pi-search-plus" text size="small" aria-label="Zoom in" @click="changeZoom(1.25)" />
      <Button label="Reset view" text size="small" aria-label="Reset graph view" @click="resetView" />
    </div>
  </div>

  <p v-if="graphFailure" ref="fallback" class="graph-fallback" role="alert" tabindex="-1">
    {{ graphFailure }}
  </p>
  <div
    ref="host"
    class="sphere"
    role="application"
    aria-roledescription="3D relation graph"
    :aria-label="`Spherical relation graph with ${graph.tables.length} tables and ${graph.edges.length} relations.${selectedEdgeId ? ` Selected relation ${selectedEdgeId}.` : ''} Drag to rotate, scroll to zoom, or use the keyboard.`"
    aria-describedby="sphere-instructions"
    tabindex="0"
    :hidden="Boolean(graphFailure)"
    @keydown="onHostKeydown"
  />
</template>

<style scoped>
.sphere-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 1rem;
  min-height: 2.8rem;
  padding: 0.35rem 0.6rem;
  border-bottom: 1px solid var(--p-content-border-color);
}

.legend,
.sphere-actions {
  display: flex;
  align-items: center;
}

.legend {
  gap: 1rem;
  color: var(--p-text-muted-color);
  font-size: 0.75rem;
}

.legend span {
  display: flex;
  align-items: center;
  gap: 0.35rem;
}

.swatch {
  width: 18px;
  height: 2px;
  background: #718fa8;
}

.swatch.conditional {
  background: #aa91d6;
}

.interaction-hint {
  padding-left: 1rem;
  border-left: 1px solid var(--p-content-border-color);
  letter-spacing: 0.01em;
}

.sphere-actions {
  gap: 0.2rem;
}

.view-status {
  margin-right: 0.35rem;
  color: var(--p-text-muted-color);
  font-family: var(--font-mono, ui-monospace, monospace);
  font-size: 0.7rem;
  white-space: nowrap;
}

.sphere {
  position: relative;
  min-height: 420px;
  overflow: hidden;
  background: #071019;
  isolation: isolate;
}

.sphere :deep(canvas) {
  display: block;
  outline: none;
}

.sphere:focus-visible {
  box-shadow: inset 0 0 0 2px var(--p-primary-color);
}

.sphere :deep(.scene-nav-info) {
  display: none;
}

.sphere :deep(.graph-tooltip) {
  max-width: 30rem;
  color: #e6edf5;
  font-family: var(--font-mono, ui-monospace, monospace);
  font-size: 0.78rem;
}

.graph-fallback {
  min-height: 420px;
  margin: 0;
  padding: 2rem;
  color: var(--p-text-muted-color);
  background: #071019;
}

.sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  padding: 0;
  overflow: hidden;
  clip: rect(0, 0, 0, 0);
  white-space: nowrap;
  border: 0;
}

@media (max-width: 820px) {
  .sphere-head {
    align-items: flex-start;
    flex-direction: column;
  }

  .legend {
    align-items: flex-start;
    flex-wrap: wrap;
  }

  .interaction-hint {
    width: 100%;
    padding-left: 0;
    border-left: 0;
  }

  .sphere-actions {
    width: 100%;
    justify-content: flex-end;
  }

  .view-status {
    margin-right: auto;
  }
}
</style>
