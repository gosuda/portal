import { describe, expect, it } from "vitest";
import { buildDefaultExposeName } from "@/lib/exposeName";

// Cross-language parity vectors -- keep in sync with utils/identity_test.go
const exposeNameVectors = [
  { target: "3000", seed: "test_seed", expected: "bubble-cricket-beacon" },
  { target: "", seed: "portal", expected: "zesty-beacon-sketch" },
  { target: "http://localhost:8080", seed: "cli_abc", expected: "sprightly-rocket-zap" },
  { target: "192.168.1.1:8080", seed: "web_xyz", expected: "velvet-yeti-march" },
  { target: "localhost", seed: "cli_", expected: "misty-rocket-ripple" },
] as const;

describe("buildDefaultExposeName", () => {
  it.each(exposeNameVectors)(
    "generates $expected for target=$target seed=$seed",
    ({ target, seed, expected }) => {
      expect(buildDefaultExposeName(target, seed)).toBe(expected);
    },
  );
});
