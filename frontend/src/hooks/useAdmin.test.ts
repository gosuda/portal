import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { ServerData } from "@/hooks/useSSRData";
import { useAdmin } from "@/hooks/useAdmin";
import { API_PATHS, adminLeasePath, encodeLeaseID } from "@/lib/apiPaths";
import { APIClientError, apiClient } from "@/lib/apiClient";

vi.mock("@/hooks/useList", () => ({
  useList: vi.fn(() => ({
    searchQuery: "",
    status: "all",
    sortBy: "default",
    selectedTags: [],
    favorites: [],
    availableTags: [],
    filteredServers: [],
    handleSearchChange: vi.fn(),
    handleStatusChange: vi.fn(),
    handleSortByChange: vi.fn(),
    handleTagToggle: vi.fn(),
    handleToggleFavorite: vi.fn(),
  })),
}));

vi.mock("@/lib/apiClient", async () => {
  const actual = await vi.importActual<typeof import("@/lib/apiClient")>(
    "@/lib/apiClient",
  );

  return {
    ...actual,
    apiClient: {
      get: vi.fn(),
      post: vi.fn(),
      delete: vi.fn(),
    },
  };
});

function buildLease(peer: string): ServerData {
  return {
    Peer: peer,
    Name: "relay-1",
    Kind: "relay",
    Connected: true,
    DNS: "relay.example.com",
    LastSeen: "2026-03-03T00:00:00Z",
    LastSeenISO: "2026-03-03T00:00:00Z",
    FirstSeenISO: "2026-03-02T00:00:00Z",
    TTL: "1h",
    Link: "https://relay.example.com",
    StaleRed: false,
    Hide: false,
    Metadata: JSON.stringify({
      description: "relay",
      tags: ["core"],
      thumbnail: "",
      owner: "ops",
      hide: false,
    }),
    BPS: 1024,
    IsApproved: true,
    IsDenied: false,
    IP: "203.0.113.10",
    IsIPBanned: false,
  };
}

async function waitForLoaded(result: { current: { loading: boolean } }) {
  await waitFor(() => {
    expect(result.current.loading).toBe(false);
  });
}

describe("useAdmin", () => {
  const mockGet = vi.mocked(apiClient.get);
  const mockPost = vi.mocked(apiClient.post);
  const mockDelete = vi.mocked(apiClient.delete);

  beforeEach(() => {
    vi.clearAllMocks();

    mockGet.mockImplementation(async (path: string) => {
      if (path === API_PATHS.admin.snapshot) {
        return {
          leases: [buildLease("peer-a")],
          banned_leases: ["peer-a", "peer-a", "peer-b"],
          approval_mode: "not-a-mode",
        } as never;
      }
      throw new Error(`Unexpected GET path: ${path}`);
    });

    mockPost.mockResolvedValue({} as never);
    mockDelete.mockResolvedValue({} as never);
  });

  it("normalizes fetchData results on success", async () => {
    const { result } = renderHook(() => useAdmin());

    await waitForLoaded(result);

    expect(result.current.error).toBe("");
    expect(result.current.approvalMode).toBe("auto");
    expect(result.current.bannedLeases).toEqual(["peer-a", "peer-b"]);
    expect(result.current.servers[0]?.peerId).toBe("peer-a");
  });

  it("surfaces fetchData API errors", async () => {
    mockGet.mockImplementation(async (path: string) => {
      if (path === API_PATHS.admin.snapshot) {
        throw new APIClientError("failed to load leases", 500, "server_error");
      }
      throw new Error(`Unexpected GET path: ${path}`);
    });

    const { result } = renderHook(() => useAdmin());

    await waitForLoaded(result);

    expect(result.current.error).toBe("failed to load leases");
  });

  it("maps contract error codes to resilient admin messages", async () => {
    const { result } = renderHook(() => useAdmin());
    await waitForLoaded(result);

    mockPost.mockRejectedValueOnce(
      new APIClientError("request failed", 400, "invalid_mode"),
    );

    await act(async () => {
      await expect(result.current.handleApprovalModeChange("manual")).rejects.toBeInstanceOf(
        APIClientError,
      );
    });

    await waitFor(() => {
      expect(result.current.error).toBe(
        "Invalid approval mode. Choose auto or manual and retry.",
      );
    });
  });

  it("validates missing IP in handleIPBanStatus", async () => {
    const { result } = renderHook(() => useAdmin());
    await waitForLoaded(result);

    await act(async () => {
      await expect(result.current.handleIPBanStatus("   ", true)).rejects.toThrow(
        "Missing IP address",
      );
    });
    await waitFor(() => {
      expect(result.current.error).toContain("Missing IP address");
    });
  });

  it("encodes peer IDs for action routes", async () => {
    const { result } = renderHook(() => useAdmin());
    await waitForLoaded(result);
    const plainLeaseID = "deadbeefcafebabe";

    await act(async () => {
      await result.current.handleApproveStatus(plainLeaseID, true);
    });

    const calledPaths = mockPost.mock.calls.map(([path]) => path as string);
    expect(calledPaths).toContain(
      adminLeasePath(encodeLeaseID(plainLeaseID), "approve"),
    );
  });

  it("bulk deny posts deduped lease IDs to action routes", async () => {
    const { result } = renderHook(() => useAdmin());
    await waitForLoaded(result);
    const normalizedPeerA = encodeLeaseID("peer-a");
    const normalizedPeerB = encodeLeaseID("peer-b");

    await act(async () => {
      await result.current.handleBulkDeny([
        "peer-a",
        "peer-a",
        "peer-b",
      ]);
    });

    const calledPaths = mockPost.mock.calls.map(([path]) => path as string);
    expect(calledPaths).toEqual(
      expect.arrayContaining([
        adminLeasePath(normalizedPeerA, "deny"),
        adminLeasePath(normalizedPeerB, "deny"),
      ]),
    );

    const denyCalls = calledPaths.filter((path) => path.endsWith("/deny"));
    expect(denyCalls).toHaveLength(2);
  });
});
