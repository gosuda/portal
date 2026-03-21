import { describe, expect, it } from "vitest";

import {
  buildTunnelCommand,
  buildTunnelDisplayCommand,
  buildTunnelPreviewURL,
} from "@/lib/tunnelCommand";

describe("tunnelCommand", () => {
  it("keeps copied unix commands flat and directly pasteable", () => {
    const options = {
      currentOrigin: "https://localhost:4017",
      target: "3000",
      name: "My App",
      nameSeed: "web_portal",
      relayUrls: ["https://localhost:4017"],
      defaultRelays: true,
      thumbnailURL: "",
      os: "unix" as const,
    };

    const command = buildTunnelCommand(options);

    expect(command).toBe(
      [
        "curl -ksSL https://localhost:4017/install.sh | bash",
        "portal expose 3000 --name my-app --relays https://localhost:4017",
      ].join("\n")
    );
    expect(command).not.toContain(" \\\n");
    expect(command).not.toContain("\n  --name");
  });

  it("wraps display commands before relays while keeping copied commands flat", () => {
    const options = {
      currentOrigin: "https://relay.example.com",
      target: "localhost:3000",
      name: "my-app",
      nameSeed: "web_portal",
      relayUrls: ["https://relay.example.com"],
      defaultRelays: false,
      thumbnailURL: "https://example.com/thumb.png",
      os: "windows" as const,
    };

    expect(buildTunnelCommand(options)).toBe(
      [
        `$ProgressPreference = 'SilentlyContinue'`,
        `irm https://relay.example.com/install.ps1 | iex`,
        `portal expose localhost:3000 --name my-app --relays https://relay.example.com --default-relays=false --thumbnail https://example.com/thumb.png`,
      ].join("\n")
    );
    expect(buildTunnelDisplayCommand(options)).toBe(
      [
        `$ProgressPreference = 'SilentlyContinue'`,
        `irm https://relay.example.com/install.ps1 | iex`,
        `portal expose localhost:3000 --name my-app`,
        `--relays https://relay.example.com --default-relays=false --thumbnail https://example.com/thumb.png`,
      ].join("\n")
    );
  });

  it("uses the relay root host for preview URLs instead of a placeholder host", () => {
    expect(
      buildTunnelPreviewURL(
        "https://localhost:4017",
        "my-app",
        "3000",
        "web_portal"
      )
    ).toBe("https://my-app.localhost");

    expect(
      buildTunnelPreviewURL(
        "https://portal.example.com",
        "my-app",
        "3000",
        "web_portal"
      )
    ).toBe("https://my-app.portal.example.com");
  });
});
