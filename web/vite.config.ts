import { fileURLToPath, URL } from "node:url";
import { writeFileSync } from "node:fs";

import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";

// The console is served by the Go binary at /app/, from files embedded out of
// internal/transport/webapi/app. Building straight into that directory keeps
// one build step and one artifact: `npm run build` then `go build`.
const goEmbedDirectory = fileURLToPath(
  new URL("../internal/transport/webapi/app", import.meta.url),
);

const preserveGoEmbedDirectory = {
  name: "preserve-go-embed-directory",
  closeBundle() {
    writeFileSync(
      new URL("../internal/transport/webapi/app/.gitkeep", import.meta.url),
      "Vue build output is generated here before Go build and test commands.\n",
    );
  },
};

const backend = "http://127.0.0.1:8080";

export default defineConfig({
  base: "/app/",
  plugins: [vue(), preserveGoEmbedDirectory],
  resolve: {
    alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) },
  },
  build: {
    outDir: goEmbedDirectory,
    emptyOutDir: true,
  },
  server: {
    // Same-origin in development too, so the session cookie behaves exactly as
    // it does in production and there is no CORS story to maintain.
    proxy: {
      "/api": { target: backend, changeOrigin: false },
      "/login": { target: backend, changeOrigin: false },
      "/logout": { target: backend, changeOrigin: false },
      "/healthz": { target: backend, changeOrigin: false },
    },
  },
});
