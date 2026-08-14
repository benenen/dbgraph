<script setup lang="ts">
import {
  computed,
  nextTick,
  onBeforeUnmount,
  onMounted,
  ref,
  watch,
} from "vue";
import Button from "primevue/button";
import Message from "primevue/message";
import ProgressSpinner from "primevue/progressspinner";
import Tag from "primevue/tag";
import Textarea from "primevue/textarea";
import { useConfirm } from "primevue/useconfirm";
import { useToast } from "primevue/usetoast";

import {
  api,
  UnauthenticatedError,
  type ProposalKind,
  type Relation,
  type Revision,
} from "@/api/client";
import { conditionNodeIds, describeCondition } from "@/lib/conditions";
import {
  beginReviewOperation,
  cancelReviewOperation,
  finishReviewOperation,
  reviewOperationActive,
  reviewOperationCanContinue,
} from "@/lib/reviewOperation";

const toast = useToast();
const confirm = useConfirm();

const proposals = ref<Relation[]>([]);
const loading = ref(true);
const failure = ref("");
const deciding = ref("");
// The server answers a bounded page. Saying so matters here more than most
// places: a reviewer who cannot see that there are more will read an empty
// screen as an empty queue, and a bulk button that claimed "all" would be
// deciding a different set than the one on screen.
const truncated = ref(false);
const bulk = ref<{
  decision: "APPROVE" | "REJECT";
  done: number;
  total: number;
} | null>(null);
const bulkBar = ref<HTMLElement | null>(null);
const bulkStatus = ref<HTMLElement | null>(null);
const emptyState = ref<HTMLElement | null>(null);
const reviewHeading = ref<HTMLElement | null>(null);
const queueNeedsRefresh = ref(false);
// A reason per proposal: a reviewer is recording why this one goes through,
// and the field must not carry over to the next.
const reasons = ref<Record<string, string>>({});

// Proposals name their endpoints by id. A reviewer reads table and column
// names, so ids are resolved once and reused across every proposal.
const nodeNames = ref<Record<string, string>>({});
let loadGeneration = 0;
let viewActive = true;
let ownedOperation: symbol | null = null;
let ignoreNextSharedCompletion = false;

async function load(showLoading = true): Promise<boolean> {
  const generation = ++loadGeneration;
  if (showLoading) loading.value = true;
  failure.value = "";
  try {
    const listed = await api.listProposals();
    await resolveNames(listed.relations);
    if (generation !== loadGeneration) return false;
    const currentReasonKeys = new Set(listed.relations.map(proposalReasonKey));
    reasons.value = Object.fromEntries(
      Object.entries(reasons.value).filter(([key]) =>
        currentReasonKeys.has(key),
      ),
    );
    proposals.value = listed.relations;
    truncated.value = listed.truncated;
    queueNeedsRefresh.value = false;
    return true;
  } catch (error) {
    if (error instanceof UnauthenticatedError) return false;
    if (generation !== loadGeneration) return false;
    failure.value =
      error instanceof Error ? error.message : "Could not load proposals.";
    return false;
  } finally {
    if (showLoading && generation === loadGeneration) loading.value = false;
  }
}

async function resolveNames(relations: Relation[]): Promise<void> {
  const wanted = new Set<string>();
  for (const relation of relations) {
    const revision = relation.proposed ?? relation.active;
    if (!revision) continue;
    collectNodeIds(revision, wanted);
  }
  const missing = [...wanted].filter((id) => !(id in nodeNames.value));
  const resolved = await Promise.all(
    missing.map(async (id) => {
      try {
        const node = await api.node(id);
        return [id, node.qualifiedName] as const;
      } catch {
        // A name is a convenience; a proposal is still reviewable without it.
        return [id, id] as const;
      }
    }),
  );
  nodeNames.value = { ...nodeNames.value, ...Object.fromEntries(resolved) };
}

function collectNodeIds(revision: Revision, into: Set<string>): void {
  into.add(revision.sourceNodeId);
  into.add(revision.targetNodeId);
  for (const condition of [
    revision.guard,
    revision.selector,
    revision.transform,
  ]) {
    for (const id of conditionNodeIds(condition)) into.add(id);
  }
}

