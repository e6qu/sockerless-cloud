import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  base: "/ui/",
  publicDir: "../../public",
  build: {
    rollupOptions: {
      output: {
        manualChunks(id) {
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
      "/health": "http://localhost:4567",
      "/sim": "http://localhost:4567",
    },
  },
});
