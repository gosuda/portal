import { API_PATHS } from "@/lib/apiPaths";
import {
  buildDefaultExposeName,
  resolveExposeName,
} from "../../../utils/exposeName";

export type TunnelCommandOS = "unix" | "windows";

export interface TunnelCommandOptions {
  currentOrigin: string;
  target: string;
  name: string;
  nameSeed: string;
  relayUrls: string[];
  defaultRelays: boolean;
  thumbnailURL: string;
  enableUDP: boolean;
  udpAddr: string;
  os: TunnelCommandOS;
}

export function buildDefaultTunnelName(
  target: string,
  nameSeed: string
): string {
  return buildDefaultExposeName(target, nameSeed);
}

export function buildTunnelCommand(options: TunnelCommandOptions): string {
  const { installLine, exposeHead, exposeOptions } =
    buildTunnelCommandParts(options);
  return joinTunnelCommand(installLine, exposeHead, exposeOptions);
}


function buildTunnelCommandParts({
  currentOrigin,
  defaultRelays,
  enableUDP,
  name,
  nameSeed,
  os,
  relayUrls,
  target,
  thumbnailURL,
  udpAddr,
}: TunnelCommandOptions): {
  installLine: string;
  exposeHead: string;
  exposeOptions: string[];
} {
  const targetValue = target.trim() === "" ? "3000" : target.trim();
  const nameValue = resolveExposeName(name, targetValue, nameSeed);
  const relayURLValue =
    relayUrls.length > 0 ? relayUrls.join(",") : currentOrigin;
  const installScriptURL = new URL(
    API_PATHS.install.shell,
    currentOrigin
  ).toString();
  const installPowerShellURL = new URL(
    API_PATHS.install.powershell,
    currentOrigin
  ).toString();

  const exposeArgs: string[] = [];

  exposeArgs.push(`--name ${formatToken(nameValue, os)}`);
  if (relayUrls.length > 0) {
    exposeArgs.push(`--relays ${formatToken(relayURLValue, os)}`);
  }
  if (!defaultRelays) {
    exposeArgs.push("--default-relays=false");
  }

  const normalizedThumbnailURL = normalizeAbsoluteHTTPURL(thumbnailURL);
  if (normalizedThumbnailURL !== "") {
    exposeArgs.push(`--thumbnail ${formatToken(normalizedThumbnailURL, os)}`);
  }

  if (enableUDP) {
    exposeArgs.push("--udp");
    const udpAddrValue = udpAddr.trim() || targetValue;
    exposeArgs.push(`--udp-addr ${formatToken(udpAddrValue, os)}`);
  }

  if (os === "windows") {
    return {
      installLine: [
        `$ProgressPreference = 'SilentlyContinue'`,
        `irm ${formatToken(installPowerShellURL, os)} | iex`,
      ].join("\n"),
      exposeHead: "portal expose",
      exposeOptions: [...exposeArgs, formatToken(targetValue, os)],
    };
  }

  const curlFlags = isLocalRelayOrigin(currentOrigin) ? "-ksSL" : "-sSL";
  return {
    installLine: `curl ${curlFlags} ${formatToken(installScriptURL, os)} | bash`,
    exposeHead: "portal expose",
    exposeOptions: [...exposeArgs, formatToken(targetValue, os)],
  };
}

export function buildTunnelPreviewURL(
  origin: string,
  name: string,
  target: string,
  nameSeed: string
): string {
  const baseHost = getRelayOriginHost(origin);
  const subdomain = resolveExposeName(name, target, nameSeed);
  return `https://${subdomain}.${baseHost}`;
}

export function buildTunnelStatusHostname(
  origin: string,
  name: string,
  target: string,
  nameSeed: string
): string {
  const relayHost = getRelayOriginHost(origin);
  if (relayHost === "") {
    return "";
  }
  const subdomain = resolveExposeName(name, target, nameSeed);
  return `${subdomain}.${relayHost}`;
}

export function normalizeAbsoluteHTTPURL(raw: string): string {
  const trimmed = raw.trim();
  if (trimmed === "") {
    return "";
  }

  try {
    const parsed = new URL(trimmed);
    if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
      return "";
    }
    return parsed.toString();
  } catch {
    return "";
  }
}

function getRelayOriginHost(origin: string): string {
  try {
    const parsed = new URL(origin);
    return parsed.hostname.trim().toLowerCase();
  } catch {
    return "";
  }
}

function isLocalRelayOrigin(origin: string): boolean {
  try {
    const parsed = new URL(origin);
    return isLocalRelayHostname(parsed.hostname.trim().toLowerCase());
  } catch {
    return false;
  }
}

function isLocalRelayHostname(hostname: string): boolean {
  return (
    hostname === "localhost" ||
    hostname === "127.0.0.1" ||
    hostname === "::1" ||
    hostname.endsWith(".localhost")
  );
}

function quoteShellValue(value: string): string {
  return "'" + value.replace(/'/g, `'"'"'`) + "'";
}

function quotePowerShellValue(value: string): string {
  return `'${value.replace(/'/g, "''")}'`;
}

function formatToken(value: string, os: TunnelCommandOS): string {
  if (/^[A-Za-z0-9:/.=_-]+$/.test(value)) {
    return value;
  }
  return os === "windows" ? quotePowerShellValue(value) : quoteShellValue(value);
}

function joinTunnelCommand(
  installLine: string,
  exposeHead: string,
  exposeOptions: string[]
): string {
  return [installLine, [exposeHead, ...exposeOptions].join(" ")].join("\n");
}
