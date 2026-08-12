<script setup lang="ts">
import { onMounted, onUnmounted, ref } from "vue";
import { useRouter } from "vue-router";
import Button from "primevue/button";
import Column from "primevue/column";
import DataTable from "primevue/datatable";
import Dialog from "primevue/dialog";
import InputText from "primevue/inputtext";
import Message from "primevue/message";
import Password from "primevue/password";
import ProgressSpinner from "primevue/progressspinner";
import Tag from "primevue/tag";
import Textarea from "primevue/textarea";
import { useToast } from "primevue/usetoast";

import { api, UnauthenticatedError, type DataSource, type Project } from "@/api/client";

const props = defineProps<{ projectId: string }>();

const router = useRouter();
const toast = useToast();

const project = ref<Project | null>(null);
const sources = ref<DataSource[]>([]);
const loading = ref(true);
const failure = ref("");

const dialogOpen = ref(false);
const saving = ref(false);
const draft = ref({ name: "", dsnEnvironment: "", dsn: "", reason: "" });

/** Scan status per data source id, so a row can report its own progress. */
const scanStatus = ref<Record<string, string>>({});
const scanning = ref<Record<string, boolean>>({});
let timers: number[] = [];

const SCAN_POLL_MS = 5000;
const SCAN_POLL_LIMIT = 60;
const TERMINAL = new Set(["SUCCEEDED", "FAILED", "CANCELLED"]);

async function load(): Promise<void> {
  loading.value = true;
  failure.value = "";
  try {
    const projects = await api.listProjects();
    project.value = projects.find((candidate: Project) => candidate.id === props.projectId) ?? null;
    sources.value = await api.listDataSources(props.projectId);
  } catch (error) {
    if (error instanceof UnauthenticatedError) {
      await router.replace({ name: "login" });
      return;
    }
    failure.value = error instanceof Error ? error.message : "Could not load data sources.";
  } finally {
    loading.value = false;
  }
}

function openDialog(): void {
  draft.value = { name: "", dsnEnvironment: "", dsn: "", reason: "" };
  dialogOpen.value = true;
}

async function create(): Promise<void> {
  saving.value = true;
  try {
    const source = await api.createDataSource(props.projectId, draft.value);
    // The connection string is write-only; drop it as soon as it is sent.
    draft.value.dsn = "";
    dialogOpen.value = false;
    toast.add({ severity: "success", summary: `Data source ${source.name} created`, life: 4000 });
    await load();
  } catch (error) {
    toast.add({
      severity: "error",
      summary: "Could not create the data source",
      detail: error instanceof Error ? error.message : "",
      life: 6000,
    });
  } finally {
    saving.value = false;
  }
}

async function runScan(source: DataSource): Promise<void> {
  scanning.value = { ...scanning.value, [source.id]: true };
  scanStatus.value = { ...scanStatus.value, [source.id]: "Queueing…" };
  try {
    const job = await api.startScan(props.projectId, source.id, "Full inventory from the console");
    await poll(source.id, job.id);
  } catch (error) {
    scanStatus.value = {
      ...scanStatus.value,
      [source.id]: error instanceof Error ? error.message : "Scan failed to start.",
    };
  } finally {
    scanning.value = { ...scanning.value, [source.id]: false };
  }
}

function wait(ms: number): Promise<void> {
  return new Promise((resolve) => {
    timers.push(window.setTimeout(resolve, ms));
  });
}

async function poll(sourceId: string, jobId: string): Promise<void> {
  for (let attempt = 0; attempt < SCAN_POLL_LIMIT; attempt += 1) {
    try {
      const job = await api.job(props.projectId, jobId);
      const detail = job.errorCode ? ` · ${job.errorCode}` : "";
      scanStatus.value = { ...scanStatus.value, [sourceId]: `${job.status}${detail}` };
      if (TERMINAL.has(job.status)) return;
    } catch (error) {
      scanStatus.value = {
        ...scanStatus.value,
        [sourceId]: error instanceof Error ? error.message : "Checking again…",
      };
    }
    await wait(SCAN_POLL_MS);
  }
  scanStatus.value = { ...scanStatus.value, [sourceId]: "Still running. Reload to check again." };
}

function statusSeverity(status: string): "success" | "danger" | "warn" | "info" {
  if (status.startsWith("SUCCEEDED")) return "success";
  if (status.startsWith("FAILED") || status.startsWith("CANCELLED")) return "danger";
  if (status.startsWith("RUNNING") || status.startsWith("PENDING")) return "warn";
  return "info";
}

