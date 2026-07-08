import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

const serverTarget = process.env.AGENTICO_SERVER_URL ?? "http://127.0.0.1:7878";

export default defineConfig({
  plugins: [react()],
  build: {
    outDir: "dist",
    emptyOutDir: true,
    sourcemap: false,
    target: "es2020",
  },
  server: {
    port: 5173,
    proxy: {
      "/api/v1": {
        target: serverTarget,
        changeOrigin: false,
      },
    },
  },
});
