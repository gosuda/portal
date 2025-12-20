const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const crypto = require("node:crypto");

test("wasm_exec.js defines Go runtime", () => {
  const code = fs.readFileSync(path.join(__dirname, "wasm_exec.js"), "utf8");
  const ctx = {
    console,
    setTimeout,
    clearTimeout,
    TextEncoder,
    TextDecoder,
    crypto,
    performance,
  };
  ctx.globalThis = ctx;

  vm.runInNewContext(code, ctx, { filename: "wasm_exec.js" });
  assert.equal(typeof ctx.Go, "function");
});
