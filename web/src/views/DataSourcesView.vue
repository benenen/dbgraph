<script setup lang="ts">
import { onMounted, onUnmounted, ref } from "vue";
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

import { api, UnauthenticatedError, type DataSource } from "@/api/client";

const toast = useToast();

// The server guarantees one workspace and links every source to it, so the
// console never names a project. It reads the id once and uses it for the
// calls that still take one.
const workspaceId = ref("");

const sources = ref<DataSource[]>([]);
const loading = ref(true);
const failure = ref("");

const dialogOpen = ref(false);
const saving = ref(false);
// Empty means the dialog is registering; an id means it is editing that source.
const editingId = ref("");
const draft = ref({ name: "", dsnEnvironment: "", dsn: "", reason: "" });

const removing = ref("");
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
    const [projects, registered] = await Promise.all([
      api.listProjects(),
      api.listAllDataSources(),
    ]);
    workspaceId.value = projects[0]?.id ?? "";
    sources.value = registered;
    if (!workspaceId.value) {
      failure.value = "The server has no workspace yet. Restart it to create one.";
    }
  } catch (error) {
    if (error instanceof UnauthenticatedError) return;
    failure.value = error instanceof Error ? error.message : "Could not load data sources.";
  } finally {
    loading.value = false;
  }
}

function openDialog(): void {
  editingId.value = "";
  draft.value = { name: "", dsnEnvironment: "", dsn: "", reason: "" };
  dialogOpen.value = true;
}

function openEdit(source: DataSource): void {
  editingId.value = source.id;
  // The connection string is write-only, so the field starts empty and only
  // replaces the stored one when something is typed.
  draft.value = {
    name: source.name,
    dsnEnvironment: source.dsnEnvironment ?? "",
    dsn: "",
    reason: "",
  };
  dialogOpen.value = true;
}

async function save(): Promise<void> {
  saving.value = true;
  // Hand the connection string to the request and drop it from reactive state
  // in the same breath, so it never lingers in the input.
  const submitted = { ...draft.value };
  draft.value.dsn = "";
  try {
    if (editingId.value) {
      const updated = await api.updateDataSource(editingId.value, submitted);
      dialogOpen.value = false;
      toast.add({ severity: "success", summary: `${updated.name} updated`, life: 4000 });
      await load();
      return;
    }
    const source = await api.createDataSource(workspaceId.value, submitted);
    dialogOpen.value = false;
    toast.add({ severity: "success", summary: `${source.name} registered`, life: 4000 });
    await load();
  } catch (error) {
    toast.add({
      severity: "error",
      summary: editingId.value
        ? "Could not update the data source"
        : "Could not register the data source",
      detail: error instanceof Error ? error.message : "",
      life: 6000,
    });
  } finally {
    saving.value = false;
  }
}

async function remove(source: DataSource): Promise<void> {
  removing.value = source.id;
  try {
    await api.deleteDataSource(source.id);
    toast.add({ severity: "success", summary: `${source.name} deleted`, life: 3000 });
    await load();
  } catch (error) {
    toast.add({
      severity: "error",
      summary: "Could not delete the data source",
      detail: error instanceof Error ? error.message : "",
      life: 6000,
    });
  } finally {
    removing.value = "";
  }
}

async function runScan(source: DataSource): Promise<void> {
  scanning.value = { ...scanning.value, [source.id]: true };
  scanStatus.value = { ...scanStatus.value, [source.id]: "Queueing…" };
  try {
    const job = await api.startScan(workspaceId.value, source.id, "Full inventory from the console");
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
      const job = await api.job(workspaceId.value, jobId);
      // The job carries why it stopped, which is the only part worth reading.
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
      <h1>Data sources</h1>
      <p>
        The databases dbgraph reads schema metadata from. Scanning one imports its tables and
        columns into the catalog, where relations are proposed and reviewed against them.
      </p>
    </div>
    <Button
      label="Register data source"
      icon="pi pi-plus"
      :disabled="!workspaceId"
      @click="openDialog"
    />
  </header>

  <Message v-if="failure" severity="error" :closable="false">{{ failure }}</Message>

  <div v-if="loading" class="loading"><ProgressSpinner style="width: 2rem; height: 2rem" /></div>

  <DataTable v-else :value="sources" data-key="id">
    <template #empty>
      <p class="muted">No data sources yet. Register one to start importing a catalog.</p>
    </template>
    <Column field="name" header="Name" />
    <Column header="DSN">
      <template #body="{ data }">
        <code v-if="data.dsnEnvironment">{{ data.dsnEnvironment }}</code>
        <span v-else class="muted">stored</span>
      </template>
    </Column>
    <Column field="kind" header="Kind" />
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
    <Column header="" style="width: 18rem">
      <template #body="{ data }">
        <div class="row-actions">
          <Button
            label="Run scan"
            icon="pi pi-play"
            size="small"
            severity="secondary"
            outlined
            :loading="scanning[data.id]"
            @click="runScan(data as DataSource)"
          />
          <Button
            label="Edit"
            size="small"
            severity="secondary"
            text
            @click="openEdit(data as DataSource)"
          />
          <Button
            label="Delete"
            size="small"
            severity="danger"
            text
            :loading="removing === data.id"
            @click="remove(data as DataSource)"
          />
        </div>
      </template>
    </Column>
  </DataTable>

  <Dialog
    v-model:visible="dialogOpen"
    modal
    :header="editingId ? 'Edit data source' : 'Register a MySQL data source'"
    :style="{ width: '34rem' }"
  >
    <form v-if="dialogOpen" class="form" @submit.prevent="save">
      <label for="name">Name <span class="muted">unique across dbgraph</span></label>
      <InputText id="name" v-model="draft.name" maxlength="200" required autofocus fluid />

      <label for="dsn">
        Connection string
        <span class="muted">
          {{ editingId ? "leave blank to keep the stored one" : "stored encrypted; never shown again" }}
        </span>
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
        DSN environment variable <span class="muted">optional, read when nothing is stored</span>
      </label>
      <InputText
        id="environment"
        v-model="draft.dsnEnvironment"
        pattern="[A-Z][A-Z0-9_]*"
        maxlength="200"
        fluid
      />

      <label for="reason">Reason <span class="muted">optional, recorded in the audit log</span></label>
      <Textarea id="reason" v-model="draft.reason" maxlength="2000" rows="2" fluid />

      <div class="actions">
        <Button label="Cancel" severity="secondary" text @click="dialogOpen = false" />
        <Button
          type="submit"
          :label="editingId ? 'Save changes' : 'Register'"
          :loading="saving"
          :disabled="!draft.name.trim()"
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

.muted {
  color: var(--p-text-muted-color);
}

.row-actions {
  display: flex;
  gap: 0.4rem;
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
