const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const crypto = require("node:crypto");

class SimpleEventTarget {
  constructor() {
    this._listeners = new Map();
  }
  addEventListener(type, handler) {
    if (!this._listeners.has(type)) {
      this._listeners.set(type, new Set());
    }
    this._listeners.get(type).add(handler);
  }
  removeEventListener(type, handler) {
    const set = this._listeners.get(type);
    if (set) {
      set.delete(handler);
    }
  }
  dispatchEvent(event) {
    const set = this._listeners.get(event.type);
    if (set) {
      for (const handler of set) {
        handler.call(this, event);
      }
    }
    return true;
  }
}

function loadPolyfill(origin = "https://example.com") {
  const calls = { removed: false, nativeArgs: [] };

  class NativeWebSocket {
    constructor(url, protocols) {
      calls.nativeArgs.push([url, protocols]);
      this.url = url;
    }
  }

  const window = {
    location: {
      href: origin + "/",
      origin,
      hostname: new URL(origin).hostname,
    },
    WebSocket: NativeWebSocket,
  };

  const navigator = {
    serviceWorker: {
      controller: {
        postMessage: () => {},
      },
      addEventListener: () => {},
      ready: Promise.resolve(),
    },
  };

  const document = {
    currentScript: {
      parentNode: {
        removeChild: () => {
          calls.removed = true;
        },
      },
    },
  };

  const ctx = {
    window,
    navigator,
    document,
    console,
    URL,
    DOMException,
    EventTarget: SimpleEventTarget,
    TextEncoder,
    TextDecoder,
    btoa: (str) => Buffer.from(str, "binary").toString("base64"),
    atob: (str) => Buffer.from(str, "base64").toString("binary"),
    crypto: {
      getRandomValues: (arr) => {
        crypto.randomFillSync(arr);
        return arr;
      },
    },
    setTimeout,
    clearTimeout,
  };

  window.navigator = navigator;
  window.crypto = ctx.crypto;

  const code = fs.readFileSync(path.join(__dirname, "polyfill.js"), "utf8");
  vm.runInNewContext(code, ctx, { filename: "polyfill.js" });

  return { ctx, calls, NativeWebSocket };
}

test("polyfill replaces WebSocket and keeps constants", () => {
  const { ctx } = loadPolyfill();
  assert.equal(typeof ctx.window.WebSocket, "function");
  assert.equal(ctx.window.WebSocket.CONNECTING, 0);
  assert.equal(ctx.window.WebSocket.OPEN, 1);
  assert.equal(ctx.window.WebSocket.CLOSING, 2);
  assert.equal(ctx.window.WebSocket.CLOSED, 3);
});

test("polyfill uses E2EE WebSocket for same-origin", () => {
  const { ctx } = loadPolyfill("https://example.com");
  const ws = new ctx.window.WebSocket("wss://example.com/socket");
  assert.equal(typeof ws._clientId, "string");
  assert.equal(ws.readyState, 0);
});

test("polyfill uses native WebSocket for cross-origin", () => {
  const { ctx, NativeWebSocket } = loadPolyfill("https://example.com");
  const ws = new ctx.window.WebSocket("wss://other.example.com/socket");
  assert.equal(ws instanceof NativeWebSocket, true);
});

test("polyfill validates scheme", () => {
  const { ctx } = loadPolyfill("http://example.com");
  assert.throws(
    () => new ctx.window.WebSocket("http://example.com"),
    /scheme must be either/
  );
});