function nodeName(nodeId: string): string {
  return nodeNames.value[nodeId] ?? nodeId;
}

function describe(condition: Parameters<typeof describeCondition>[0]): string {
  return describeCondition(condition, nodeName);
}

function revisionOf(relation: Relation): Revision | null {
  return relation.proposed ?? relation.active;
}

const pending = computed(() =>
  proposals.value.filter((relation) => relation.proposed),
);
const mutationInProgress = computed(
  () =>
    reviewOperationActive.value || bulk.value !== null || deciding.value !== "",
);
const reconciling = ref(false);
const operationInProgress = computed(
  () =>
    mutationInProgress.value || reconciling.value || queueNeedsRefresh.value,
);
const refreshDisabled = computed(
  () => mutationInProgress.value || reconciling.value,
);

async function reconcileQueue(showLoading: boolean): Promise<void> {
  if (!viewActive || reconciling.value) return;
  reconciling.value = true;
  const refreshed = await load(showLoading || loading.value);
  if (viewActive && !refreshed) queueNeedsRefresh.value = true;
  reconciling.value = false;
}

function refresh(): void {
  if (refreshDisabled.value) return;
  void reconcileQueue(true);
}

function proposalReasonKey(relation: Relation): string {
  return `${relation.id}:${relation.proposed?.revisionNo ?? "none"}`;
}

function removeReviewedRelation(relation: Relation): void {
  proposals.value = proposals.value.filter(
    (candidate) => candidate.id !== relation.id,
  );
  const reviewedReasonKey = proposalReasonKey(relation);
  reasons.value = Object.fromEntries(
    Object.entries(reasons.value).filter(([key]) => key !== reviewedReasonKey),
  );
  queueNeedsRefresh.value = true;
}

function acquireReviewOperation(): symbol | null {
  const token = beginReviewOperation();
  if (token) ownedOperation = token;
  return token;
}

function releaseReviewOperation(token: symbol): void {
  if (ownedOperation === token) {
    ignoreNextSharedCompletion = true;
    ownedOperation = null;
  }
  finishReviewOperation(token);
  void nextTick().then(() => {
    ignoreNextSharedCompletion = false;
  });
}

async function focusReviewResult(): Promise<void> {
  await nextTick();
  const target = bulkBar.value ?? emptyState.value ?? reviewHeading.value;
  target?.focus({ preventScroll: true });
}

async function decide(
  relation: Relation,
  decision: "APPROVE" | "REJECT",
): Promise<void> {
  if (!viewActive || operationInProgress.value) return;
  const revision = relation.proposed;
  if (!revision) return;
  const operation = acquireReviewOperation();
  if (!operation) return;
  // Blank is allowed: the server records a stated default rather than an empty
  // audit row. Agreeing with a proposal that already explains itself does not
  // need a second essay.
  const reason = (reasons.value[proposalReasonKey(relation)] ?? "").trim();
  deciding.value = relation.id;
  let reviewed = false;
  try {
    // expectedRevisionNo is the concurrency check: if someone revised this
    // proposal since the page loaded, the server refuses instead of
    // overwriting a decision made against different content.
    await api.reviewRelation(relation.id, {
      expectedRevisionNo: revision.revisionNo,
      decision,
      reason,
    });
    if (!reviewOperationCanContinue(operation)) return;
    toast.add({
      severity: "success",
      summary: decision === "APPROVE" ? "Approved" : "Rejected",
      life: 3000,
    });
    removeReviewedRelation(relation);
    reviewed = true;
    const refreshed = await load(false);
    if (viewActive && !refreshed) queueNeedsRefresh.value = true;
  } catch (error) {
    if (!reviewOperationCanContinue(operation)) return;
    toast.add({
      severity: "error",
      summary:
        decision === "APPROVE" ? "Could not approve" : "Could not reject",
      detail: error instanceof Error ? error.message : "",
      life: 8000,
    });
  } finally {
    deciding.value = "";
    releaseReviewOperation(operation);
  }
  if (reviewed && viewActive) await focusReviewResult();
}

