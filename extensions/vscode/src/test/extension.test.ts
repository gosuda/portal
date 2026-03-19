import * as assert from "assert";

import { buildCommand, validateRelayUrl } from "../command";
import { buildDefaultExposeName } from "../../../../utils/exposeName";

suite("Extension Test Suite", () => {
  test("validateRelayUrl accepts only https URLs", () => {
    assert.strictEqual(validateRelayUrl("https://relay.example.com"), undefined);
    assert.strictEqual(validateRelayUrl("http://relay.example.com"), "Portal relay URLs must use https://");
    assert.strictEqual(validateRelayUrl("not-a-url"), "Enter a valid https:// URL");
  });

  test("buildCommand generates --name when empty and resolves unix portal binary after install", () => {
    const generatedName = buildDefaultExposeName("localhost:3000", "machine-seed");
    const command = buildCommand({
      host: "localhost:3000",
      name: "",
      nameSeed: "machine-seed",
      relayList: "https://relay.example.com",
      relayUrl: "https://relay.example.com",
      thumbnail: "",
      isLocal: false,
    }, "unix");

    assert.match(command, /curl -fsSL https:\/\/relay\.example\.com\/install\.sh \| bash/);
    assert.match(command, /PORTAL_BIN="\$\(command -v portal 2>\/dev\/null \|\| true\)"/);
    assert.match(
      command,
      new RegExp(`"\\$PORTAL_BIN" expose --name ${generatedName} --relays https://relay\\.example\\.com localhost:3000`)
    );
  });

  test("buildCommand can use the default public registry without --relays", () => {
    const generatedName = buildDefaultExposeName("localhost:3000", "machine-seed");
    const command = buildCommand({
      host: "localhost:3000",
      name: "",
      nameSeed: "machine-seed",
      relayList: "",
      relayUrl: "",
      thumbnail: "",
      isLocal: false,
    }, "unix");

    assert.ok(!command.includes("/install.sh"));
    assert.ok(!command.includes("--relays"));
    assert.match(command, /portal CLI not found\. Install from a relay first or configure portal\.relayUrls\./);
    assert.match(
      command,
      new RegExp(`"\\$PORTAL_BIN" expose --name ${generatedName} localhost:3000`)
    );
  });

  test("buildCommand uses explicit portal.exe path on windows", () => {
    const command = buildCommand({
      host: "localhost:3000",
      name: "my-app",
      nameSeed: "machine-seed",
      relayList: "https://relay.example.com",
      relayUrl: "https://relay.example.com",
      thumbnail: "https://example.com/thumb.png",
      isLocal: false,
    }, "windows");

    assert.match(command, /irm https:\/\/relay\.example\.com\/install\.ps1 \| iex/);
    assert.match(command, /\$PortalBin = Join-Path \$env:LOCALAPPDATA 'portal\\bin\\portal\.exe'/);
    assert.match(command, /& \$PortalBin expose --name my-app --relays https:\/\/relay\.example\.com --thumbnail https:\/\/example\.com\/thumb\.png localhost:3000/);
  });
});
