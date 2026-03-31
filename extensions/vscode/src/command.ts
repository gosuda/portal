import * as os from "os";

export type ShellTarget = "unix" | "windows";

export const defaultRelayRegistryURL = "https://raw.githubusercontent.com/gosuda/portal/main/registry.json";
export const defaultTunnelDownloadBaseURL = "https://github.com/gosuda/portal/releases/latest/download";

export interface TunnelCommandOptions {
  host: string;
  name: string;
  relayList: string;
  thumbnail: string;
  tunnelBinaryURL?: string;
}

export interface TunnelCommandRuntime {
  shellTarget: ShellTarget;
  platform: NodeJS.Platform;
  arch: string;
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

export function defaultTunnelCommandRuntime(
  platform = os.platform(),
  arch = os.arch()
): TunnelCommandRuntime {
  return {
    shellTarget: shellTargetForPlatform(platform),
    platform,
    arch,
  };
}

export function resolveTunnelBinaryURL(
  platform = os.platform(),
  arch = os.arch()
): string | undefined {
  const platformName =
    platform === "darwin" || platform === "linux"
      ? platform
      : platform === "win32"
        ? "windows"
        : undefined;
  const archName =
    arch === "x64"
      ? "amd64"
      : arch === "arm64"
        ? "arm64"
        : undefined;
  if (!platformName || !archName) {
    return undefined;
  }
  const extension = platformName === "windows" ? ".exe" : "";
  return `${defaultTunnelDownloadBaseURL}/portal-${platformName}-${archName}${extension}`;
}

export function buildCommand(
  opts: TunnelCommandOptions,
  runtime = defaultTunnelCommandRuntime()
): string {
  const { host, name, relayList, thumbnail } = opts;
  const target = runtime.shellTarget;
  const tunnelBinaryURL =
    opts.tunnelBinaryURL?.trim() ||
    resolveTunnelBinaryURL(runtime.platform, runtime.arch);
  if (!tunnelBinaryURL) {
    throw new Error(
      `Unsupported platform ${runtime.platform}/${runtime.arch}. Portal supports macOS, Linux, and Windows on x64 or arm64.`
    );
  }
  const exposeArgs: string[] = [];

  const trimmedName = name.trim();
  if (trimmedName) {
    exposeArgs.push(`--name ${formatToken(trimmedName, target)}`);
  }
  if (relayList.trim()) {
    exposeArgs.push(`--relays ${formatToken(relayList, target)}`);
  }
  if (thumbnail.trim()) {
    exposeArgs.push(`--thumbnail ${formatToken(thumbnail.trim(), target)}`);
  }

  const exposeCommand = `expose ${[formatToken(host, target), ...exposeArgs].join(" ")}`;

  if (target === "windows") {
    const commandLines = [
      `$ProgressPreference = 'SilentlyContinue'`,
      `$PortalDir = Join-Path $env:LOCALAPPDATA 'portal\\bin'`,
      `$PortalBin = Join-Path $PortalDir 'portal.exe'`,
      `$PortalTmp = Join-Path $PortalDir 'portal.exe.download'`,
      `New-Item -ItemType Directory -Force -Path $PortalDir | Out-Null`,
      `Invoke-WebRequest -Uri ${formatToken(tunnelBinaryURL, target)} -OutFile $PortalTmp`,
      `Move-Item -Force $PortalTmp $PortalBin`,
    ];
    commandLines.push(`& $PortalBin ${exposeCommand}`);
    return commandLines.join("\n");
  }

  const commandLines = [
    `PORTAL_BIN="$HOME/.local/bin/portal"`,
    `PORTAL_TMP="$PORTAL_BIN.download"`,
    `mkdir -p "$(dirname "$PORTAL_BIN")"`,
    `curl -fsSL ${formatToken(tunnelBinaryURL, target)} -o "$PORTAL_TMP"`,
    `chmod +x "$PORTAL_TMP"`,
    `mv "$PORTAL_TMP" "$PORTAL_BIN"`,
    `"${"$"}PORTAL_BIN" ${exposeCommand}`,
  ];
  return commandLines.join("\n");
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
