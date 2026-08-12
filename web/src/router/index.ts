import { createRouter, createWebHistory } from "vue-router";

import LoginView from "@/views/LoginView.vue";
import ProjectsView from "@/views/ProjectsView.vue";
import DataSourcesView from "@/views/DataSourcesView.vue";

// The open project lives in the URL, not in a picker: a link is shareable and
// a reload keeps its place.
export const router = createRouter({
  history: createWebHistory("/app/"),
  routes: [
    { path: "/", redirect: { name: "projects" } },
    { path: "/login", name: "login", component: LoginView, meta: { public: true } },
    { path: "/projects", name: "projects", component: ProjectsView },
    { path: "/data-sources", name: "data-sources", component: DataSourcesView },
    {
      path: "/projects/:projectId/data-sources",
      name: "project-sources",
      component: DataSourcesView,
      props: true,
    },
  ],
});
