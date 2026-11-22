import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { fileURLToPath, URL } from "node:url";

// The build output is embedded into the control-plane binary (internal/web).
// Asset names are content-hashed and no inline scripts or styles are emitted,
// so the server's strict Content-Security-Policy applies unchanged.
//
// Local development: run the control plane somewhere, then
//   VITE_API_PROXY=http://<control-host>:8080 npm run dev
export default defineConfig({
  plugins: [react()],
  resolve: { alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) } },
  build: {
    // Output goes straight into the Go package so the binary embeds it.
    outDir: "../internal/web/static",
    emptyOutDir: true,
    sourcemap: false,
    cssCodeSplit: false,
  },
  server: {
    proxy: { "/api": process.env.VITE_API_PROXY ?? "http://localhost:8080" },
  },
});