interface ProposalKindCounts {
  content: number;
  tombstone: number;
  stale: number;
}

function countProposalKinds(batch: Relation[]): ProposalKindCounts {
  return batch.reduce<ProposalKindCounts>(
    (counts, relation) => {
      switch (relation.proposed?.kind) {
        case "TOMBSTONE":
          return { ...counts, tombstone: counts.tombstone + 1 };
        case "STALE":
          return { ...counts, stale: counts.stale + 1 };
        default:
          return { ...counts, content: counts.content + 1 };
      }
    },
    { content: 0, tombstone: 0, stale: 0 },
  );
}

function pluralized(count: number, singular: string): string {
  return `${count} ${singular}${count === 1 ? "" : "s"}`;
}

function joinKindCounts(counts: ProposalKindCounts): string {
  const parts = [
    counts.content ? pluralized(counts.content, "content update") : "",
    counts.tombstone ? pluralized(counts.tombstone, "tombstone") : "",
    counts.stale ? pluralized(counts.stale, "stale candidate") : "",
  ].filter(Boolean);
  if (parts.length < 2) return parts[0] ?? "0 proposals";
  if (parts.length === 2) return `${parts[0]} and ${parts[1]}`;
  return `${parts.slice(0, -1).join(", ")}, and ${parts.at(-1)}`;
}

function proposalKindLabel(kind: ProposalKind): string {
  switch (kind) {
    case "TOMBSTONE":
      return "tombstone — removes relation";
    case "STALE":
      return "stale — removes relation";
    default:
      return "content update";
  }
}

function proposalKindSeverity(
  kind: ProposalKind,
): "danger" | "warn" | "secondary" {
  if (kind === "TOMBSTONE") return "danger";
  if (kind === "STALE") return "warn";
  return "secondary";
}

/**
 * Decides every proposal currently on screen, one call each.
 *
 * Sequential rather than concurrent: each decision carries the revision number
 * the reviewer saw, and firing fifty writes at once only makes a partial
 * failure harder to describe. A failure does not stop the run — the remaining
 * proposals are independent — but every one is counted and reported, because
 * "approved 48 of 50" and "approved 50" are different outcomes.
 */
function decideAll(decision: "APPROVE" | "REJECT"): void {
  if (!viewActive || operationInProgress.value) return;
  const batch = pending.value.slice();
  if (!batch.length) return;
  const counts = countProposalKinds(batch);
  const removals = counts.tombstone + counts.stale;
  const kinds = joinKindCounts(counts);
  const verb = decision === "APPROVE" ? "Approve" : "Reject";
  confirm.require({
    header:
      decision === "APPROVE" && removals
        ? `Approve ${batch.length} proposal${batch.length === 1 ? "" : "s"}, including ${removals} removal${removals === 1 ? "" : "s"}?`
        : `${verb} ${batch.length} proposal${batch.length === 1 ? "" : "s"}?`,
    message:
      decision === "APPROVE"
        ? `This approves ${kinds}. ` +
          (removals
            ? `Approving the ${removals} removal proposal${removals === 1 ? "" : "s"} removes their relation${removals === 1 ? "" : "s"} from the effective graph.`
            : `The ${pluralized(counts.content, "content update")} will be published to the effective graph.`) +
          (truncated.value
            ? " Only the proposals on this page are decided; more are waiting."
            : "")
        : `This rejects ${kinds}. The effective graph does not change.` +
          (truncated.value
            ? " Only the proposals on this page are decided; more are waiting."
            : ""),
    acceptLabel:
      decision === "APPROVE" && removals
        ? `Approve ${batch.length}, remove ${removals}`
        : `${verb} ${batch.length}`,
    rejectLabel: "Cancel",
    acceptProps:
      decision === "REJECT" || removals ? { severity: "danger" } : undefined,
    accept: () => {
      if (viewActive) void runBulk(decision, batch);
    },
  });
}

