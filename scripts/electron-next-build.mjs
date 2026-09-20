#!/usr/bin/env node
/**
 * Electron static export cannot include Next.js API routes.
 * Stash src/app/api during build, then always restore.
 */
import fs from "node:fs";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const apiDir = path.join(root, "src/app/api");
const manifestPath = path.join(root, ".api-backup-electron-build.json");

// Next 的 `output: "export"` 不接受 Route Handler，构建期间把这些文件临时改名。
// 注意：Windows 上整个 api 目录常被 dev server / 索引服务占用（rename EPERM），
// 但单个文件可以改名，因此采用文件级 stash。
const ROUTE_FILE_RE = /^route\.(ts|tsx|js|jsx|mjs|cjs)$/;
const BAK_SUFFIX = ".electron-build-bak";

function rmrf(target) {
  fs.rmSync(target, { recursive: true, force: true });
}

function sleep(ms) {
  Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, ms);
}

/** Windows 上目录常被索引/杀软/dev server 短暂占用，退避重试 */
function withRetry(label, fn, attempts = 8, delayMs = 600) {
  let lastError;
  for (let i = 1; i <= attempts; i++) {
    try {
      return fn();
    } catch (error) {
      lastError = error;
      if (i < attempts) {
        console.log(`[electron-build] ${label} 被占用，${delayMs}ms 后重试 (${i}/${attempts})`);
        sleep(delayMs);
      }
    }
  }
  throw lastError;
}

function collectRouteFiles(dir) {
  const found = [];
  if (!fs.existsSync(dir)) return found;
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) found.push(...collectRouteFiles(full));
    else if (ROUTE_FILE_RE.test(entry.name)) found.push(full);
  }
  return found;
}

function stashApiRoutes() {
  const files = collectRouteFiles(apiDir);
  fs.writeFileSync(
    manifestPath,
    JSON.stringify(files.map((f) => path.relative(root, f)), null, 2)
  );
  if (files.length === 0) return;
  console.log(`[electron-build] stash ${files.length} 个 Route Handler 文件`);
  for (const file of files) {
    withRetry(`stash ${path.basename(file)}`, () => fs.renameSync(file, file + BAK_SUFFIX));
  }
}

function restoreApiRoutes() {
  if (!fs.existsSync(manifestPath)) return;
  const files = JSON.parse(fs.readFileSync(manifestPath, "utf8")).map((rel) =>
    path.join(root, rel)
  );
  if (files.length > 0) {
    console.log("[electron-build] restore Route Handler 文件");
    for (const file of files) {
      const stashed = file + BAK_SUFFIX;
      if (!fs.existsSync(stashed)) continue;
      withRetry(`restore ${path.basename(file)}`, () => fs.renameSync(stashed, file));
    }
  }
  fs.rmSync(manifestPath, { force: true });
}

stashApiRoutes();
let exitCode = 1;
try {
  const result = spawnSync(
    process.platform === "win32" ? "npx.cmd" : "npx",
    ["cross-env", "ELECTRON=true", "next", "build"],
    {
      cwd: root,
      stdio: "inherit",
      env: process.env,
      shell: process.platform === "win32",
    }
  );
  exitCode = result.status == null ? 1 : result.status;
} finally {
  restoreApiRoutes();
}

process.exit(exitCode);
