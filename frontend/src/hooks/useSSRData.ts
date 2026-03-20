import { useState, useEffect } from "react";

export interface Metadata {
  description: string;
  tags: string[];
  thumbnail: string;
  owner: string;
  hide: boolean;
}

export interface ServerData {
  ExpiresAt: string;
  FirstSeenAt: string;
  LastSeenAt: string;
  ID: string;
  Name: string;
  BPS?: number;
  ClientIP: string;
  Hostname: string;
  Metadata: unknown;
  Ready: number;
  Transport?: string;
  UDPPort?: number;
  IsApproved?: boolean;
  IsBanned?: boolean;
  IsDenied?: boolean;
  IsIPBanned?: boolean;
}

/**
 * useSSRData hook reads server data injected by Go SSR
 * The data is embedded in a <script id="__SSR_DATA__"> tag in the HTML
 */
export function useSSRData(): ServerData[] {
  const [data, setData] = useState<ServerData[]>([]);

  useEffect(() => {
    // Try to read SSR data from the script tag
    const ssrScript = document.getElementById("__SSR_DATA__");
    console.log("[SSR] Script tag found:", !!ssrScript);

    if (ssrScript && ssrScript.textContent) {
      console.log(
        "[SSR] Script content:",
        ssrScript.textContent.substring(0, 200)
      );
      try {
        const parsed = JSON.parse(ssrScript.textContent);
        console.log("[SSR] Parsed data:", parsed);
        console.log("[SSR] Is array:", Array.isArray(parsed));
        console.log("[SSR] Length:", Array.isArray(parsed) ? parsed.length : 0);
        setData(Array.isArray(parsed) ? parsed : []);
      } catch (err) {
        console.error("[SSR] Failed to parse SSR data:", err);
        setData([]);
      }
    } else {
      console.log("[SSR] No script tag or content found");
    }
  }, []);

  return data;
}
