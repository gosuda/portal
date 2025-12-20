const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");

test("index.html includes service worker registration", () => {
  const html = fs.readFileSync(path.join(__dirname, "index.html"), "utf8");
  assert.match(html, /serviceWorker\.register/);
  assert.match(html, /service-worker\.js/);
});

test("index.html includes WASM health check endpoint", () => {
  const html = fs.readFileSync(path.join(__dirname, "index.html"), "utf8");
  assert.match(html, /e8c2c70c-ec4a-40b2-b8af-d5638264f831/);
});
