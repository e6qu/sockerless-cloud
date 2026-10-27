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
    // Serve the bundle under /ui/ and forward the rest of the console's origin
    // to the simulator: /ui/config.json, the auth layer under /auth/, the
    // Security Token Service, and the Google Cloud APIs, which the console
    // reaches at its own origin when the cloud coordinates are empty.
    proxy: {
      "/ui/config.json": "http://localhost:4567",
      "^/(?!ui(?:/|$))": "http://localhost:4567",
    },
  },
});
