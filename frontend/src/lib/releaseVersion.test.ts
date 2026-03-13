import { beforeEach, describe, expect, it } from "vitest";
import {
  getReleaseVersion,
  RELEASE_VERSION_META_NAME,
} from "@/lib/releaseVersion";

describe("getReleaseVersion", () => {
  beforeEach(() => {
    document.head.innerHTML = "";
  });

  it("reads the release version from the portal meta tag", () => {
    document.head.innerHTML = `<meta name="${RELEASE_VERSION_META_NAME}" content=" v2.0.4 " />`;

    expect(getReleaseVersion(document)).toBe("v2.0.4");
  });

  it("returns an empty string when the version meta tag is missing", () => {
    expect(getReleaseVersion(document)).toBe("");
  });
});
