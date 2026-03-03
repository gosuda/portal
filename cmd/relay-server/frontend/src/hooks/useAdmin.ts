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
import { APIClientError, apiClient } from "@/lib/apiClient";

export type ApprovalMode = "auto" | "manual";

type LeaseAction = "approve" | "deny" | "ban";

type SettingsResponse = {
  approval_mode?: ApprovalMode;
};

interface LeaseActionResult {
  approval_mode?: ApprovalMode;
}

export interface AdminServer extends BaseServer {
  peerId: string;
  isBanned: boolean;
  bps: number;
  isApproved: boolean;
  isDenied: boolean;
  ip: string;
  isIPBanned: boolean;
}

const ADMIN_ERROR_MESSAGE_BY_CODE: Record<string, string> = {
  invalid_mode: "Invalid approval mode. Choose auto or manual and retry.",
  invalid_lease_id: "Selected lease identifier is invalid. Refresh and try again.",
  lease_rejected: "Request was rejected by policy. Review conflicts and retry.",
  ip_banned: "Request denied because the source IP is banned.",
  unauthorized: "Admin authorization failed. Sign in again and retry.",
  method_not_allowed: "This action is not supported by the current server version.",
};

function toAdminErrorMessage(error: unknown, fallback: string): string {
  if (error instanceof APIClientError) {
    const mappedMessage = ADMIN_ERROR_MESSAGE_BY_CODE[error.code];
    if (mappedMessage) {
      return mappedMessage;
    }

    if (error.status === 401 || error.status === 403) {
      return "Admin authorization failed. Sign in again and retry.";
    }
    if (error.status === 409) {
      return "Request was rejected by policy. Refresh and retry.";
    }

    const message = error.message.trim();
    return message || fallback;
  }

  if (error instanceof Error) {
    const message = error.message.trim();
    return message || fallback;
  }

  return fallback;
}

function normalizeLeaseID(raw: string): string {
  return raw.trim();
}

function encodeLeaseIDForPath(raw: string): string {
  const leaseID = normalizeLeaseID(raw);
  if (!leaseID) {
    throw new Error("Missing lease ID");
  }
  return encodeLeaseID(leaseID);
}

function sanitizeMetadata(row: ServerData): Metadata {
  const isRecord = (value: unknown): value is Record<string, unknown> => {
    return (
      typeof value === "object" &&
      value !== null &&
      !Array.isArray(value)
    );
  };

  const fallback: Metadata = {
    description: "",
    tags: [],
    thumbnail: "",
    owner: "",
    hide: false,
  };

  if (!row.Metadata) {
    return fallback;
  }

  try {
    const parsed = JSON.parse(row.Metadata);
    if (!isRecord(parsed)) {
      return fallback;
    }

    const rawTags = parsed.tags;
    const tags = Array.isArray(rawTags)
      ? rawTags
          .map((tag) => (typeof tag === "string" ? tag.trim() : ""))
          .filter(Boolean)
      : [];

    return {
      description:
        typeof parsed.description === "string" ? parsed.description : "",
      tags,
      thumbnail:
        typeof parsed.thumbnail === "string" ? parsed.thumbnail : "",
      owner: typeof parsed.owner === "string" ? parsed.owner : "",
      hide: typeof parsed.hide === "boolean" ? parsed.hide : false,
    };
  } catch {
    return fallback;
  }
}

function toAdminServer(
  row: ServerData,
  index: number,
  bannedLeases: Set<string>
): AdminServer {
  const metadata = sanitizeMetadata(row);
  const peerId = normalizeLeaseID(row.Peer);

  return {
    id: index + 1,
    name: row.Name || row.DNS || "(unnamed)",
    description: metadata.description,
    tags: metadata.tags,
    thumbnail: metadata.thumbnail,
    owner: metadata.owner,
    online: row.Connected,
    dns: row.DNS || "",
    link: row.Link,
    lastUpdated: row.LastSeenISO || row.LastSeen || undefined,
    firstSeen: row.FirstSeenISO || undefined,
    peerId,
    isBanned: bannedLeases.has(peerId),
    bps: row.BPS || 0,
    isApproved: row.IsApproved || false,
    isDenied: row.IsDenied || false,
    ip: row.IP || "",
    isIPBanned: row.IsIPBanned || false,
  };
}

