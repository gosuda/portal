import { describe, expect, it } from "vitest";

import { API_PATHS, adminLeasePath, encodePathPart } from "@/lib/apiPaths";

describe("API_PATHS contract alignment", () => {
  it("keeps admin snapshot path aligned", () => {
    expect(API_PATHS.admin.snapshot).toBe("/admin/snapshot");
    expect(API_PATHS.admin.landingPage).toBe("/admin/settings/landing-page");
  });

  it("keeps sdk endpoint paths aligned", () => {
    expect(API_PATHS.sdk).toEqual({
      prefix: "/sdk",
      register: "/sdk/register",
      unregister: "/sdk/unregister",
      renew: "/sdk/renew",
      domain: "/sdk/domain",
      connect: "/sdk/connect",
    });
  });

  it("encodes lease identities as base64url path segments", () => {
    const name = "relay-1";
    const address = "0x00000000000000000000000000000000000000A1";
    const expectedName = Buffer.from(name)
      .toString("base64")
      .replace(/\+/g, "-")
      .replace(/\//g, "_")
      .replace(/=+$/, "");
    const expectedAddress = Buffer.from(address)
      .toString("base64")
      .replace(/\+/g, "-")
      .replace(/\//g, "_")
      .replace(/=+$/, "");
    const encodedName = encodePathPart(name);
    const encodedAddress = encodePathPart(address);

    expect(encodedName).toBe(expectedName);
    expect(encodedAddress).toBe(expectedAddress);
    expect(encodedAddress).not.toContain("=");
    expect(adminLeasePath(name, address, "approve")).toBe(
      `${API_PATHS.admin.leases}/${encodeURIComponent(encodedName)}/${encodeURIComponent(encodedAddress)}/approve`
    );
  });

  it("keeps install script endpoints aligned", () => {
    expect(API_PATHS.install).toEqual({
      shell: "/install.sh",
      powershell: "/install.ps1",
    });
  });
});
