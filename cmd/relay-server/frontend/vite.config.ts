import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { resolve } from "path";
import { renameSync } from "fs";

// https://vitejs.dev/config/
export default defineConfig({
  plugins: [
    react(),
    tailwindcss(),
    {
      name: "rename-index",
      closeBundle() {
        const appDir = resolve(process.cwd(), "../app");
        const indexPath = resolve(appDir, "index.html");
        const portalPath = resolve(appDir, "portal.html");
        try {
          renameSync(indexPath, portalPath);
          console.log("✓ Renamed index.html to portal.html");
        } catch (err) {
          console.error("Failed to rename index.html:", err);
        }
      },
    },
  ],
  resolve: {
    alias: {
      "@": resolve(process.cwd(), "./src"),
    },
  },
  build: {
    outDir: "../app",
    emptyOutDir: false,
    rollupOptions: {
      output: {
        manualChunks: undefined,
      },
    },
  },
});
