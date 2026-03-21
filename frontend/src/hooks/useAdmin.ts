import { useEffect, useMemo, useState } from "react";
import type { ServerData } from "@/hooks/useSSRData";
import { useList, type BaseServer } from "@/hooks/useList";
import type { BanFilter } from "@/components/ServerListView";
import {
  API_PATHS,
  adminIPBanPath,
  adminLeasePath,
  encodeLeaseID,
} from "@/lib/apiPaths";
import { APIClientError, apiClient } from "@/lib/apiClient";
import { parseLeaseMetadata } from "@/lib/metadata";

export type ApprovalMode = "auto" | "manual";

type LeaseAction = "approve" | "deny" | "ban";

type ApprovalModeResponse = {
  approval_mode?: ApprovalMode;
};

type AdminSnapshotResponse = {
  approval_mode?: ApprovalMode;
  leases?: ServerData[];
  udp?: { enabled: boolean; max_leases: number };
};

type LeaseActionResult = ApprovalModeResponse;

export interface AdminServer extends BaseServer {
  peerId: string;
  isBanned: boolean;
  bps: number;
  isApproved: boolean;
  isDenied: boolean;
  ip: string;
  isIPBanned: boolean;
  transport: string;
  udpPort: number;
}

export interface UDPSettings {
  enabled: boolean;
  maxLeases: number;
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

function toAdminServer(
  row: ServerData,
  index: number
): AdminServer {
  const metadata = parseLeaseMetadata(row.Metadata);
  const hostname = row.Hostname || "";

  return {
    id: index + 1,
    name: row.Name || hostname || "(unnamed)",
    description: metadata.description,
    tags: metadata.tags,
    thumbnail: metadata.thumbnail,
    owner: metadata.owner,
    online: (row.Ready || 0) > 0,
    dns: hostname,
    link: hostname ? `https://${hostname}/` : "",
    lastUpdated: row.LastSeenAt || undefined,
    firstSeen: row.FirstSeenAt || undefined,
    peerId: row.ID,
    isBanned: row.IsBanned || false,
    bps: row.BPS || 0,
    isApproved: row.IsApproved || false,
    isDenied: row.IsDenied || false,
    ip: row.ClientIP || "",
    isIPBanned: row.IsIPBanned || false,
    transport: row.Transport || "tcp",
    udpPort: row.UDPPort || 0,
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

interface AdminSnapshot {
  serverData: ServerData[];
  approvalMode: ApprovalMode;
  udpSettings: UDPSettings;
}

async function loadAdminSnapshot(): Promise<AdminSnapshot> {
  const snapshot = await apiClient.get<AdminSnapshotResponse>(API_PATHS.admin.snapshot);
  const normalizedLeases = Array.isArray(snapshot?.leases) ? snapshot.leases : [];

  return {
    serverData: normalizedLeases,
    approvalMode: normalizeApprovalMode(snapshot?.approval_mode),
    udpSettings: {
      enabled: snapshot?.udp?.enabled ?? false,
      maxLeases: snapshot?.udp?.max_leases ?? 0,
    },
  };
}

export function useAdmin() {
  const [serverData, setServerData] = useState<ServerData[]>([]);
  const [approvalMode, setApprovalMode] = useState<ApprovalMode>("auto");
  const [udpSettings, setUDPSettings] = useState<UDPSettings>({ enabled: false, maxLeases: 0 });
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");

  const [banFilter, setBanFilter] = useState<BanFilter>("all");

  const applySnapshot = (snapshot: AdminSnapshot) => {
    setServerData(snapshot.serverData);
    setApprovalMode(snapshot.approvalMode);
    setUDPSettings(snapshot.udpSettings);
  };

  const fetchData = async () => {
    setError("");

    try {
      applySnapshot(await loadAdminSnapshot());
    } catch (err: unknown) {
      setError(toAdminErrorMessage(err, "Failed to load admin data"));
    }
  };

  useEffect(() => {
    let mounted = true;
    const loadInitialData = async () => {
      setError("");
      setLoading(true);
      try {
        const snapshot = await loadAdminSnapshot();
        if (!mounted) {
          return;
        }
        applySnapshot(snapshot);
      } catch (err: unknown) {
        if (!mounted) {
          return;
        }
        setError(toAdminErrorMessage(err, "Failed to load admin data"));
      } finally {
        if (mounted) {
          setLoading(false);
        }
      }
    };

    void loadInitialData();
    return () => {
      mounted = false;
    };
  }, []);

  const servers: AdminServer[] = useMemo(() => {
    return serverData.map((row, index) => toAdminServer(row, index));
  }, [serverData]);

  const additionalFilter = (server: AdminServer) => {
    switch (banFilter) {
      case "banned":
        return server.isBanned;
      case "active":
        return !server.isBanned;
      default:
        return true;
    }
  };

  const listState = useList({
    servers,
    storageKey: "adminFavorites",
    additionalFilter,
  });

  const runAdminAction = async (action: () => Promise<void>) => {
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
  };

  const updateLeaseAction = async (
    peerId: string,
    action: LeaseAction,
    enabled: boolean
  ) => {
    if (!peerId) {
      throw new Error("Missing lease ID");
    }
    const encodedLeaseID = encodeLeaseID(peerId);
    const method = enabled ? apiClient.post : apiClient.delete;
    await method<LeaseActionResult>(adminLeasePath(encodedLeaseID, action));
  };

  const handleBanFilterChange = (value: BanFilter) => {
    setBanFilter(value);
  };

  const handleBanStatus = (peerId: string, isBan: boolean) =>
    runAdminAction(() => updateLeaseAction(peerId, "ban", isBan));

  const handleBPSChange = async (peerId: string, bps: number) => {
    if (!peerId) {
      throw new Error("Missing lease ID");
    }

    const encodedLeaseID = encodeLeaseID(peerId);
    const normalizedBPS = Math.max(0, Math.trunc(bps));
    const previousBPS = serverData.find((row) => row.ID === peerId)?.BPS ?? 0;

    setServerData((prev) =>
      prev.map((row) =>
        row.ID === peerId ? { ...row, BPS: normalizedBPS } : row
      )
    );

    try {
      await runAdminAction(async () => {
        if (!Number.isFinite(normalizedBPS) || normalizedBPS <= 0) {
          await apiClient.delete<LeaseActionResult>(
            adminLeasePath(encodedLeaseID, "bps")
          );
          return;
        }
        await apiClient.post<LeaseActionResult>(
          adminLeasePath(encodedLeaseID, "bps"),
          { bps: normalizedBPS }
        );
      });
    } catch (err) {
      setServerData((prev) =>
        prev.map((row) =>
          row.ID === peerId ? { ...row, BPS: previousBPS } : row
        )
      );
      throw err;
    }
  };

  const handleApprovalModeChange = async (mode: ApprovalMode) => {
    await runAdminAction(async () => {
      const response = await apiClient.post<ApprovalModeResponse>(
        API_PATHS.admin.approvalMode,
        { mode }
      );
      const nextMode = normalizeApprovalMode(response?.approval_mode ?? mode);
      setApprovalMode(nextMode);
    });
  };

  const handleUDPSettingsChange = async (settings: UDPSettings) => {
    await runAdminAction(async () => {
      const response = await apiClient.post<{ enabled: boolean; max_leases: number }>(
        API_PATHS.admin.udpSettings,
        { enabled: settings.enabled, max_leases: settings.maxLeases }
      );
      setUDPSettings({
        enabled: response?.enabled ?? settings.enabled,
        maxLeases: response?.max_leases ?? settings.maxLeases,
      });
    });
  };

  const handleApproveStatus = (peerId: string, approve: boolean) =>
    runAdminAction(() => updateLeaseAction(peerId, "approve", approve));

  const handleDenyStatus = (peerId: string, deny: boolean) =>
    runAdminAction(() => updateLeaseAction(peerId, "deny", deny));

  const handleIPBanStatus = (ip: string, isBan: boolean) =>
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
    });

  const runBulkLeaseAction = async (peerIds: string[], action: LeaseAction) => {
    const normalizedPeerIDs = dedupeStrings(peerIds.filter((peerId) => peerId.length > 0));
    if (normalizedPeerIDs.length === 0) {
      throw new Error("No valid leases selected");
    }

    const results = await Promise.allSettled(
      normalizedPeerIDs.map((peerId) =>
        apiClient.post<LeaseActionResult>(
          adminLeasePath(encodeLeaseID(peerId), action)
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
  };

  const handleBulkAction = (peerIds: string[], action: LeaseAction) =>
    runAdminAction(() => runBulkLeaseAction(peerIds, action));

  const handleBulkApprove = (peerIds: string[]) => handleBulkAction(peerIds, "approve");

  const handleBulkDeny = (peerIds: string[]) => handleBulkAction(peerIds, "deny");

  const handleBulkBan = (peerIds: string[]) => handleBulkAction(peerIds, "ban");

  return {
    servers,
    ...listState,
    banFilter,
    approvalMode,
    udpSettings,
    loading,
    error,
    handleBanFilterChange,
    handleBanStatus,
    handleBPSChange,
    handleApprovalModeChange,
    handleUDPSettingsChange,
    handleApproveStatus,
    handleDenyStatus,
    handleIPBanStatus,
    handleBulkApprove,
    handleBulkDeny,
    handleBulkBan,
  };
}
