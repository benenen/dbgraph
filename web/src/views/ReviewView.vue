<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import Button from "primevue/button";
import Message from "primevue/message";
import ProgressSpinner from "primevue/progressspinner";
import Tag from "primevue/tag";
import Textarea from "primevue/textarea";
import { useToast } from "primevue/usetoast";

import { api, UnauthenticatedError, type Relation, type Revision } from "@/api/client";
import { conditionNodeIds, describeCondition } from "@/lib/conditions";

const toast = useToast();

const proposals = ref<Relation[]>([]);
const loading = ref(true);
const failure = ref("");
const deciding = ref("");
// A reason per proposal: a reviewer is recording why this one goes through,
// and the field must not carry over to the next.
const reasons = ref<Record<string, string>>({});

// Proposals name their endpoints by id. A reviewer reads table and column
// names, so ids are resolved once and reused across every proposal.
const nodeNames = ref<Record<string, string>>({});

async function load(): Promise<void> {
  loading.value = true;
  failure.value = "";
  try {
    const listed = await api.listProposals();
    proposals.value = listed.relations;
    await resolveNames(listed.relations);
  } catch (error) {
    if (error instanceof UnauthenticatedError) return;
    failure.value = error instanceof Error ? error.message : "Could not load proposals.";
  } finally {
    loading.value = false;
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
  for (const condition of [revision.guard, revision.selector, revision.transform]) {
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

const pending = computed(() => proposals.value.filter((relation) => relation.proposed));

async function decide(relation: Relation, decision: "APPROVE" | "REJECT"): Promise<void> {
  const revision = relation.proposed;
  if (!revision) return;
  const reason = (reasons.value[relation.id] ?? "").trim();
  if (!reason) {
    toast.add({
      severity: "warn",
      summary: "A reason is required",
      detail: "The decision is recorded in the audit log; say why.",
      life: 5000,
    });
    return;
  }
  deciding.value = relation.id;
  try {
    // expectedRevisionNo is the concurrency check: if someone revised this
    // proposal since the page loaded, the server refuses instead of
    // overwriting a decision made against different content.
    await api.reviewRelation(relation.id, {
      expectedRevisionNo: revision.revisionNo,
      decision,
      reason,
    });
    toast.add({
      severity: "success",
      summary: decision === "APPROVE" ? "Approved" : "Rejected",
      life: 3000,
    });
    reasons.value = { ...reasons.value, [relation.id]: "" };
    await load();
  } catch (error) {
    toast.add({
      severity: "error",
      summary: decision === "APPROVE" ? "Could not approve" : "Could not reject",
      detail: error instanceof Error ? error.message : "",
      life: 8000,
    });
  } finally {
    deciding.value = "";
  }
}

function evidenceLocation(file: string, startLine: number, endLine: number): string {
  const name = file.split("/").pop() ?? file;
  return startLine === endLine ? `${name}:${startLine}` : `${name}:${startLine}-${endLine}`;
}

onMounted(load);
</script>

<template>
  <header class="page-head">
    <div>
      <h1>Review</h1>
      <p>
        Relations an agent proposed from application source. Nothing reaches the graph until it is
        approved here, so read the evidence before deciding.
      </p>
    </div>
    <Button label="Refresh" icon="pi pi-refresh" severity="secondary" outlined @click="load" />
  </header>

  <Message v-if="failure" severity="error" :closable="false">{{ failure }}</Message>

  <div v-if="loading" class="loading"><ProgressSpinner style="width: 2rem; height: 2rem" /></div>

  <p v-else-if="!pending.length" class="empty">
    Nothing waiting. Approved relations show on the Relation graph; an agent proposes new ones over
    MCP.
  </p>

  <ul v-else class="proposals">
    <li v-for="relation in pending" :key="relation.id" class="proposal">
      <header class="proposal-head">
        <span class="endpoints">
          <code>{{ nodeName(revisionOf(relation)!.sourceNodeId) }}</code>
          <span class="arrow">→</span>
          <code>{{ nodeName(revisionOf(relation)!.targetNodeId) }}</code>
        </span>
        <span class="badges">
          <Tag :value="`revision ${revisionOf(relation)!.revisionNo}`" severity="secondary" />
          <Tag :value="`${Math.round(revisionOf(relation)!.confidence * 100)}% confident`" />
        </span>
      </header>

      <dl class="clauses">
        <template v-if="revisionOf(relation)!.guard">
          <dt>Guard</dt>
          <dd><code>{{ describe(revisionOf(relation)!.guard) }}</code></dd>
        </template>
        <template v-if="revisionOf(relation)!.selector">
          <dt>Selector</dt>
          <dd><code>{{ describe(revisionOf(relation)!.selector) }}</code></dd>
        </template>
        <dt>Transform</dt>
        <dd><code>{{ describe(revisionOf(relation)!.transform) }}</code></dd>
        <dt>Proposed by</dt>
        <dd>{{ revisionOf(relation)!.actor }}</dd>
        <dt>Reason</dt>
        <dd class="reason-given">{{ revisionOf(relation)!.reason }}</dd>
      </dl>

      <section class="evidence">
        <h3>Evidence</h3>
        <ul>
          <li v-for="(item, index) in revisionOf(relation)!.evidence" :key="index">
            <Tag :value="item.kind" severity="secondary" />
            <code :title="item.file">{{ evidenceLocation(item.file, item.startLine, item.endLine) }}</code>
            <span v-if="item.symbol" class="symbol">{{ item.symbol }}</span>
            <span class="commit">{{ item.commit.slice(0, 8) }}</span>
          </li>
        </ul>
      </section>

      <div class="decision">
        <Textarea
          v-model="reasons[relation.id]"
          rows="2"
          maxlength="2000"
          placeholder="Why you are approving or rejecting — recorded in the audit log"
          fluid
        />
        <div class="decision-actions">
          <Button
            label="Reject"
            severity="danger"
            outlined
            :loading="deciding === relation.id"
            @click="decide(relation, 'REJECT')"
          />
          <Button
            label="Approve"
            :loading="deciding === relation.id"
            @click="decide(relation, 'APPROVE')"
          />
        </div>
      </div>
    </li>
  </ul>
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
