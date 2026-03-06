import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { resolve } from "path";
import { existsSync, renameSync } from "fs";

// https://vitejs.dev/config/
export default defineConfig({
  plugins: [
    react({
        babel: {
            plugins: [
                ["babel-plugin-react-compiler", {}]
            ]
        }
    }),
    tailwindcss(),
    {
      name: "rename-index",
      closeBundle() {
        if (process.env.VITEST) {
          return;
        }

        const appDir = resolve(process.cwd(), "../cmd/relay-server/dist/app");
        const indexPath = resolve(appDir, "index.html");
        const portalPath = resolve(appDir, "portal.html");

        if (!existsSync(indexPath)) {
          return;
        }

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
    outDir: "../cmd/relay-server/dist/app",
    emptyOutDir: false,
    rollupOptions: {
      output: {
        manualChunks: undefined,
      },
    },
  },
  test: {
    globals: true,
    environment: "jsdom",
    setupFiles: "./src/test/setup.ts",
    include: ["src/**/*.test.ts", "src/**/*.test.tsx"],
  },
});
