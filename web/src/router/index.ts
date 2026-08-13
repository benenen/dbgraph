import { createRouter, createWebHistory } from "vue-router";

import LoginView from "@/views/LoginView.vue";
import DataSourcesView from "@/views/DataSourcesView.vue";
import ReviewView from "@/views/ReviewView.vue";
import SchemaGraphView from "@/views/SchemaGraphView.vue";

// The workspace routes carry no catalog-scope identifier: dbgraph has one
// service-wide data-source registry and relation graph.
export const router = createRouter({
  history: createWebHistory("/app/"),
  routes: [
    { path: "/", redirect: { name: "data-sources" } },
    { path: "/login", name: "login", component: LoginView, meta: { public: true } },
    { path: "/data-sources", name: "data-sources", component: DataSourcesView },
    { path: "/relation-graph", name: "relation-graph", component: SchemaGraphView },
    { path: "/review", name: "review", component: ReviewView },
  ],
});
