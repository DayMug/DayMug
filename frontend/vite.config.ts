/// <reference types="vitest/config" />
import { defineConfig, type Plugin } from "vite";
import vue from "@vitejs/plugin-vue";
import tailwindcss from "@tailwindcss/vite";
import { existsSync, readdirSync, readFileSync } from "fs";
import { extname, join, resolve } from "path";
import { execSync } from "child_process";

/**
 * Serve the CasualOffice embeds in dev.
 *
 * Their JS/CSS exist in `public/casual-office/` as **`.gz` only** (28MB raw
 * down to 12MB — see `scripts/sync-casual-office.mjs`). In production the Go
 * static handler picks the representation from `Accept-Encoding`; Vite knows
 * nothing about that convention, so without this the iframe just 404s in
 * `pnpm dev`. The two rules have to stay in step or a change works in one and
 * breaks in the other.
 */
function casualOfficeGzip(): Plugin {
  return {
    name: "daymug:casual-office-gzip",
    configureServer(server) {
      server.middlewares.use((req, res, next) => {
        const urlPath = (req.url ?? "").split("?")[0] ?? "";
        if (!urlPath.startsWith("/casual-office/")) return next();

        const file = join(server.config.publicDir, urlPath.slice(1));
        if (existsSync(file) || !existsSync(`${file}.gz`)) return next();

        const ext = extname(file);
        res.setHeader(
          "Content-Type",
          ext === ".css" ? "text/css" : "text/javascript; charset=utf-8",
        );
        res.setHeader("Content-Encoding", "gzip");
        res.end(readFileSync(`${file}.gz`));
      });
    },
  };
}

/**
 * Emit `third-party-licenses.txt` next to the bundle.
 *
 * The release binary embeds `dist/`, so every npm package Vite bundled (fonts
 * and icon sets included) ships in it, and their MIT/BSD/OFL terms require the
 * license text to travel with the copy. The list is derived from the modules
 * the build actually pulled in, plus what reaches `dist/` outside the module
 * graph: the CasualOffice embeds and fonts `scripts/sync-casual-office.mjs`
 * copies into `public/`, and Inter, which index.css `@import`s through the CSS
 * pipeline rather than as a module.
 */
function thirdPartyLicenses(): Plugin {
  const outsideGraph = [
    "@casualoffice/sheets",
    "@casualoffice/docs",
    "@fontsource-variable/inter",
    "@material-symbols/font-400",
  ];
  const licenseFile = /^(licen[cs]e|copying|notice)(\.|$)/i;

  return {
    name: "daymug:third-party-licenses",
    apply: "build",
    generateBundle() {
      const dirs = new Set<string>();
      for (const id of this.getModuleIds()) {
        const m = id.replace(/\\/g, "/").match(/^(.*\/node_modules\/(?:@[^/]+\/)?[^/]+)\//);
        if (m?.[1]) dirs.add(m[1]);
      }
      for (const name of outsideGraph) {
        dirs.add(join(__dirname, "node_modules", name));
      }

      const entries = new Map<string, string>();
      for (const dir of dirs) {
        const pkgPath = join(dir, "package.json");
        if (!existsSync(pkgPath)) continue;
        const pkg = JSON.parse(readFileSync(pkgPath, "utf-8")) as {
          name?: string;
          version?: string;
          license?: string;
        };
        if (!pkg.name) continue;
        const key = `${pkg.name}@${pkg.version ?? ""}`;
        if (entries.has(key)) continue;
        const texts = readdirSync(dir)
          .filter((f) => licenseFile.test(f))
          .sort()
          .map((f) => readFileSync(join(dir, f), "utf-8").trim());
        entries.set(
          key,
          [`${key}`, `License: ${pkg.license ?? "see below"}`, "", ...texts].join("\n"),
        );
      }

      const header =
        "Third-party software bundled into the DayMug web interface.\n" +
        "Generated at build time from the modules the bundle contains.\n";
      const body = [...entries.keys()]
        .sort()
        .map((key) => entries.get(key))
        .join(`\n\n${"-".repeat(78)}\n\n`);
      this.emitFile({
        type: "asset",
        fileName: "third-party-licenses.txt",
        source: `${header}\n${"=".repeat(78)}\n\n${body}\n`,
      });
    },
  };
}

function getAppVersion(): string {
  if (process.env.APP_VERSION) return process.env.APP_VERSION;
  try {
    return execSync("git describe --tags --always --dirty", { encoding: "utf-8" }).trim();
  } catch {
    return "dev";
  }
}

// Keep DOM-free tests out of happy-dom. Each entry is intentionally explicit:
// adding a file here is a reviewed assertion that it does not rely on browser
// globals or the shared Vue/i18n test setup.
const nodeTestFiles = [
  "src/composables/chat/messageState.test.ts",
  "src/composables/slashCommands.test.ts",
  "src/composables/useAsyncOperation.test.ts",
  "src/lib/avatar.test.ts",
  "src/lib/chatFormat.test.ts",
  "src/lib/displayPath.test.ts",
  "src/lib/fileName.test.ts",
  "src/lib/format.test.ts",
  "src/lib/relPath.test.ts",
  "src/lib/uploadHelpers.test.ts",
  "src/i18n/parity.test.ts",
  "src/test/fetchRouter.test.ts",
  "src/lib/office/csv.test.ts",
  "scripts/sync-casual-office.test.ts",
];

// These exercise real spreadsheet parsers/writers and are useful integration
// coverage, but they should not inflate the fast unit-test feedback loop.
const integrationTestFiles = ["src/lib/office/csvbridge.test.ts"];

const allTestFiles = ["src/**/*.{test,spec}.{ts,tsx}"];

export default defineConfig({
  plugins: [vue(), tailwindcss(), casualOfficeGzip(), thirdPartyLicenses()],
  define: {
    __APP_VERSION__: JSON.stringify(getAppVersion()),
  },
  resolve: {
    alias: {
      "@": resolve(__dirname, "src"),
    },
  },
  server: {
    port: 5174,
    proxy: {
      "/api": {
        // 8090 is what `daymug init` / `bootstrap` write into server.addr, so
        // a freshly initialised dev backend needs no edit. Not the compiled
        // :8080 fallback, which only applies when a config omits addr.
        // 127.0.0.1 rather than localhost: init binds IPv4 loopback only, and
        // Node may resolve localhost to ::1 first.
        target: process.env.DAYMUG_DEV_BACKEND || "http://127.0.0.1:8090",
        changeOrigin: true,
        ws: true,
      },
    },
  },
  test: {
    projects: [
      {
        extends: true,
        test: {
          name: "node",
          environment: "node",
          include: nodeTestFiles,
          isolate: false,
          pool: "forks",
          maxWorkers: 3,
        },
      },
      {
        extends: true,
        test: {
          name: "ui",
          environment: "happy-dom",
          include: allTestFiles,
          exclude: [...nodeTestFiles, ...integrationTestFiles],
          css: {
            include: /src\/assets\/index\.css/,
          },
          setupFiles: ["src/test/setup.ts"],
          pool: "forks",
          maxWorkers: 3,
        },
      },
      {
        extends: true,
        test: {
          name: "integration",
          environment: "node",
          include: integrationTestFiles,
          pool: "forks",
          maxWorkers: 1,
        },
      },
    ],
  },
});
