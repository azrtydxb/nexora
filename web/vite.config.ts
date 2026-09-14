import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { fileURLToPath } from "node:url";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  // The build stamp (declared in src/build-info.d.ts); deploy/docker/mgmt.Dockerfile sets the variables.
  define: {
    __NEXORA_VERSION__: JSON.stringify(process.env.NEXORA_VERSION ?? "dev"),
    __NEXORA_COMMIT__: JSON.stringify(process.env.NEXORA_COMMIT ?? ""),
    __NEXORA_BUILD_DATE__: JSON.stringify(process.env.NEXORA_BUILD_DATE ?? ""),
  },
  resolve: { alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) } },
  server: { proxy: { "/api": "http://127.0.0.1:8080" } },
  build: { outDir: "dist", sourcemap: false },
});
