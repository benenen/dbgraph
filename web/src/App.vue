<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { RouterView, useRoute, useRouter } from "vue-router";
import Button from "primevue/button";
import Tag from "primevue/tag";
import Toast from "primevue/toast";

import { api, setCsrfToken, UnauthenticatedError, type Session } from "@/api/client";

const route = useRoute();
const router = useRouter();
const session = ref<Session | null>(null);
const ready = ref(false);

// One workspace, so the sidebar names the one thing an operator manages.
const navigation = [
  { label: "Data sources", icon: "pi pi-database", route: "data-sources" },
  { label: "Relation graph", icon: "pi pi-share-alt", route: "relation-graph" },
];

function isActive(name: string): boolean {
  return route.name === name;
}

async function signOut(): Promise<void> {
  try {
    await api.signOut();
  } finally {
    session.value = null;
    await router.push({ name: "login" });
  }
}

onMounted(async () => {
  if (route.meta.public === true) {
    ready.value = true;
    return;
  }
  try {
    const current = await api.session();
    setCsrfToken(current.csrfToken);
    session.value = current;
  } catch (error) {
    if (error instanceof UnauthenticatedError) {
      await router.replace({ name: "login" });
    }
  } finally {
    ready.value = true;
  }
});

// The login view provides its own frame.
const chromeless = computed(() => route.meta.public === true);
</script>

<template>
  <Toast />

  <RouterView v-if="chromeless" @signed-in="(s: Session) => (session = s)" />

  <div v-else-if="ready" class="console">
    <header class="topbar">
      <span class="brand"><span class="brand-mark">d/g</span> dbgraph</span>
      <span class="spacer" />
      <template v-if="session">
        <span class="actor">{{ session.actor }}</span>
        <Tag :value="session.role" severity="secondary" />
        <Button label="Sign out" severity="secondary" text size="small" @click="signOut" />
      </template>
    </header>

    <div class="shell">
      <nav class="sidebar" aria-label="Primary">
        <RouterLink
          v-for="item in navigation"
          :key="item.label"
          :to="{ name: item.route }"
          class="nav-item"
          :class="{ active: isActive(item.route) }"
        >
          <i :class="item.icon" />
          <span>{{ item.label }}</span>
        </RouterLink>
      </nav>

      <main class="workspace">
        <RouterView />
      </main>
    </div>
  </div>
</template>

<style scoped>
.console {
  min-height: 100vh;
}

.topbar {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  height: 52px;
  padding: 0 1rem;
  border-bottom: 1px solid var(--p-content-border-color);
  background: var(--p-content-background);
}

.brand {
  display: inline-flex;
  align-items: center;
  gap: 0.5rem;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 0.9rem;
}

.brand-mark {
  display: grid;
  place-items: center;
  width: 24px;
  height: 24px;
  border-radius: 3px;
  background: var(--p-text-color);
  color: var(--p-content-background);
  font-size: 0.7rem;
}

.spacer {
  flex: 1;
}

.actor {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 0.8rem;
  color: var(--p-text-muted-color);
}

.shell {
  display: grid;
  grid-template-columns: 220px 1fr;
  min-height: calc(100vh - 52px);
}

.sidebar {
  padding: 0.75rem;
  border-right: 1px solid var(--p-content-border-color);
  background: var(--p-content-background);
}

.nav-item {
  display: flex;
  align-items: center;
  gap: 0.6rem;
  padding: 0.5rem 0.7rem;
  border-radius: 6px;
  color: var(--p-text-color);
  text-decoration: none;
  font-size: 0.9rem;
}

.nav-item:hover {
  background: var(--p-content-hover-background);
}

.nav-item.active {
  background: var(--p-highlight-background);
  color: var(--p-highlight-color);
}

.nav-item.disabled {
  opacity: 0.45;
  pointer-events: none;
}

.nav-hint {
  margin: 0.6rem 0.7rem 0;
  font-size: 0.75rem;
  line-height: 1.5;
  color: var(--p-text-muted-color);
}

.workspace {
  min-width: 0;
  padding: 1.5rem;
}

@media (max-width: 820px) {
  .shell {
    grid-template-columns: 1fr;
  }

  .sidebar {
    display: flex;
    gap: 0.4rem;
    border-right: 0;
    border-bottom: 1px solid var(--p-content-border-color);
  }

  .nav-hint {
    display: none;
  }
}
</style>
