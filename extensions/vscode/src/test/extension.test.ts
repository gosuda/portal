import * as assert from "assert";

import {
  buildCommand,
  resolveTunnelBinaryURL,
  validateRelayUrl,
} from "../command";

suite("Extension Test Suite", () => {
  test("validateRelayUrl accepts only https URLs", () => {
    assert.strictEqual(validateRelayUrl("https://relay.example.com"), undefined);
    assert.strictEqual(validateRelayUrl("http://relay.example.com"), "Portal relay URLs must use https://");
    assert.strictEqual(validateRelayUrl("not-a-url"), "Enter a valid https:// URL");
  });

  test("resolveTunnelBinaryURL maps darwin amd64 assets to GitHub releases", () => {
    assert.strictEqual(
      resolveTunnelBinaryURL("darwin", "x64"),
      "https://github.com/gosuda/portal/releases/latest/download/portal-darwin-amd64"
    );
    assert.strictEqual(
      resolveTunnelBinaryURL("win32", "arm64"),
      "https://github.com/gosuda/portal/releases/latest/download/portal-windows-arm64.exe"
    );
    assert.strictEqual(resolveTunnelBinaryURL("freebsd", "x64"), undefined);
  });

  test("buildCommand omits --name when empty and downloads the unix tunnel binary directly", () => {
    const command = buildCommand({
      host: "localhost:3000",
      name: "",
      relayList: "https://relay.example.com",
      thumbnail: "",
      tunnelBinaryURL: "https://github.com/gosuda/portal/releases/latest/download/portal-linux-amd64",
    }, {
      shellTarget: "unix",
      platform: "linux",
      arch: "x64",
    });

    assert.match(command, /curl -fsSL https:\/\/github\.com\/gosuda\/portal\/releases\/latest\/download\/portal-linux-amd64 -o "\$PORTAL_TMP"/);
    assert.match(command, /PORTAL_BIN="\$HOME\/\.local\/bin\/portal"/);
    assert.match(command, /"\$PORTAL_BIN" expose localhost:3000 --relays https:\/\/relay\.example\.com/);
    assert.ok(!command.includes("--name"));
  });

  test("buildCommand can use the default public registry without --relays", () => {
    const command = buildCommand({
      host: "localhost:3000",
      name: "",
      relayList: "",
      thumbnail: "",
      tunnelBinaryURL: "https://github.com/gosuda/portal/releases/latest/download/portal-linux-amd64",
    }, {
      shellTarget: "unix",
      platform: "linux",
      arch: "x64",
    });

    assert.match(command, /curl -fsSL https:\/\/github\.com\/gosuda\/portal\/releases\/latest\/download\/portal-linux-amd64 -o "\$PORTAL_TMP"/);
    assert.ok(!command.includes("--relays"));
    assert.ok(!command.includes("--name"));
    assert.match(command, /"\$PORTAL_BIN" expose localhost:3000/);
  });

  test("buildCommand downloads portal.exe on windows", () => {
    const command = buildCommand({
      host: "localhost:3000",
      name: "my-app",
      relayList: "https://relay.example.com",
      thumbnail: "https://example.com/thumb.png",
      tunnelBinaryURL: "https://github.com/gosuda/portal/releases/latest/download/portal-windows-amd64.exe",
    }, {
      shellTarget: "windows",
      platform: "win32",
      arch: "x64",
    });

    assert.match(command, /Invoke-WebRequest -Uri https:\/\/github\.com\/gosuda\/portal\/releases\/latest\/download\/portal-windows-amd64\.exe -OutFile \$PortalTmp/);
    assert.match(command, /\$PortalBin = Join-Path \$PortalDir 'portal\.exe'/);
    assert.match(command, /& \$PortalBin expose localhost:3000 --name my-app --relays https:\/\/relay\.example\.com --thumbnail https:\/\/example\.com\/thumb\.png/);
  });
});
