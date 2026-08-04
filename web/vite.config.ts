import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { existsSync, readFileSync } from "node:fs";

interface DiscoveryRecord {
  base_url?: string;
  auth_token?: string;
}

function readDiscovery(): DiscoveryRecord | undefined {
  const path =
    process.env.AGENTICO_DISCOVERY_FILE ?? "/tmp/agentico-web-pr63/.agentico-server.json";
  if (!existsSync(path)) return undefined;
  try {
    return JSON.parse(readFileSync(path, "utf8")) as DiscoveryRecord;
  } catch {
    return undefined;
  }
}

const discovery = readDiscovery();
const serverTarget =
  process.env.AGENTICO_SERVER_URL ?? discovery?.base_url ?? "http://127.0.0.1:7878";
const authToken = discovery?.auth_token;

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
        headers: authToken ? { Authorization: `Bearer ${authToken}` } : undefined,
      },
    },
  },
});
