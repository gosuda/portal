import * as vscode from "vscode";
import * as os from "os";

let tunnelTerminal: vscode.Terminal | undefined;

export function activate(context: vscode.ExtensionContext) {
  context.subscriptions.push(
    vscode.commands.registerCommand("portal.startTunnel", startTunnel),
    vscode.commands.registerCommand("portal.stopTunnel", stopTunnel),
    vscode.window.onDidCloseTerminal((t) => {
      if (t === tunnelTerminal) {
        tunnelTerminal = undefined;
      }
    })
  );
}

export function deactivate() {
  tunnelTerminal?.dispose();
}

async function startTunnel() {
  const host = await promptHost();
  if (!host) { return; }

  const name = await promptName();
  if (!name) { return; }

  const relayUrls = await promptRelayUrls();
  if (!relayUrls) { return; }

  const thumbnail = await promptThumbnail();
  if (thumbnail === undefined) { return; }

  const relayUrl = relayUrls[0];
  const command = buildCommand({
    host,
    name,
    relayList: relayUrls.join(","),
    relayUrl,
    thumbnail,
    isLocal: isLocalhost(relayUrl),
  });

  if (tunnelTerminal) {
    tunnelTerminal.dispose();
  }
  tunnelTerminal = createTunnelTerminal();
  tunnelTerminal.show();
  tunnelTerminal.sendText(command);
}

function stopTunnel() {
  if (tunnelTerminal) {
    tunnelTerminal.dispose();
    tunnelTerminal = undefined;
    vscode.window.showInformationMessage("Portal tunnel stopped.");
  } else {
    vscode.window.showWarningMessage("No active Portal tunnel.");
  }
}

async function promptHost(): Promise<string | undefined> {
  const config = vscode.workspace.getConfiguration("portal");
  const defaultHost = config.get<string>("defaultHost") ?? "localhost:3000";
  return vscode.window.showInputBox({
    title: "Portal: Local Host",
    prompt: "Hostname or IP:Port where your service is running",
    value: defaultHost,
    validateInput: (v) => (v.trim() ? undefined : "Required"),
  });
}

async function promptName(): Promise<string | undefined> {
  const config = vscode.workspace.getConfiguration("portal");
  const workspaceName = vscode.workspace.workspaceFolders?.[0]?.name ?? "my-app";
  const defaultName = config.get<string>("defaultName") || workspaceName;
  return vscode.window.showInputBox({
    title: "Portal: Service Name",
    prompt: "Unique identifier for your tunnel",
    value: defaultName,
    validateInput: (v) => (v.trim() ? undefined : "Required"),
  });
}

async function promptRelayUrls(): Promise<string[] | undefined> {
  const config = vscode.workspace.getConfiguration("portal");
  const saved = config.get<string[]>("relayUrls") ?? [];
  if (saved.length > 0) { return saved; }

  const input = await vscode.window.showInputBox({
    title: "Portal: Relay URL",
    prompt: "Relay server URL (e.g. https://my-relay.example.com)",
    validateInput: (v) => {
      try { new URL(v.trim()); return undefined; } catch { return "Enter a valid URL"; }
    },
  });
  return input ? [input.trim()] : undefined;
}

async function promptThumbnail(): Promise<string | undefined> {
  const result = await vscode.window.showInputBox({
    title: "Portal: Thumbnail URL (optional)",
    prompt: "Image URL to display as thumbnail. Leave empty to skip.",
    placeHolder: "https://example.com/image.png",
    validateInput: (v) => {
      if (!v.trim()) { return undefined; }
      try { new URL(v.trim()); return undefined; } catch { return "Enter a valid URL or leave empty"; }
    },
  });
  // undefined = user pressed Escape, "" = user skipped
  return result;
}

interface TunnelCommandOptions {
  host: string;
  name: string;
  relayList: string;
  relayUrl: string;
  thumbnail: string;
  isLocal: boolean;
}

function buildCommand(opts: TunnelCommandOptions): string {
  const { host, name, relayList, relayUrl, thumbnail, isLocal } = opts;
  const target = os.platform() === "win32" ? "windows" : "unix";
  const installShellUrl = `${relayUrl}/install.sh`;
  const installPowerShellUrl = `${relayUrl}/install.ps1`;
  const exposeArgs: string[] = [];

  if (name.trim()) {
    exposeArgs.push(`--name ${formatToken(name.trim(), target)}`);
  }
  exposeArgs.push(`--relays ${formatToken(relayList, target)}`);
  if (thumbnail.trim()) {
    exposeArgs.push(`--thumbnail ${formatToken(thumbnail.trim(), target)}`);
  }

  const exposeCommand = `portal expose ${[...exposeArgs, formatToken(host, target)].join(" ")}`;

  if (os.platform() === "win32") {
    return [
      `$ProgressPreference = 'SilentlyContinue'`,
      `irm ${formatToken(installPowerShellUrl, target)} | iex`,
      exposeCommand,
    ].join("\n");
  }

  const curlFlags = isLocal ? "-kfsSL" : "-fsSL";
  return [
    `curl ${curlFlags} ${formatToken(installShellUrl, target)} | bash`,
    exposeCommand,
  ].join("\n");
}

function createTunnelTerminal(): vscode.Terminal {
  if (os.platform() !== "win32") {
    return vscode.window.createTerminal("Portal Tunnel");
  }

  // Use PowerShell on Windows for non-PowerShell terminal profiles (Git Bash, WSL, etc.).
  return vscode.window.createTerminal({
    name: "Portal Tunnel",
    shellPath: "powershell.exe",
  });
}

function isLocalhost(url: string): boolean {
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

function formatToken(value: string, target: "unix" | "windows"): string {
  if (/^[A-Za-z0-9:/.=_,-]+$/.test(value)) {
    return value;
  }
  return target === "windows" ? quotePowerShellValue(value) : quoteShellValue(value);
}
