import { createRouter, createWebHistory } from "vue-router";

import LoginView from "@/views/LoginView.vue";
import DataSourcesView from "@/views/DataSourcesView.vue";

// One workspace, so there is nothing to pick and no project in the URL. The
// server guarantees the project exists; the console never names it. Links from
// before that shape land on the same page.
export const router = createRouter({
  history: createWebHistory("/app/"),
  routes: [
    { path: "/", redirect: { name: "data-sources" } },
    { path: "/login", name: "login", component: LoginView, meta: { public: true } },
    { path: "/data-sources", name: "data-sources", component: DataSourcesView },
    { path: "/projects", redirect: { name: "data-sources" } },
    { path: "/projects/:projectId/data-sources", redirect: { name: "data-sources" } },
  ],
});
