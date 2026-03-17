import { useMemo } from "react";
import { useSSRData } from "@/hooks/useSSRData";
import type { ServerData } from "@/hooks/useSSRData";
import { useList, type BaseServer } from "@/hooks/useList";
import { generateRandomServers } from "@/lib/testUtils";
import { parseLeaseMetadata } from "@/lib/metadata";

const useDebug = false;

export type ClientServer = BaseServer;

function convertSSRDataToServers(ssrData: ServerData[]): ClientServer[] {
  return ssrData.map((row, index) => {
    const metadata = parseLeaseMetadata(row.Metadata);
    const hostname = row.Hostname || "";

    return {
      id: index + 1,
      name: row.Name || hostname || "(unnamed)",
      description: metadata.description || "",
      tags: metadata.tags,
      thumbnail: metadata.thumbnail || "",
      owner: metadata.owner || "",
      online: (row.Ready || 0) > 0,
      dns: hostname,
      link: hostname ? `https://${hostname}/` : "",
      lastUpdated: row.LastSeenAt || undefined,
      firstSeen: row.FirstSeenAt || undefined,
    };
  });
}

export function useServerList() {
  // Get SSR data
  const ssrData = useSSRData();

  // Convert SSR data to servers
  const servers: ClientServer[] = useMemo(() => {
    console.log("[App] SSR data length:", ssrData.length);

    if (useDebug) {
      return generateRandomServers(100);
    }
    if (ssrData.length > 0) {
      console.log("[App] Using SSR data");
      const converted = convertSSRDataToServers(ssrData);
      console.log("[App] Converted servers:", converted);
      return converted;
    }
    console.log("[App] Using sample servers");
    return [];
  }, [ssrData]);

  // Use common list logic
  const listState = useList({
    servers,
    storageKey: "serverFavorites",
  });

  return {
    // Raw servers (before filtering)
    servers,
    // All list state and handlers from useList
    ...listState,
  };
}