async function runBulk(
  decision: "APPROVE" | "REJECT",
  batch: Relation[],
): Promise<void> {
  if (!viewActive || operationInProgress.value) return;
  const operation = acquireReviewOperation();
  if (!operation) return;
  let completed = false;
  try {
    bulk.value = { decision, done: 0, total: batch.length };
    await nextTick();
    bulkStatus.value?.focus({ preventScroll: true });
    let succeeded = 0;
    const failures: string[] = [];
    for (const relation of batch) {
      if (!reviewOperationCanContinue(operation)) return;
      const revision = relation.proposed;
      if (!revision) continue;
      try {
        await api.reviewRelation(relation.id, {
          expectedRevisionNo: revision.revisionNo,
          decision,
          // A typed reason still wins; otherwise the audit row says this was a
          // bulk decision rather than borrowing the proposer's words.
          reason:
            (reasons.value[proposalReasonKey(relation)] ?? "").trim() ||
            `${decision === "APPROVE" ? "Approved" : "Rejected"} in bulk from the review queue`,
        });
        if (!reviewOperationCanContinue(operation)) return;
        succeeded += 1;
        removeReviewedRelation(relation);
      } catch (error) {
        if (!reviewOperationCanContinue(operation)) return;
        if (error instanceof UnauthenticatedError) return;
        failures.push(
          `${endpointLabel(relation)}: ${error instanceof Error ? error.message : "failed"}`,
        );
      }
      bulk.value = {
        decision,
        done: succeeded + failures.length,
        total: batch.length,
      };
    }
    if (!reviewOperationCanContinue(operation)) return;
    toast.add({
      severity: failures.length ? "warn" : "success",
      summary: failures.length
        ? `${succeeded} of ${batch.length} ${decision === "APPROVE" ? "approved" : "rejected"}`
        : `${succeeded} ${decision === "APPROVE" ? "approved" : "rejected"}`,
      detail: failures.length ? failures.slice(0, 3).join("; ") : undefined,
      life: failures.length ? 12000 : 4000,
    });
    const refreshed = await load(false);
    if (viewActive && !refreshed) queueNeedsRefresh.value = true;
    if (!reviewOperationCanContinue(operation)) return;
    completed = true;
  } finally {
    bulk.value = null;
    releaseReviewOperation(operation);
  }
  if (completed && viewActive) await focusReviewResult();
}

/** Names a proposal by its two ends, for a failure line that says which one. */
function endpointLabel(relation: Relation): string {
  const revision = revisionOf(relation);
  if (!revision) return relation.id;
  return `${nodeName(revision.sourceNodeId)} → ${nodeName(revision.targetNodeId)}`;
}

function evidenceLocation(
  file: string,
  startLine: number,
  endLine: number,
): string {
  const name = file.split("/").pop() ?? file;
  return startLine === endLine
    ? `${name}:${startLine}`
    : `${name}:${startLine}-${endLine}`;
}

watch(reviewOperationActive, (active, previous) => {
  if (previous && !active && !ignoreNextSharedCompletion && viewActive) {
    void reconcileQueue(false);
  }
});

onMounted(load);
onBeforeUnmount(() => {
  viewActive = false;
  loadGeneration += 1;
  confirm.close();
  if (ownedOperation) cancelReviewOperation(ownedOperation);
});
</script>

