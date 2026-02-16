import { useMemo } from "react";
import { useSSRData } from "@/hooks/useSSRData";
import type { ServerData, Metadata } from "@/hooks/useSSRData";
import { useList } from "@/hooks/useList";
import type { ClientServer } from "@/types/server";
import { generateRandomServers } from "@/lib/testUtils";

const useDebug = false;

function convertSSRDataToServers(ssrData: ServerData[]): ClientServer[] {
  return ssrData.map((row, index) => {
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
      console.error("[App] Failed to parse metadata:", err, row.Metadata);
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
