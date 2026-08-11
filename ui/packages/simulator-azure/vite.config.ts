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
    proxy: {
      "/health": "http://localhost:4568",
      "/sim": "http://localhost:4568",
    },
  },
});