<template>
  <header class="page-head">
    <div>
      <h1 ref="reviewHeading" tabindex="-1">Review</h1>
      <p>
        Relations an agent proposed from application source. Nothing reaches the
        graph until it is approved here, so read the evidence before deciding.
      </p>
    </div>
    <Button
      label="Refresh"
      icon="pi pi-refresh"
      severity="secondary"
      outlined
      :disabled="refreshDisabled"
      @click="refresh"
    />
  </header>

  <Message v-if="failure" severity="error" :closable="false">{{
    failure
  }}</Message>

  <Message
    v-if="reviewOperationActive && !bulk && !deciding"
    severity="info"
    :closable="false"
  >
    A review operation is finishing. Controls will unlock after the current
    request completes.
  </Message>

  <Message
    v-else-if="reconciling"
    severity="info"
    :closable="false"
    role="status"
  >
    Refreshing the review queue before controls are unlocked.
  </Message>

  <Message
    v-else-if="queueNeedsRefresh && failure"
    severity="warn"
    :closable="false"
  >
    Queue state could not be confirmed. Refresh successfully before making
    another decision.
  </Message>

  <div v-if="loading" class="loading">
    <ProgressSpinner style="width: 2rem; height: 2rem" />
  </div>

  <p
    v-else-if="!pending.length && !bulk"
    ref="emptyState"
    class="empty"
    tabindex="-1"
  >
    <template v-if="queueNeedsRefresh || truncated">
      This page is complete. Refresh to check whether more proposals are
      waiting.
    </template>
    <template v-else>
      Nothing waiting. Approved relations show on the Relation graph; an agent
      proposes new ones over MCP.
    </template>
  </p>

  <template v-else>
    <!-- The count is on the buttons, not just beside them: a bulk action has to
         say how many it is about to decide, especially when the page is one
         bounded slice of a longer queue. -->
    <div ref="bulkBar" class="bulk-bar" tabindex="-1">
      <span v-if="bulk" class="bulk-count">
        {{ bulk.total }} in this batch
      </span>
      <span v-else class="bulk-count">
        {{ pending.length }} waiting<template v-if="truncated">
          on this page — decide these to see the rest</template
        >
      </span>
      <div
        v-if="bulk"
        ref="bulkStatus"
        class="bulk-progress"
        role="status"
        aria-live="polite"
        aria-atomic="true"
        tabindex="-1"
      >
        <ProgressSpinner
          style="width: 1.1rem; height: 1.1rem"
          stroke-width="6"
        />
        {{ bulk.decision === "APPROVE" ? "Approving" : "Rejecting" }}
        {{ bulk.done }} /
        {{ bulk.total }}
      </div>
      <div v-else class="bulk-actions">
        <Button
          :label="`Reject all ${pending.length}`"
          severity="danger"
          outlined
          size="small"
          :disabled="operationInProgress"
          @click="decideAll('REJECT')"
        />
        <Button
          :label="`Approve all ${pending.length}`"
          size="small"
          :disabled="operationInProgress"
          @click="decideAll('APPROVE')"
        />
      </div>
    </div>

    <ul class="proposals">
      <li v-for="relation in pending" :key="relation.id" class="proposal">
        <header class="proposal-head">
          <span class="endpoints">
            <code>{{ nodeName(revisionOf(relation)!.sourceNodeId) }}</code>
            <span class="arrow">→</span>
            <code>{{ nodeName(revisionOf(relation)!.targetNodeId) }}</code>
          </span>
          <span class="badges">
            <Tag
              :value="proposalKindLabel(revisionOf(relation)!.kind)"
              :severity="proposalKindSeverity(revisionOf(relation)!.kind)"
            />
            <Tag
              :value="`revision ${revisionOf(relation)!.revisionNo}`"
              severity="secondary"
            />
            <Tag
              :value="`${Math.round(revisionOf(relation)!.confidence * 100)}% confident`"
            />
          </span>
        </header>

        <dl class="clauses">
          <template v-if="revisionOf(relation)!.guard">
            <dt>Guard</dt>
            <dd>
              <code>{{ describe(revisionOf(relation)!.guard) }}</code>
            </dd>
          </template>
          <template v-if="revisionOf(relation)!.selector">
            <dt>Selector</dt>
            <dd>
              <code>{{ describe(revisionOf(relation)!.selector) }}</code>
            </dd>
          </template>
          <dt>Transform</dt>
          <dd>
            <code>{{ describe(revisionOf(relation)!.transform) }}</code>
          </dd>
          <dt>Proposed by</dt>
          <dd>{{ revisionOf(relation)!.actor }}</dd>
          <dt>Reason</dt>
          <dd class="reason-given">{{ revisionOf(relation)!.reason }}</dd>
        </dl>

        <section class="evidence">
          <h3>Evidence</h3>
          <ul>
            <li
              v-for="(item, index) in revisionOf(relation)!.evidence"
              :key="index"
            >
              <Tag :value="item.kind" severity="secondary" />
              <code :title="item.file">{{
                evidenceLocation(item.file, item.startLine, item.endLine)
              }}</code>
              <span v-if="item.symbol" class="symbol">{{ item.symbol }}</span>
              <span class="commit">{{ item.commit.slice(0, 8) }}</span>
            </li>
          </ul>
        </section>

        <div class="decision">
          <Textarea
            v-model="reasons[proposalReasonKey(relation)]"
            rows="2"
            maxlength="2000"
            placeholder="Optional — add a reason only if yours differs from the evidence above"
            fluid
            :disabled="operationInProgress"
          />
          <div class="decision-actions">
            <Button
              label="Reject"
              severity="danger"
              outlined
              :loading="deciding === relation.id"
              :disabled="operationInProgress"
              @click="decide(relation, 'REJECT')"
            />
            <Button
              label="Approve"
              :loading="deciding === relation.id"
              :disabled="operationInProgress"
              @click="decide(relation, 'APPROVE')"
            />
          </div>
        </div>
      </li>
    </ul>
  </template>
