// Bundles the server into dist/server.js (no node_modules needed at run
// time) and writes dist/bin/shfd-gui and dist/VERSION. `vite build` has
// already put the web app into dist/web.

import { execSync } from "node:child_process";
import { chmodSync, mkdirSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { build } from "esbuild";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const dist = join(root, "dist");

await build({
  entryPoints: [join(root, "server/src/main.ts")],
  outfile: join(dist, "server.js"),
  bundle: true,
  platform: "node",
  target: "node22",
  format: "esm",
  sourcemap: true,
  legalComments: "linked",
  // CommonJS dependencies (fastify) call require(); give the ESM bundle one.
  banner: { js: "import { createRequire as __cr } from 'node:module'; const require = __cr(import.meta.url);" },
  // Optional peers of @jsudc/server that shfd-gui does not use.
  external: ["pg", "better-sqlite3", "@fastify/swagger", "@fastify/swagger-ui", "fastify-type-provider-zod", "node:sqlite"],
  logLevel: "warning",
});

let version = process.env.VERSION;
if (!version) {
  try {
    version = execSync("git describe --tags --always --dirty", { cwd: root }).toString().trim();
  } catch {
    version = "dev";
  }
}
writeFileSync(join(dist, "VERSION"), version + "\n");

mkdirSync(join(dist, "bin"), { recursive: true });
const bin = join(dist, "bin", "shfd-gui");
writeFileSync(
  bin,
  `#!/bin/sh
# shfd-gui: the web GUI of shfd (docs/gui.md). Needs Node 22 or newer.
here=$(dirname "$(readlink -f "$0")")
exec "\${NODE:-node}" "$here/../server.js" "$@"
`,
);
chmodSync(bin, 0o755);
console.log(`dist/ ready (${version})`);
