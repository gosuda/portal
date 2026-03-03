import { useCallback, useEffect, useMemo, useState } from "react";
import type { ServerData, Metadata } from "@/hooks/useSSRData";
import { useList, type BaseServer } from "@/hooks/useList";
import type { BanFilter } from "@/components/ServerListView";
import {
  API_PATHS,
  adminIPBanPath,
  adminLeasePath,
  encodeLeaseID,
} from "@/lib/apiPaths";
import { apiClient } from "@/lib/apiClient";

// Approval mode type
export type ApprovalMode = "auto" | "manual";

// Extended BaseServer with admin-specific fields
export interface AdminServer extends BaseServer {
  peerId: string;
  isBanned: boolean;
  bps: number; // bytes-per-second limit (0 = unlimited)
  isApproved: boolean; // whether lease is approved (for manual mode)
  isDenied: boolean; // whether lease is denied (for manual mode)
  ip: string; // client IP address (for IP-based ban)
  isIPBanned: boolean; // whether the IP is banned
}

// Convert ServerData (from API) to AdminServer format
function convertServerDataToAdminServer(
  row: ServerData,
  index: number,
  bannedLeases: string[]
): AdminServer {
  let metadata: Metadata = {
    description: "",
    tags: [],
    thumbnail: "",
    owner: "",
    hide: false,
  };

  try {
    if (row.Metadata) {
      metadata = JSON.parse(row.Metadata);
    }
  } catch (err) {
    console.error("[Admin] Failed to parse metadata:", err, row.Metadata);
  }

  const normalizedTags = Array.isArray(metadata.tags)
    ? metadata.tags
        .map((tag) => (typeof tag === "string" ? tag.trim() : ""))
        .filter(Boolean)
    : [];

  return {
    id: index + 1,
    name: row.Name || row.DNS || "(unnamed)",
    description: metadata.description || "",
    tags: normalizedTags,
    thumbnail: metadata.thumbnail || "",
    owner: metadata.owner || "",
    online: row.Connected,
    dns: row.DNS || "",
    link: row.Link,
    lastUpdated: row.LastSeenISO || row.LastSeen || undefined,
    firstSeen: row.FirstSeenISO || undefined,
    // Admin-specific fields
    peerId: row.Peer,
    isBanned: bannedLeases.includes(row.Peer),
    bps: row.BPS || 0,
    isApproved: row.IsApproved || false,
    isDenied: row.IsDenied || false,
    ip: row.IP || "",
    isIPBanned: row.IsIPBanned || false,
  };
}

function decodeLeaseID(value: string): string {
  try {
    return atob(value);
  } catch {
    return value;
  }
}