onMounted(load);
onUnmounted(() => {
  timers.forEach((timer) => window.clearTimeout(timer));
  timers = [];
});
</script>

<template>
  <header class="page-head">
    <div>
      <nav class="crumbs">
        <RouterLink :to="{ name: 'projects' }">Projects</RouterLink>
        <span>/</span>
        <span>{{ project?.name ?? projectId }}</span>
      </nav>
      <h1>Data sources</h1>
      <p>Where this project's catalog is imported from. A scan reads schema metadata only.</p>
    </div>
    <Button label="Add data source" icon="pi pi-plus" @click="openDialog" />
  </header>

  <Message v-if="failure" severity="error" :closable="false">{{ failure }}</Message>

  <div v-if="loading" class="loading"><ProgressSpinner style="width: 2rem; height: 2rem" /></div>

  <DataTable v-else :value="sources" data-key="id">
    <template #empty>
      <p class="empty">No data sources yet. Add one, then run a scan to import its catalog.</p>
    </template>
    <Column field="name" header="Name" />
    <Column header="DSN">
      <template #body="{ data }">
        <code v-if="data.dsnEnvironment">{{ data.dsnEnvironment }}</code>
        <span v-else class="muted">stored</span>
      </template>
    </Column>
    <Column field="id" header="ID">
      <template #body="{ data }"><code>{{ data.id }}</code></template>
    </Column>
    <Column header="Last scan">
      <template #body="{ data }">
        <Tag
          v-if="scanStatus[data.id]"
          :value="scanStatus[data.id]"
          :severity="statusSeverity(scanStatus[data.id])"
        />
        <span v-else class="muted">—</span>
      </template>
    </Column>
    <Column header="" style="width: 11rem">
      <template #body="{ data }">
        <Button
          label="Run scan"
          icon="pi pi-play"
          size="small"
          severity="secondary"
          outlined
          :loading="scanning[data.id]"
          @click="runScan(data as DataSource)"
        />
      </template>
    </Column>
  </DataTable>

  <Dialog v-model:visible="dialogOpen" modal header="Add a MySQL data source" :style="{ width: '34rem' }">
    <form class="form" @submit.prevent="create">
      <label for="name">Name</label>
      <InputText id="name" v-model="draft.name" maxlength="200" required autofocus fluid />

      <label for="dsn">
        Connection string <span class="muted">stored encrypted; never shown again</span>
      </label>
      <Password
        id="dsn"
        v-model="draft.dsn"
        :feedback="false"
        toggle-mask
        fluid
        autocomplete="off"
        placeholder="user:password@tcp(host:3306)/database?charset=utf8mb4"
      />

      <label for="environment">
        DSN environment variable <span class="muted">fallback when nothing is stored</span>
      </label>
      <InputText
        id="environment"
        v-model="draft.dsnEnvironment"
        pattern="[A-Z][A-Z0-9_]*"
        maxlength="200"
        required
        fluid
      />

      <label for="reason">Reason <span class="muted">recorded in the audit log</span></label>
      <Textarea id="reason" v-model="draft.reason" maxlength="2000" rows="2" required fluid />

      <div class="actions">
        <Button label="Cancel" severity="secondary" text @click="dialogOpen = false" />
        <Button
          type="submit"
          label="Create data source"
          :loading="saving"
          :disabled="!draft.name.trim() || !draft.dsnEnvironment.trim() || !draft.reason.trim()"
        />
      </div>
    </form>
  </Dialog>
</template>

<style scoped>
.page-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 1rem;
  margin-bottom: 1.25rem;
}

.crumbs {
  display: flex;
  gap: 0.4rem;
  margin-bottom: 0.35rem;
  font-size: 0.8rem;
  color: var(--p-text-muted-color);
}

.crumbs a {
  color: inherit;
}

h1 {
  margin: 0 0 0.25rem;
  font-size: 1.35rem;
}

.page-head p {
  margin: 0;
  color: var(--p-text-muted-color);
  font-size: 0.9rem;
}

.loading {
  display: grid;
  place-items: center;
  padding: 3rem;
}

.empty,
.muted {
  color: var(--p-text-muted-color);
}

.form {
  display: grid;
  gap: 0.5rem;
}

label {
  font-size: 0.8rem;
  color: var(--p-text-muted-color);
}

.actions {
  display: flex;
  justify-content: flex-end;
  gap: 0.5rem;
  margin-top: 0.75rem;
}

code {
  font-size: 0.8rem;
}
</style>
