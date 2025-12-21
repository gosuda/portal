const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

function loadServiceWorker(overrides = {}) {
  const handlers = {};
  const ctx = {
    location: {
      origin: "https://example.com",
      protocol: "https:",
      hostname: "example.com",
    },
    navigator: {
      userAgent: "Mozilla/5.0",
    },
    clients: {
      claim: async () => {},
      matchAll: async () => [],
    },
    caches: {
      keys: async () => [],
      delete: async () => true,
    },
    importScripts: () => {},
    addEventListener: (type, handler) => {
      handlers[type] = handler;
    },
    setInterval: () => 0,
    clearInterval: () => {},
    setTimeout,
    clearTimeout,
    console,
    URL,
    TextEncoder,
    TextDecoder,
    __PORTAL_SW_TEST__: {},
    fetch: async () => {
      throw new Error("fetch not stubbed");
    },
    ...overrides,
  };

  ctx.self = ctx;
  ctx._handlers = handlers;

  const code = fs.readFileSync(
    path.join(__dirname, "service-worker.js"),
    "utf8"
  );
  vm.runInNewContext(code, ctx, { filename: "service-worker.js" });
  return ctx;
}

test("resolveWasmURL prefers local wasmFile", () => {
  const ctx = loadServiceWorker();
  const resolve = ctx.__PORTAL_SW_TEST__.resolveWasmURL;
  assert.equal(resolve({ wasmFile: "hash.wasm" }), "/frontend/hash.wasm");
});

test("resolveWasmURL accepts https wasmUrl", () => {
  const ctx = loadServiceWorker();
  const resolve = ctx.__PORTAL_SW_TEST__.resolveWasmURL;
  assert.equal(
    resolve({ wasmFile: "hash.wasm", wasmUrl: "https://cdn.example.com/a.wasm" }),
    "https://cdn.example.com/a.wasm"
  );
});

test("resolveWasmURL rejects mixed-content http", () => {
  const ctx = loadServiceWorker();
  const resolve = ctx.__PORTAL_SW_TEST__.resolveWasmURL;
  assert.equal(
    resolve({ wasmFile: "hash.wasm", wasmUrl: "http://cdn.example.com/a.wasm" }),
    "/frontend/hash.wasm"
  );
});

test("resolveWasmURL accepts same-origin relative url", () => {
  const ctx = loadServiceWorker();
  const resolve = ctx.__PORTAL_SW_TEST__.resolveWasmURL;
  assert.equal(
    resolve({ wasmFile: "hash.wasm", wasmUrl: "/frontend/alt.wasm" }),
    "https://example.com/frontend/alt.wasm"
  );
});

test("resolveWasmURL throws when manifest missing", () => {
  const ctx = loadServiceWorker();
  const resolve = ctx.__PORTAL_SW_TEST__.resolveWasmURL;
  assert.throws(() => resolve({}), /missing wasmFile\/wasmUrl/);
});

test("fetchWithRetry retries then succeeds", async () => {
  let attempts = 0;
  const ctx = loadServiceWorker({
    fetch: async () => {
      attempts += 1;
      if (attempts < 3) {
        throw new Error("network");
      }
      return { ok: true, status: 200, statusText: "OK" };
    },
  });

  const resp = await ctx.__PORTAL_SW_TEST__.fetchWithRetry("x", {}, 3);
  assert.equal(resp.ok, true);
  assert.equal(attempts, 3);
});

test("fetchWithRetry stops on 404", async () => {
  let attempts = 0;
  const ctx = loadServiceWorker({
    fetch: async () => {
      attempts += 1;
      return { ok: false, status: 404, statusText: "Not Found" };
    },
  });

  await assert.rejects(
    () => ctx.__PORTAL_SW_TEST__.fetchWithRetry("x", {}, 3),
    /404/
  );
  assert.equal(attempts, 1);
});

test("isRecoverableError flags fatal errors", () => {
  const ctx = loadServiceWorker();
  const isRecoverable = ctx.__PORTAL_SW_TEST__.isRecoverableError;
  const { ReadinessStage } = ctx.__PORTAL_SW_TEST__;
  ctx.__PORTAL_SW_TEST__.setHandlers(() => {}, () => {});
  const fatal = new Error("bad magic number");
  assert.equal(
    isRecoverable(fatal, ReadinessStage.UNINITIALIZED, ReadinessStage.READY),
    false
  );
});

test("isRecoverableError flags transient errors", () => {
  const ctx = loadServiceWorker();
  const isRecoverable = ctx.__PORTAL_SW_TEST__.isRecoverableError;
  const { ReadinessStage } = ctx.__PORTAL_SW_TEST__;
  const transient = new Error("network timeout");
  assert.equal(
    isRecoverable(transient, ReadinessStage.UNINITIALIZED, ReadinessStage.READY),
    true
  );
});