</template>

<style scoped>
.bulk-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 1rem;
  margin-bottom: 0.9rem;
  padding: 0.5rem 0.75rem;
  border: 1px solid var(--p-content-border-color);
  border-radius: 8px;
}

.bulk-count {
  font-size: 0.85rem;
  color: var(--p-text-muted-color);
}

.bulk-actions {
  display: flex;
  gap: 0.5rem;
}

/* Replaces the buttons while a run is going, so there is nothing to click
   twice and the count says how far it has got. */
.bulk-progress {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  font-size: 0.85rem;
  color: var(--p-text-muted-color);
}

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

.loading {
  display: grid;
  place-items: center;
  padding: 3rem;
}

.empty {
  padding: 2.5rem 0;
  color: var(--p-text-muted-color);
}

.proposals {
  display: grid;
  gap: 0.85rem;
  margin: 0;
  padding: 0;
  list-style: none;
}

.proposal {
  padding: 0.9rem 1rem;
  border: 1px solid var(--p-content-border-color);
  border-radius: 8px;
}

.proposal-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: 0.6rem;
  margin-bottom: 0.7rem;
}

.endpoints {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  font-size: 0.9rem;
}

.arrow {
  color: var(--p-text-muted-color);
}

.badges {
  display: flex;
  gap: 0.4rem;
}

.clauses {
  display: grid;
  grid-template-columns: 7rem 1fr;
  gap: 0.25rem 0.75rem;
  margin: 0 0 0.8rem;
  font-size: 0.82rem;
}

.clauses dt {
  color: var(--p-text-muted-color);
}

.clauses dd {
  margin: 0;
}

.reason-given {
  max-width: 80ch;
}

.evidence h3 {
  margin: 0 0 0.35rem;
  font-size: 0.7rem;
  font-weight: 600;
  letter-spacing: 0.06em;
  text-transform: uppercase;
  color: var(--p-text-muted-color);
}

.evidence ul {
  display: grid;
  gap: 0.25rem;
  margin: 0 0 0.85rem;
  padding: 0;
  list-style: none;
}

.evidence li {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 0.45rem;
  font-size: 0.78rem;
}

.symbol {
  color: var(--p-text-muted-color);
}

.commit {
  font-family: var(--font-mono, ui-monospace, monospace);
  font-size: 0.72rem;
  color: var(--p-text-muted-color);
}

.decision {
  display: grid;
  gap: 0.5rem;
  padding-top: 0.7rem;
  border-top: 1px solid var(--p-content-border-color);
}

.decision-actions {
  display: flex;
  justify-content: flex-end;
  gap: 0.5rem;
}

code {
  font-size: 0.8rem;
}
</style>
