import * as os from "os";
import { resolveExposeName } from "../../../utils/exposeName";

export type ShellTarget = "unix" | "windows";

export const defaultRelayRegistryURL = "https://raw.githubusercontent.com/gosuda/portal/main/registry.json";

export interface TunnelCommandOptions {
  host: string;
  name: string;
  nameSeed: string;
  relayList: string;
  relayUrl: string;
  thumbnail: string;
  isLocal: boolean;
}

export function validateRelayUrl(value: string): string | undefined {
  const trimmed = value.trim();
  if (!trimmed) {
    return "Required";
  }
  let parsed: URL;
  try {
    parsed = new URL(trimmed);
  } catch {
    return "Enter a valid https:// URL";
  }
  if (parsed.protocol !== "https:") {
    return "Portal relay URLs must use https://";
  }
  return undefined;
}

export function shellTargetForPlatform(platform = os.platform()): ShellTarget {
  return platform === "win32" ? "windows" : "unix";
}

export function buildCommand(opts: TunnelCommandOptions, target = shellTargetForPlatform()): string {
  const { host, name, nameSeed, relayList, relayUrl, thumbnail, isLocal } = opts;
  const installShellUrl = `${relayUrl}/install.sh`;
  const installPowerShellUrl = `${relayUrl}/install.ps1`;
  const exposeArgs: string[] = [];
  const resolvedName = resolveExposeName(name, host, nameSeed);

  exposeArgs.push(`--name ${formatToken(resolvedName, target)}`);
  if (relayList.trim()) {
    exposeArgs.push(`--relays ${formatToken(relayList, target)}`);
  }
  if (thumbnail.trim()) {
    exposeArgs.push(`--thumbnail ${formatToken(thumbnail.trim(), target)}`);
  }

  const exposeCommand = `expose ${[...exposeArgs, formatToken(host, target)].join(" ")}`;

  if (target === "windows") {
    const commandLines = [`$ProgressPreference = 'SilentlyContinue'`];
    if (relayUrl.trim()) {
      commandLines.push(`irm ${formatToken(installPowerShellUrl, target)} | iex`);
    }
    commandLines.push(`$PortalBin = Join-Path $env:LOCALAPPDATA 'portal\\bin\\portal.exe'`);
    commandLines.push(`if (-not (Test-Path $PortalBin)) { throw "portal CLI not found. Install from a relay first or configure portal.relayUrls." }`);
    commandLines.push(`& $PortalBin ${exposeCommand}`);
    return commandLines.join("\n");
  }

  const commandLines: string[] = [];
  if (relayUrl.trim()) {
    const curlFlags = isLocal ? "-kfsSL" : "-fsSL";
    commandLines.push(`curl ${curlFlags} ${formatToken(installShellUrl, target)} | bash`);
  }
  commandLines.push(`PORTAL_BIN="$(command -v portal 2>/dev/null || true)"`);
  commandLines.push(`if [ -z "$PORTAL_BIN" ]; then`);
  commandLines.push(`  for candidate in "$HOME/.local/bin/portal" "$HOME/bin/portal"; do`);
  commandLines.push(`    if [ -x "$candidate" ]; then PORTAL_BIN="$candidate"; break; fi`);
  commandLines.push(`  done`);
  commandLines.push(`fi`);
  commandLines.push(`if [ -z "$PORTAL_BIN" ]; then echo "portal CLI not found. Install from a relay first or configure portal.relayUrls." >&2; exit 1; fi`);
  commandLines.push(`"${"$"}PORTAL_BIN" ${exposeCommand}`);
  return commandLines.join("\n");
}

export function isLocalhost(url: string): boolean {
  try {
    const h = new URL(url).hostname.toLowerCase();
    return h === "localhost" || h === "127.0.0.1" || h === "::1" || h.endsWith(".localhost");
  } catch {
    return false;
  }
}

function quoteShellValue(value: string): string {
  return "'" + value.replace(/'/g, `'\"'\"'`) + "'";
}

function quotePowerShellValue(value: string): string {
  return `'${value.replace(/'/g, "''")}'`;
}

function formatToken(value: string, target: ShellTarget): string {
  if (/^[A-Za-z0-9:/.=_,-]+$/.test(value)) {
    return value;
  }
  return target === "windows" ? quotePowerShellValue(value) : quoteShellValue(value);
}
