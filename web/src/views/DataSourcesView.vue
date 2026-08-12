<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import { useRouter } from "vue-router";
import Button from "primevue/button";
import Column from "primevue/column";
import DataTable from "primevue/datatable";
import Dialog from "primevue/dialog";
import InputText from "primevue/inputtext";
import Message from "primevue/message";
import MultiSelect from "primevue/multiselect";
import Password from "primevue/password";
import ProgressSpinner from "primevue/progressspinner";
import Tag from "primevue/tag";
import Textarea from "primevue/textarea";
import { useToast } from "primevue/usetoast";

import { api, UnauthenticatedError, type DataSource, type Project } from "@/api/client";

// One page serves both routes. Without a project it is the registry of every
// source; with one it shows that project's sources and can scan them, because
// a scan writes into a project's catalog and has nowhere to go without one.
const props = defineProps<{ projectId?: string }>();

const router = useRouter();
const toast = useToast();

const scoped = computed(() => Boolean(props.projectId));

const sources = ref<DataSource[]>([]);
const projects = ref<Project[]>([]);
const loading = ref(true);
const failure = ref("");

const project = computed(
  () => projects.value.find((candidate) => candidate.id === props.projectId) ?? null,
);

/** Registered sources this project has not adopted yet. */
const available = ref<DataSource[]>([]);

const dialogOpen = ref(false);
const saving = ref(false);
// Empty means the dialog is registering; an id means it is editing that source.
const editingId = ref("");
// Registering records which projects introduced the source, and each becomes a
// link. On a project's own page that project is implied.
const draft = ref({ name: "", dsnEnvironment: "", dsn: "", reason: "", projectIds: [] as string[] });

