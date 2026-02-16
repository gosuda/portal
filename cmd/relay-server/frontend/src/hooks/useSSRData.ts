import { useMemo } from "react";

export interface Metadata {
  description: string;
  tags: string[];
  thumbnail: string;
  owner: string;
  hide: boolean;
}

export interface ServerData {
  Peer: string;
  Name: string;
  Kind: string;
  Connected: boolean;
  DNS: string;
  LastSeen: string;
  LastSeenISO: string;
  FirstSeenISO: string;
  TTL: string;
  Link: string;
  StaleRed: boolean;
  Hide: boolean;
  Metadata: string;
  BPS?: number; // bytes-per-second limit (0 = unlimited), admin only
  IsApproved?: boolean; // whether lease is approved (for manual mode), admin only
  IsDenied?: boolean; // whether lease is denied (for manual mode), admin only
  IP?: string; // client IP address (for IP-based ban), admin only
  IsIPBanned?: boolean; // whether the IP is banned, admin only
}

function readSSRData(): ServerData[] {
  if (typeof document === "undefined") {
    return [];
  }

  // Try to read SSR data from the script tag
  const ssrScript = document.getElementById("__SSR_DATA__");
  console.log("[SSR] Script tag found:", !!ssrScript);

  if (!ssrScript || !ssrScript.textContent) {
    console.log("[SSR] No script tag or content found");
    return [];
  }

  console.log("[SSR] Script content:", ssrScript.textContent.substring(0, 200));

  try {
    const parsed = JSON.parse(ssrScript.textContent);
    console.log("[SSR] Parsed data:", parsed);
    console.log("[SSR] Is array:", Array.isArray(parsed));
    console.log("[SSR] Length:", Array.isArray(parsed) ? parsed.length : 0);
    return Array.isArray(parsed) ? parsed : [];
  } catch (err) {
    console.error("[SSR] Failed to parse SSR data:", err);
    return [];
  }
}

/**
 * useSSRData hook reads server data injected by Go SSR.
 * The data is embedded in a <script id="__SSR_DATA__"> tag in the HTML.
 */
export function useSSRData(): ServerData[] {
  return useMemo(() => readSSRData(), []);
}
