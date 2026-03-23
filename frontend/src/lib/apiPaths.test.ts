import { describe, expect, it } from "vitest";

import { API_PATHS, adminLeasePath, encodeLeaseID } from "@/lib/apiPaths";

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

  it("encodes lease IDs as base64url path segments", () => {
    const leaseId = "peer:legacy/123";
    const expected = Buffer.from(leaseId)
      .toString("base64")
      .replace(/\+/g, "-")
      .replace(/\//g, "_")
      .replace(/=+$/, "");
    const encoded = encodeLeaseID(leaseId);

    expect(encoded).toBe(expected);
    expect(encoded).not.toContain("=");
    expect(adminLeasePath(encoded, "approve")).toBe(
      `${API_PATHS.admin.leases}/${encodeURIComponent(encoded)}/approve`
    );
  });

  it("keeps install script endpoints aligned", () => {
    expect(API_PATHS.install).toEqual({
      shell: "/install.sh",
      powershell: "/install.ps1",
    });
  });
});