function normalizeApprovalMode(value: string | undefined): ApprovalMode {
  return value === "manual" ? "manual" : "auto";
}

function dedupeStrings(values: string[]): string[] {
  const seen = new Set<string>();
  const output: string[] = [];

  values.forEach((value) => {
    if (seen.has(value)) {
      return;
    }
    seen.add(value);
    output.push(value);
  });

  return output;
}

export function useAdmin() {
  const [serverData, setServerData] = useState<ServerData[]>([]);
  const [bannedLeases, setBannedLeases] = useState<string[]>([]);
  const [approvalMode, setApprovalMode] = useState<ApprovalMode>("auto");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");

  const [banFilter, setBanFilter] = useState<BanFilter>("all");

  const fetchData = useCallback(async () => {
    setError("");
    setLoading(true);

    try {
      const settingsRequest = apiClient
        .get<SettingsResponse>(API_PATHS.admin.settings)
        .catch(async (err) => {
          if (err instanceof APIClientError && err.status === 404) {
            return apiClient.get<SettingsResponse>(API_PATHS.admin.approvalMode);
          }
          throw err;
        });

      const [leasesData, bannedData, settings] = await Promise.all([
        apiClient.get<ServerData[]>(API_PATHS.admin.leases),
        apiClient.get<string[]>(API_PATHS.admin.bannedLeases),
        settingsRequest,
      ]);

      const normalizedBans = (Array.isArray(bannedData) ? bannedData : [])
        .map((leaseID) =>
          typeof leaseID === "string" ? normalizeLeaseID(leaseID) : ""
        )
        .filter(Boolean);

      setServerData(Array.isArray(leasesData) ? leasesData : []);
      setBannedLeases(dedupeStrings(normalizedBans));
      setApprovalMode(normalizeApprovalMode(settings?.approval_mode));
    } catch (err: unknown) {
      setError(toAdminErrorMessage(err, "Failed to load admin data"));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchData();
  }, [fetchData]);

  const bannedLeaseSet = useMemo(
    () => new Set(bannedLeases.map((leaseID) => normalizeLeaseID(leaseID))),
    [bannedLeases]
  );

  const servers: AdminServer[] = useMemo(() => {
    return serverData.map((row, index) =>
      toAdminServer(row, index, bannedLeaseSet)
    );
  }, [serverData, bannedLeaseSet]);

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

  const listState = useList({
    servers,
    storageKey: "adminFavorites",
    additionalFilter,
  });

  const runAdminAction = useCallback(
    async (action: () => Promise<void>) => {
      setError("");
      try {
        await action();
        await fetchData();
      } catch (err: unknown) {
        const message = toAdminErrorMessage(err, "Action failed");
        console.error(err);
        setError(message);
        throw err;
      }
    },
    [fetchData]
  );

  const updateLeaseAction = useCallback(
    async (peerId: string, action: LeaseAction, enabled: boolean) => {
      const encodedLeaseID = encodeLeaseIDForPath(peerId);
      const method = enabled ? apiClient.post : apiClient.delete;
      await method<LeaseActionResult>(adminLeasePath(encodedLeaseID, action));
    },
    []
  );

  const handleBanFilterChange = useCallback((value: BanFilter) => {
    setBanFilter(value);
  }, []);

  const handleBanStatus = useCallback(
    (peerId: string, isBan: boolean) =>
      runAdminAction(() => updateLeaseAction(peerId, "ban", isBan)),
    [runAdminAction, updateLeaseAction]
  );

  const handleBPSChange = useCallback(
    (peerId: string, bps: number) =>
      runAdminAction(async () => {
        const encodedLeaseID = encodeLeaseIDForPath(peerId);
        const normalizedBPS = Math.trunc(bps);
        if (!Number.isFinite(normalizedBPS) || normalizedBPS <= 0) {
          await apiClient.delete<LeaseActionResult>(
            adminLeasePath(encodedLeaseID, "bps")
          );
          return;
        }
        await apiClient.post<LeaseActionResult>(adminLeasePath(encodedLeaseID, "bps"), {
          bps: normalizedBPS,
        });
      }),
    [runAdminAction]
  );

  const handleApprovalModeChange = useCallback(
    async (mode: ApprovalMode) => {
      await runAdminAction(async () => {
        const response = await apiClient.post<SettingsResponse>(
          API_PATHS.admin.approvalMode,
          { mode }
        );
        const nextMode = normalizeApprovalMode(response?.approval_mode ?? mode);
        setApprovalMode(nextMode);
      });
    },
    [runAdminAction]
  );

  const handleApproveStatus = useCallback(
    (peerId: string, approve: boolean) =>
      runAdminAction(() => updateLeaseAction(peerId, "approve", approve)),
    [runAdminAction, updateLeaseAction]
  );

  const handleDenyStatus = useCallback(
    (peerId: string, deny: boolean) =>
      runAdminAction(() => updateLeaseAction(peerId, "deny", deny)),
    [runAdminAction, updateLeaseAction]
  );

  const handleIPBanStatus = useCallback(
    (ip: string, isBan: boolean) =>
      runAdminAction(async () => {
        const normalizedIP = ip.trim();
        if (!normalizedIP) {
          throw new Error("Missing IP address");
        }
        if (isBan) {
          await apiClient.post<LeaseActionResult>(adminIPBanPath(normalizedIP));
          return;
        }
        await apiClient.delete<LeaseActionResult>(adminIPBanPath(normalizedIP));
      }),
    [runAdminAction]
  );

  const runBulkLeaseAction = useCallback(
    async (peerIds: string[], action: LeaseAction) => {
      const normalizedPeerIDs = dedupeStrings(
        peerIds
          .map((peerId) => normalizeLeaseID(peerId))
          .filter(Boolean)
      );
      if (normalizedPeerIDs.length === 0) {
        throw new Error("No valid leases selected");
      }

      const results = await Promise.allSettled(
        normalizedPeerIDs.map((peerId) =>
          apiClient.post<LeaseActionResult>(
            adminLeasePath(encodeLeaseIDForPath(peerId), action)
          )
        )
      );

      const failed = results.find(
        (
          result
        ): result is PromiseRejectedResult =>
          result.status === "rejected"
      );
      if (failed) {
        throw failed.reason instanceof Error
          ? failed.reason
          : new Error(String(failed.reason));
      }
    },
    []
  );

  const handleBulkAction = useCallback(
    (peerIds: string[], action: LeaseAction) =>
      runAdminAction(() => runBulkLeaseAction(peerIds, action)),
    [runAdminAction, runBulkLeaseAction]
  );

  const handleBulkApprove = useCallback(
    (peerIds: string[]) => handleBulkAction(peerIds, "approve"),
    [handleBulkAction]
  );

  const handleBulkDeny = useCallback(
    (peerIds: string[]) => handleBulkAction(peerIds, "deny"),
    [handleBulkAction]
  );

  const handleBulkBan = useCallback(
    (peerIds: string[]) => handleBulkAction(peerIds, "ban"),
    [handleBulkAction]
  );

  return {
    serverData,
    bannedLeases,
    servers,
    ...listState,
    banFilter,
    approvalMode,
    loading,
    error,
    handleBanFilterChange,
    handleBanStatus,
    handleBPSChange,
    handleApprovalModeChange,
    handleApproveStatus,
    handleDenyStatus,
    handleIPBanStatus,
    handleBulkApprove,
    handleBulkDeny,
    handleBulkBan,
    refresh: fetchData,
  };
}
