import { defineConfig } from "vite";
import solid from "vite-plugin-solid";
import { tanstackRouter } from "@tanstack/router-plugin/vite";
import tailwindcss from "@tailwindcss/vite";
import iconifyOffline from "vite-plugin-iconify-offline";
import { resolve } from "node:path";

// The frontend builds into internal/web/dist, which the Go binary embeds via
// go:embed. Asset filenames are content-hashed so the Go server can serve them
// with a long, immutable cache (see internal/web/embed.go).
export default defineConfig({
  plugins: [
    tanstackRouter({ target: "solid", autoCodeSplitting: true }),
    solid(),
    tailwindcss(),
    iconifyOffline({ package: "@iconify-icon/solid" }),
  ],
  resolve: {
    alias: { "~": resolve(import.meta.dirname, "src") },
  },
  build: {
    outDir: resolve(import.meta.dirname, "../internal/web/dist"),
    emptyOutDir: true,
    assetsDir: "assets",
  },
  // Dev-only: proxy API/WS to the running Go server.
  server: {
    proxy: {
      "/api": { target: "http://localhost:8080", changeOrigin: true, ws: true },
    },
  },
});