export function useAdmin() {
  const [serverData, setServerData] = useState<ServerData[]>([]);
  const [bannedLeases, setBannedLeases] = useState<string[]>([]);
  const [approvalMode, setApprovalMode] = useState<ApprovalMode>("auto");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");

  // Admin-specific filter state
  const [banFilter, setBanFilter] = useState<BanFilter>("all");

  const fetchData = useCallback(async () => {
    try {
      const [leasesData, bannedData, settings] = await Promise.all([
        apiClient.get<ServerData[]>(API_PATHS.admin.leases),
        apiClient.get<string[]>(API_PATHS.admin.bannedLeases),
        apiClient.get<{ approval_mode?: ApprovalMode }>(API_PATHS.admin.settings),
      ]);

      setServerData(leasesData || []);
      setBannedLeases((bannedData || []).map(decodeLeaseID));
      setApprovalMode(settings?.approval_mode || "auto");
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchData();
  }, [fetchData]);

  // Convert ServerData to AdminServer format
  const servers: AdminServer[] = useMemo(() => {
    return serverData.map((row, index) =>
      convertServerDataToAdminServer(row, index, bannedLeases)
    );
  }, [serverData, bannedLeases]);

  // Additional filter for ban status
  const additionalFilter = useCallback(
    (server: AdminServer) => {
      switch (banFilter) {
        case "banned":
          return server.isBanned;
        case "active":
          return !server.isBanned;
        default:
          return true;
      }
    },
    [banFilter]
  );

  // Use common list logic with additional ban filter
  const listState = useList({
    servers,
    storageKey: "adminFavorites",
    additionalFilter,
  });

  // Admin-specific handlers
  const handleBanFilterChange = useCallback((value: BanFilter) => {
    setBanFilter(value);
  }, []);

  const handleBanStatus = useCallback(
    async (peerId: string, isBan: boolean) => {
      try {
        const encodedLeaseID = encodeLeaseID(peerId);
        if (isBan) {
          await apiClient.post<unknown>(adminLeasePath(encodedLeaseID, "ban"));
        } else {
          await apiClient.delete<unknown>(adminLeasePath(encodedLeaseID, "ban"));
        }
        await fetchData();
      } catch (err) {
        console.error(err);
      }
    },
    [fetchData]
  );

  const handleBPSChange = useCallback(
    async (peerId: string, bps: number) => {
      try {
        const encodedLeaseID = encodeLeaseID(peerId);
        if (bps <= 0) {
          await apiClient.delete<unknown>(adminLeasePath(encodedLeaseID, "bps"));
        } else {
          await apiClient.post<unknown>(adminLeasePath(encodedLeaseID, "bps"), {
            bps,
          });
        }
        await fetchData();
      } catch (err) {
        console.error(err);
      }
    },
    [fetchData]
  );

  const handleApprovalModeChange = useCallback(async (mode: ApprovalMode) => {
    try {
      await apiClient.post<unknown>(API_PATHS.admin.approvalMode, { mode });
      setApprovalMode(mode);
    } catch (err) {
      console.error(err);
    }
  }, []);

  const handleApproveStatus = useCallback(
    async (peerId: string, approve: boolean) => {
      try {
        const encodedLeaseID = encodeLeaseID(peerId);
        if (approve) {
          await apiClient.post<unknown>(adminLeasePath(encodedLeaseID, "approve"));
        } else {
          await apiClient.delete<unknown>(adminLeasePath(encodedLeaseID, "approve"));
        }
        await fetchData();
      } catch (err) {
        console.error(err);
      }
    },
    [fetchData]
  );

  const handleDenyStatus = useCallback(
    async (peerId: string, deny: boolean) => {
      try {
        const encodedLeaseID = encodeLeaseID(peerId);
        if (deny) {
          await apiClient.post<unknown>(adminLeasePath(encodedLeaseID, "deny"));
        } else {
          await apiClient.delete<unknown>(adminLeasePath(encodedLeaseID, "deny"));
        }
        await fetchData();
      } catch (err) {
        console.error(err);
      }
    },
    [fetchData]
  );

  const handleIPBanStatus = useCallback(
    async (ip: string, isBan: boolean) => {
      try {
        if (isBan) {
          await apiClient.post<unknown>(adminIPBanPath(ip));
        } else {
          await apiClient.delete<unknown>(adminIPBanPath(ip));
        }
        await fetchData();
      } catch (err) {
        console.error(err);
      }
    },
    [fetchData]
  );

  // Bulk action handlers
  const runBulkLeaseAction = useCallback(
    async (peerIds: string[], action: "approve" | "deny" | "ban") => {
      await Promise.all(
        peerIds.map((peerId) =>
          apiClient.post<unknown>(adminLeasePath(encodeLeaseID(peerId), action))
        )
      );
    },
    []
  );

  const handleBulkApprove = useCallback(
    async (peerIds: string[]) => {
      try {
        await runBulkLeaseAction(peerIds, "approve");
        await fetchData();
      } catch (err) {
        console.error(err);
      }
    },
    [fetchData, runBulkLeaseAction]
  );

  const handleBulkDeny = useCallback(
    async (peerIds: string[]) => {
      try {
        await runBulkLeaseAction(peerIds, "deny");
        await fetchData();
      } catch (err) {
        console.error(err);
      }
    },
    [fetchData, runBulkLeaseAction]
  );

  const handleBulkBan = useCallback(
    async (peerIds: string[]) => {
      try {
        await runBulkLeaseAction(peerIds, "ban");
        await fetchData();
      } catch (err) {
        console.error(err);
      }
    },
    [fetchData, runBulkLeaseAction]
  );

  return {
    // Raw data
    serverData,
    bannedLeases,
    // Converted servers (before filtering)
    servers,
    // All list state and handlers from useList
    ...listState,
    // Admin-specific filter state
    banFilter,
    approvalMode,
    // State
    loading,
    error,
    // Admin-specific handlers
    handleBanFilterChange,
    handleBanStatus,
    handleBPSChange,
    handleApprovalModeChange,
    handleApproveStatus,
    handleDenyStatus,
    handleIPBanStatus,
    // Bulk action handlers
    handleBulkApprove,
    handleBulkDeny,
    handleBulkBan,
    refresh: fetchData,
  };
}
