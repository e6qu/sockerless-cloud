import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  base: "/ui/",
  publicDir: "../../public",
  build: {
    // Fluent UI is isolated into one cacheable vendor chunk whose compressed
    // size remains below the production transfer budget.
    chunkSizeWarningLimit: 700,
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (id.includes("/node_modules/@fluentui/")) {
            return "fluent";
          }
          if (
            id.includes("/node_modules/react/") ||
            id.includes("/node_modules/react-dom/") ||
            id.includes("/node_modules/react-router/")
          ) {
            return "react";
          }
          if (id.includes("/node_modules/@tanstack/")) {
            return "query";
          }
        },
      },
    },
  },
  server: {
    // Serve the bundle under /ui/ and forward the rest of the portal's origin
    // to the simulator: /ui/config.json, the auth layer and federation broker
    // under /auth/, and Azure Resource Manager, Microsoft Graph and Log
    // Analytics, which the portal reaches at its own origin when their
    // coordinates are empty. The Blob service is the storage account's own
    // endpoint, which the browser reaches directly.
    proxy: {
      "/ui/config.json": "http://localhost:4568",
      "^/(?!ui(?:/|$))": "http://localhost:4568",
    },
  },
});
