import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  base: "/ui/",
  publicDir: "../../public",
  build: {
    // Cloudscape is deliberately isolated into one cacheable vendor chunk.
    // Its current minified size is below this budget and compresses to 219 KiB.
    chunkSizeWarningLimit: 800,
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (id.includes("/node_modules/@cloudscape-design/")) {
            return "cloudscape";
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
      "/health": "http://localhost:4566",
      "/sim": "http://localhost:4566",
    },
  },
});
