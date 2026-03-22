import { describe, expect, it } from "vitest";

import {
  buildTunnelCommand,
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
      enableUDP: false,
      udpAddr: "",
      os: "unix" as const,
    };

    const command = buildTunnelCommand(options);

    expect(command).toBe(
      [
        "curl -ksSL https://localhost:4017/install.sh | bash",
        "portal expose --name my-app --relays https://localhost:4017 3000",
      ].join("\n")
    );
    expect(command).not.toContain(" \\\n");
    expect(command).not.toContain("\n  --name");
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

  it("includes --udp and --udp-addr when UDP is enabled with an explicit address", () => {
    const command = buildTunnelCommand({
      currentOrigin: "https://relay.example.com",
      target: "3000",
      name: "game-server",
      nameSeed: "web_portal",
      relayUrls: [],
      defaultRelays: true,
      thumbnailURL: "",
      enableUDP: true,
      udpAddr: "127.0.0.1:7777",
      os: "unix" as const,
    });

    expect(command).toContain("--udp");
    expect(command).toContain("--udp-addr 127.0.0.1:7777");
  });

  it("defaults --udp-addr to target when UDP is enabled but udpAddr is blank", () => {
    const command = buildTunnelCommand({
      currentOrigin: "https://relay.example.com",
      target: "3000",
      name: "game-server",
      nameSeed: "web_portal",
      relayUrls: [],
      defaultRelays: true,
      thumbnailURL: "",
      enableUDP: true,
      udpAddr: "",
      os: "unix" as const,
    });

    expect(command).toContain("--udp");
    expect(command).toContain("--udp-addr 3000");
  });
});
