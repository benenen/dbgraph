<script setup lang="ts">
import { onMounted, ref } from "vue";
import { useRouter } from "vue-router";
import Button from "primevue/button";
import Column from "primevue/column";
import DataTable from "primevue/datatable";
import Dialog from "primevue/dialog";
import InputText from "primevue/inputtext";
import Message from "primevue/message";
import ProgressSpinner from "primevue/progressspinner";
import Textarea from "primevue/textarea";
import { useToast } from "primevue/usetoast";

import { api, UnauthenticatedError, type Project } from "@/api/client";

const router = useRouter();
const toast = useToast();

const projects = ref<Project[]>([]);
const loading = ref(true);
const failure = ref("");

const dialogOpen = ref(false);
const saving = ref(false);
const draft = ref({ name: "", description: "", reason: "" });

async function load(): Promise<void> {
  loading.value = true;
  failure.value = "";
  try {
    projects.value = await api.listProjects();
  } catch (error) {
    if (error instanceof UnauthenticatedError) {
      await router.replace({ name: "login" });
      return;
    }
    failure.value = error instanceof Error ? error.message : "Could not load projects.";
  } finally {
    loading.value = false;
  }
}

function openDialog(): void {
  draft.value = { name: "", description: "", reason: "" };
  dialogOpen.value = true;
}

async function create(): Promise<void> {
  saving.value = true;
  try {
    const project = await api.createProject(draft.value);
    dialogOpen.value = false;
    toast.add({ severity: "success", summary: `Project ${project.name} created`, life: 4000 });
    await load();
    await open(project);
  } catch (error) {
    toast.add({
      severity: "error",
      summary: "Could not create the project",
      detail: error instanceof Error ? error.message : "",
      life: 6000,
    });
  } finally {
    saving.value = false;
  }
}

async function open(project: Project): Promise<void> {
  await router.push({ name: "data-sources", params: { projectId: project.id } });
}

onMounted(load);
</script>

<template>
  <header class="page-head">
    <div>
      <h1>Projects</h1>
      <p>Each project owns its own catalog, relations, and data sources.</p>
    </div>
    <Button label="New project" icon="pi pi-plus" @click="openDialog" />
  </header>

  <Message v-if="failure" severity="error" :closable="false">{{ failure }}</Message>

  <div v-if="loading" class="loading"><ProgressSpinner style="width: 2rem; height: 2rem" /></div>

  <DataTable
    v-else
    :value="projects"
    data-key="id"
    :row-hover="true"
    selection-mode="single"
    @row-select="(event) => open(event.data as Project)"
  >
    <template #empty>
      <p class="empty">No projects yet. Create one to start a catalog.</p>
    </template>
    <Column field="name" header="Name" />
    <Column field="description" header="Description" />
    <Column field="id" header="ID">
      <template #body="{ data }"><code>{{ data.id }}</code></template>
    </Column>
    <Column header="Created">
      <template #body="{ data }">{{ data.createdAt.slice(0, 10) }}</template>
    </Column>
    <Column header="" style="width: 12rem">
      <template #body="{ data }">
        <Button
          label="Data sources"
          icon="pi pi-database"
          size="small"
          severity="secondary"
          outlined
          @click="open(data as Project)"
        />
      </template>
    </Column>
  </DataTable>

  <Dialog v-model:visible="dialogOpen" modal header="New project" :style="{ width: '30rem' }">
    <form class="form" @submit.prevent="create">
      <label for="name">Name</label>
      <InputText id="name" v-model="draft.name" maxlength="200" required autofocus fluid />

      <label for="description">Description</label>
      <Textarea id="description" v-model="draft.description" maxlength="2000" rows="2" fluid />

      <label for="reason">Reason <span class="muted">recorded in the audit log</span></label>
      <Textarea id="reason" v-model="draft.reason" maxlength="2000" rows="2" required fluid />

      <div class="actions">
        <Button label="Cancel" severity="secondary" text @click="dialogOpen = false" />
        <Button
          type="submit"
          label="Create project"
          :loading="saving"
          :disabled="!draft.name.trim() || !draft.reason.trim()"
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

.muted {
  opacity: 0.7;
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