const linkDialogOpen = ref(false);
const linking = ref("");
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
    const [allProjects, allSources, linked] = await Promise.all([
      api.listProjects(),
      api.listAllDataSources(),
      props.projectId ? api.listDataSources(props.projectId) : Promise.resolve([] as DataSource[]),
    ]);
    projects.value = allProjects;
    if (props.projectId) {
      const linkedIds = new Set(linked.map((source) => source.id));
      sources.value = linked;
      available.value = allSources.filter((source) => !linkedIds.has(source.id));
    } else {
      sources.value = allSources;
      available.value = [];
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
  draft.value = {
    name: "",
    dsnEnvironment: "",
    dsn: "",
    reason: "",
    projectIds: props.projectId ? [props.projectId] : [],
  };
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
    projectIds: [],
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
    // The source is registered through the first project chosen; the rest adopt
    // it immediately, which is the same thing the Link action does later.
    const [owner, ...adopters] = submitted.projectIds;
    const source = await api.createDataSource(owner, submitted);
    for (const projectId of adopters) {
      await api.linkDataSource(projectId, source.id);
    }
    dialogOpen.value = false;
    toast.add({ severity: "success", summary: `Data source ${source.name} registered`, life: 4000 });
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

async function link(source: DataSource): Promise<void> {
  if (!props.projectId) return;
  linking.value = source.id;
  try {
    await api.linkDataSource(props.projectId, source.id);
    toast.add({ severity: "success", summary: `${source.name} linked`, life: 3000 });
    linkDialogOpen.value = false;
    await load();
  } catch (error) {
    toast.add({
      severity: "error",
      summary: "Could not link the data source",
      detail: error instanceof Error ? error.message : "",
      life: 6000,
    });
  } finally {
    linking.value = "";
  }
}

async function unlink(source: DataSource): Promise<void> {
  if (!props.projectId) return;
  try {
    await api.unlinkDataSource(props.projectId, source.id);
    toast.add({ severity: "success", summary: `${source.name} unlinked`, life: 3000 });
    await load();
  } catch (error) {
    toast.add({
      severity: "error",
      summary: "Could not unlink the data source",
      detail: error instanceof Error ? error.message : "",
      life: 6000,
    });
  }
}

async function runScan(source: DataSource): Promise<void> {
  if (!props.projectId) return;
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
  if (!props.projectId) return;
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

function openProjects(): void {
  void router.push({ name: "projects" });
}

onMounted(load);
watch(() => props.projectId, load);
onUnmounted(() => {
  timers.forEach((timer) => window.clearTimeout(timer));
  timers = [];
});
</script>

<template>
  <header class="page-head">
    <div>
      <nav v-if="scoped" class="crumbs">
        <a href="#" @click.prevent="openProjects">Projects</a>
        <span>/</span>
        <span>{{ project?.name ?? projectId }}</span>
      </nav>
      <h1>Data sources</h1>
      <p v-if="scoped">
        The databases this project reads schema metadata from. Scanning one imports its catalog into
        this project; other projects linking the same source keep their own.
      </p>
      <p v-else>
        Databases dbgraph reads schema metadata from. A source is registered once and any number of
        projects can use it; each project scans it separately and keeps its own catalog.
      </p>
    </div>
    <div class="head-actions">
      <Button
        v-if="scoped"
        label="Link existing"
        icon="pi pi-link"
        severity="secondary"
        outlined
        :disabled="!available.length"
        @click="linkDialogOpen = true"
      />
      <Button
        label="Register data source"
        icon="pi pi-plus"
        :disabled="!projects.length"
        @click="openDialog"
      />
    </div>
  </header>

  <Message v-if="failure" severity="error" :closable="false">{{ failure }}</Message>
  <Message v-else-if="!loading && !projects.length" severity="warn" :closable="false">
    Create a project first — registering a source records which project introduced it.
  </Message>

  <div v-if="loading" class="loading"><ProgressSpinner style="width: 2rem; height: 2rem" /></div>

  <DataTable v-else :value="sources" data-key="id">
    <template #empty>
      <p v-if="scoped" class="muted">
        No data sources linked yet. Link one that already exists, or register a new one.
      </p>
      <p v-else class="muted">No data sources yet. Register one to start importing catalogs.</p>
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
    <Column v-if="scoped" header="Last scan">
      <template #body="{ data }">
        <Tag
          v-if="scanStatus[data.id]"
          :value="scanStatus[data.id]"
          :severity="statusSeverity(scanStatus[data.id])"
        />
        <span v-else class="muted">—</span>
      </template>
    </Column>
    <Column header="" :style="scoped ? 'width: 24rem' : 'width: 12rem'">
      <template #body="{ data }">
        <div class="row-actions">
          <Button
            v-if="scoped"
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
            v-if="scoped"
            label="Unlink"
            size="small"
            severity="danger"
            text
            @click="unlink(data as DataSource)"
          />
          <Button
            v-else
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
      <template v-if="!editingId && !scoped">
        <label for="project">
          Linked projects <span class="muted">each keeps its own catalog</span>
        </label>
        <MultiSelect
          id="project"
          v-model="draft.projectIds"
          :options="projects"
          option-label="name"
          option-value="id"
          display="chip"
          filter
          placeholder="Choose one or more projects"
          fluid
        />
      </template>

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
          :disabled="!draft.name.trim() || (!editingId && !draft.projectIds.length)"
        />
      </div>
    </form>
  </Dialog>

  <Dialog
    v-model:visible="linkDialogOpen"
    modal
    header="Link a data source"
    :style="{ width: '30rem' }"
  >
    <p class="muted dialog-lead">
      Sources already registered in dbgraph that this project has not adopted.
    </p>
    <ul class="candidates">
      <li v-for="source in available" :key="source.id">
        <span>
          <strong>{{ source.name }}</strong>
          <code v-if="source.dsnEnvironment">{{ source.dsnEnvironment }}</code>
        </span>
        <Button
          label="Link"
          size="small"
          :loading="linking === source.id"
          @click="link(source as DataSource)"
        />
      </li>
    </ul>
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

.head-actions {
  display: flex;
  flex-shrink: 0;
  gap: 0.5rem;
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

.dialog-lead {
  margin: 0 0 0.75rem;
  font-size: 0.85rem;
}

.candidates {
  display: grid;
  gap: 0.4rem;
  margin: 0;
  padding: 0;
  list-style: none;
}

.candidates li {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 1rem;
  padding: 0.5rem 0.7rem;
  border: 1px solid var(--p-content-border-color);
  border-radius: 6px;
}

.candidates code {
  margin-left: 0.5rem;
  font-size: 0.75rem;
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
