import { describe, expect, it } from "vitest";

import { API_PATHS } from "@/lib/apiPaths";

describe("API_PATHS contract alignment", () => {
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

  it("keeps tunnel installer endpoint aligned", () => {
    expect(API_PATHS.tunnel).toBe("/tunnel");
  });
});
