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
  const tunnelScript = `${relayUrl}/tunnel`;
  const thumbEnv = thumbnail ? ` APP_THUMBNAIL=${thumbnail}` : "";

  if (os.platform() === "win32") {
    const thumbEnvWin = thumbnail ? ` $env:APP_THUMBNAIL="${thumbnail}";` : "";
    return (
      `$ProgressPreference = 'SilentlyContinue'; ` +
      `$env:APP_HOST="${host}"; $env:APP_NAME="${name}"; $env:RELAYS="${relayList}";${thumbEnvWin} ` +
      `irm ${tunnelScript}?os=windows | iex`
    );
  }

  const curlFlags = isLocal ? "-kfsSL" : "-fsSL";
  return `curl ${curlFlags} ${tunnelScript} | APP_HOST=${host} APP_NAME=${name}${thumbEnv} RELAYS="${relayList}" sh`;
}

function createTunnelTerminal(): vscode.Terminal {
  if (os.platform() !== "win32") {
    return vscode.window.createTerminal("Portal Tunnel");
  }

  // Force PowerShell on Windows so command syntax is consistent even if the
  // user's default profile is cmd/WSL/Git Bash.
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
