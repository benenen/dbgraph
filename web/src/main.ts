import { createApp } from "vue";
import PrimeVue from "primevue/config";
import Aura from "@primevue/themes/aura";
import ConfirmationService from "primevue/confirmationservice";
import ToastService from "primevue/toastservice";

import App from "./App.vue";
import { onSignedOut } from "./api/client";
import { router } from "./router";
import "primeicons/primeicons.css";
import "./style.css";

// The server mints a nonce per request and stamps it into the shell. PrimeVue
// injects its theme at runtime, so it has to sign those style elements with the
// same nonce or a strict style-src rejects them.
const nonce = document
  .querySelector('meta[property="csp-nonce"]')
  ?.getAttribute("content") ?? "";

onSignedOut(() => {
  if (router.currentRoute.value.name !== "login") void router.replace({ name: "login" });
});

createApp(App)
  .use(router)
  .use(PrimeVue, {
    theme: { preset: Aura, options: { darkModeSelector: ".dark" } },
    csp: { nonce },
  })
  .use(ToastService)
  .use(ConfirmationService)
  .mount("#app");
