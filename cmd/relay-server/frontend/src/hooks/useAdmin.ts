import { useCallback, useEffect, useMemo, useState } from "react";
import type { ServerData, Metadata } from "@/hooks/useSSRData";
import { useList } from "@/hooks/useList";
import type { AdminServer, ApprovalMode, BanFilter } from "@/types/server";

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
      const [leasesRes, bannedRes, settingsRes] = await Promise.all([
        fetch("/admin/leases"),
        fetch("/admin/leases/banned"),
        fetch("/admin/settings"),
      ]);

      if (!leasesRes.ok || !bannedRes.ok) {
        throw new Error("Failed to fetch admin data. Are you on localhost?");
      }

      const leasesData: ServerData[] = await leasesRes.json();
      const bannedData: string[] = await bannedRes.json();

      setServerData(leasesData || []);
      // bannedData is base64 encoded byte arrays, decode them
      const decodedBanned = (bannedData || []).map((b64: string) => {
        try {
          return atob(b64);
        } catch {
          return b64;
        }
      });
      setBannedLeases(decodedBanned);

      // Load settings
      if (settingsRes.ok) {
        const settings = await settingsRes.json();
        setApprovalMode(settings.approval_mode || "auto");
      }
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
      if (banFilter === "all") return true;
      if (banFilter === "banned") return server.isBanned;
      if (banFilter === "active") return !server.isBanned;
      return true;
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
        // URL-safe base64 encode the peer ID
        const safeId = btoa(peerId)
          .replace(/\+/g, "-")
          .replace(/\//g, "_")
          .replace(/=+$/, "");
        await fetch(`/admin/leases/${safeId}/ban`, {
          method: isBan ? "POST" : "DELETE",
        });
        fetchData();
      } catch (err) {
        console.error(err);
      }
    },
    [fetchData]
  );

  const handleBPSChange = useCallback(
    async (peerId: string, bps: number) => {
      try {
        // URL-safe base64 encode the peer ID
        const safeId = btoa(peerId)
          .replace(/\+/g, "-")
          .replace(/\//g, "_")
          .replace(/=+$/, "");
        if (bps <= 0) {
          await fetch(`/admin/leases/${safeId}/bps`, { method: "DELETE" });
        } else {
          await fetch(`/admin/leases/${safeId}/bps`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ bps }),
          });
        }
        fetchData();
      } catch (err) {
        console.error(err);
      }
    },
    [fetchData]
  );

  const handleApprovalModeChange = useCallback(async (mode: ApprovalMode) => {
    try {
      await fetch("/admin/settings/approval-mode", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ mode }),
      });
      setApprovalMode(mode);
    } catch (err) {
      console.error(err);
    }
  }, []);

  const handleApproveStatus = useCallback(
    async (peerId: string, approve: boolean) => {
      try {
        const safeId = btoa(peerId)
          .replace(/\+/g, "-")
          .replace(/\//g, "_")
          .replace(/=+$/, "");
        await fetch(`/admin/leases/${safeId}/approve`, {
          method: approve ? "POST" : "DELETE",
        });
        fetchData();
      } catch (err) {
        console.error(err);
      }
    },
    [fetchData]
  );

  const handleDenyStatus = useCallback(
    async (peerId: string, deny: boolean) => {
      try {
        const safeId = btoa(peerId)
          .replace(/\+/g, "-")
          .replace(/\//g, "_")
          .replace(/=+$/, "");
        await fetch(`/admin/leases/${safeId}/deny`, {
          method: deny ? "POST" : "DELETE",
        });
        fetchData();
      } catch (err) {
        console.error(err);
      }
    },
    [fetchData]
  );

  const handleIPBanStatus = useCallback(
    async (ip: string, isBan: boolean) => {
      try {
        await fetch(`/admin/ips/${ip}/ban`, {
          method: isBan ? "POST" : "DELETE",
        });
        fetchData();
      } catch (err) {
        console.error(err);
      }
    },
    [fetchData]
  );

  // Bulk action handlers
  const handleBulkApprove = useCallback(
    async (peerIds: string[]) => {
      try {
        await Promise.all(
          peerIds.map((peerId) => {
            const safeId = btoa(peerId)
              .replace(/\+/g, "-")
              .replace(/\//g, "_")
              .replace(/=+$/, "");
            return fetch(`/admin/leases/${safeId}/approve`, { method: "POST" });
          })
        );
        fetchData();
      } catch (err) {
        console.error(err);
      }
    },
    [fetchData]
  );

  const handleBulkDeny = useCallback(
    async (peerIds: string[]) => {
      try {
        await Promise.all(
          peerIds.map((peerId) => {
            const safeId = btoa(peerId)
              .replace(/\+/g, "-")
              .replace(/\//g, "_")
              .replace(/=+$/, "");
            return fetch(`/admin/leases/${safeId}/deny`, { method: "POST" });
          })
        );
        fetchData();
      } catch (err) {
        console.error(err);
      }
    },
    [fetchData]
  );

  const handleBulkBan = useCallback(
    async (peerIds: string[]) => {
      try {
        await Promise.all(
          peerIds.map((peerId) => {
            const safeId = btoa(peerId)
              .replace(/\+/g, "-")
              .replace(/\//g, "_")
              .replace(/=+$/, "");
            return fetch(`/admin/leases/${safeId}/ban`, { method: "POST" });
          })
        );
        fetchData();
      } catch (err) {
        console.error(err);
      }
    },
    [fetchData]
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
