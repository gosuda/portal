import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { ServerData } from "@/hooks/useSSRData";
import { useAdmin } from "@/hooks/useAdmin";
import { API_PATHS, adminLeasePath, encodeLeaseID } from "@/lib/apiPaths";
import { APIClientError, apiClient } from "@/lib/apiClient";

type DeferredAdminSnapshot = {
  leases: ServerData[];
  approval_mode: "auto" | "manual";
};

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
    ExpiresAt: "2026-03-03T01:00:00Z",
    FirstSeenAt: "2026-03-02T00:00:00Z",
    LastSeenAt: "2026-03-03T00:00:00Z",
    ID: peer,
    Name: "relay-1",
    BPS: 1024,
    ClientIP: "203.0.113.10",
    Hostname: "relay.example.com",
    Metadata: {
      description: "relay",
      tags: ["core"],
      thumbnail: "",
      owner: "ops",
      hide: false,
    },
    Ready: 1,
    IsApproved: true,
    IsBanned: peer === "peer-a",
    IsDenied: false,
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
    expect(result.current.servers[0]?.peerId).toBe("peer-a");
    expect(result.current.servers[0]?.isBanned).toBe(true);
    expect(result.current.servers[0]?.bps).toBe(1024);
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

  it("posts bps updates to the lease action route", async () => {
    const { result } = renderHook(() => useAdmin());
    await waitForLoaded(result);

    await act(async () => {
      await result.current.handleBPSChange("peer-a", 4096);
    });

    expect(mockPost).toHaveBeenCalledWith(
      adminLeasePath(encodeLeaseID("peer-a"), "bps"),
      { bps: 4096 },
    );
  });

  it("keeps loading false while refreshing bps in the background", async () => {
    let getCalls = 0;
    let resolveRefresh:
      | ((value: DeferredAdminSnapshot | PromiseLike<DeferredAdminSnapshot>) => void)
      | undefined;

    mockGet.mockImplementation((path: string) => {
      if (path !== API_PATHS.admin.snapshot) {
        throw new Error(`Unexpected GET path: ${path}`);
      }
      getCalls++;
      if (getCalls === 1) {
        return Promise.resolve({
          leases: [buildLease("peer-a")],
          approval_mode: "auto",
        } as never);
      }
      return new Promise<DeferredAdminSnapshot>((resolve) => {
        resolveRefresh = resolve;
      }) as never;
    });

    const { result } = renderHook(() => useAdmin());
    await waitForLoaded(result);

    let pending: Promise<void> | undefined;
    await act(async () => {
      pending = result.current.handleBPSChange("peer-a", 2048);
      await Promise.resolve();
      expect(result.current.loading).toBe(false);
      resolveRefresh?.({
        leases: [{ ...buildLease("peer-a"), BPS: 2048 }],
        approval_mode: "auto",
      });
      await pending;
    });

    expect(result.current.servers[0]?.bps).toBe(2048);
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
