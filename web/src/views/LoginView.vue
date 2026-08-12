<script setup lang="ts">
import { ref } from "vue";
import { useRouter } from "vue-router";
import Button from "primevue/button";
import Card from "primevue/card";
import Message from "primevue/message";
import Password from "primevue/password";

import { api, setCsrfToken, type Session } from "@/api/client";

const emit = defineEmits<{ (event: "signed-in", session: Session): void }>();

const router = useRouter();
const token = ref("");
const failure = ref("");
const busy = ref(false);

async function submit(): Promise<void> {
  failure.value = "";
  busy.value = true;
  try {
    const session = await api.signIn(token.value.trim());
    setCsrfToken(session.csrfToken);
    token.value = "";
    emit("signed-in", session);
    await router.push({ name: "data-sources" });
  } catch (error) {
    failure.value = error instanceof Error ? error.message : "Sign-in failed.";
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <div class="login-page">
    <Card class="login-card">
      <template #title>Sign in to dbgraph</template>
      <template #subtitle>
        Use the Web token configured for your role. It is exchanged for a server-side session and is
        never stored by this page.
      </template>
      <template #content>
        <form class="login-form" @submit.prevent="submit">
          <label for="token">Access token</label>
          <Password
            id="token"
            v-model="token"
            :feedback="false"
            toggle-mask
            fluid
            autocomplete="off"
            required
          />
          <Message v-if="failure" severity="error" :closable="false">{{ failure }}</Message>
          <Button type="submit" label="Sign in" :loading="busy" :disabled="!token.trim()" />
        </form>
      </template>
    </Card>
  </div>
</template>

<style scoped>
.login-page {
  display: grid;
  place-items: center;
  min-height: 100vh;
  padding: 1.5rem;
}

.login-card {
  width: min(420px, 100%);
}

.login-form {
  display: grid;
  gap: 0.75rem;
}

label {
  font-size: 0.8rem;
  color: var(--p-text-muted-color);
}
</style>
