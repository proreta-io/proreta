/// <reference types="vitest/config" />
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

const apiTarget = process.env.PRORETA_API_TARGET ?? "http://localhost:8080";
const statuspageTarget = process.env.PRORETA_STATUSPAGE_TARGET ?? "http://localhost:8081";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    port: Number(process.env.PRORETA_WEB_PORT ?? 5173),
    // The session cookie and the public URL are bound to this port, so do not drift to another one.
    strictPort: true,
    proxy: {
      "/api": { target: apiTarget, changeOrigin: true },
      "/status": { target: statuspageTarget, changeOrigin: true },
    },
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test-setup.ts"],
    // Times are rendered in local time; tests pin the zone so the expectations are stable.
    env: { TZ: "UTC" },
  },
});
