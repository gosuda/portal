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

async function startTunnel() {
  const config = vscode.workspace.getConfiguration("portal");

  // --- host ---
  const defaultHost = config.get<string>("portal.defaultHost") ?? "localhost:3000";
  const host = await vscode.window.showInputBox({
    title: "Portal: Local Host",
    prompt: "hostname or IP:Port where your service is running",
    value: defaultHost,
    validateInput: (v) => (v.trim() ? undefined : "Required"),
  });
  if (!host) { return; }

  // --- name ---
  const workspaceName =
    vscode.workspace.workspaceFolders?.[0]?.name ?? "my-app";
  const defaultName =
    config.get<string>("portal.defaultName") || workspaceName;
  const name = await vscode.window.showInputBox({
    title: "Portal: Service Name",
    prompt: "Unique identifier for your tunnel",
    value: defaultName,
    validateInput: (v) => (v.trim() ? undefined : "Required"),
  });
  if (!name) { return; }

  // --- relay URLs ---
  let relayUrls = config.get<string[]>("portal.relayUrls") ?? [];
  if (relayUrls.length === 0) {
    const input = await vscode.window.showInputBox({
      title: "Portal: Relay URL",
      prompt: "Relay server URL (e.g. https://my-relay.example.com)",
      validateInput: (v) => {
        try {
          new URL(v.trim());
          return undefined;
        } catch {
          return "Enter a valid URL";
        }
      },
    });
    if (!input) { return; }
    relayUrls = [input.trim()];
  }

  const relayUrl = relayUrls[0];
  const relayList = relayUrls.join(",");
  const isLocal = isLocalhost(relayUrl);
  const command = buildCommand(host.trim(), name.trim(), relayList, relayUrl, isLocal);

  // reuse existing terminal or create a new one
  if (tunnelTerminal) {
    tunnelTerminal.dispose();
  }
  tunnelTerminal = vscode.window.createTerminal("Portal Tunnel");
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

function buildCommand(
  host: string,
  name: string,
  relayList: string,
  relayUrl: string,
  isLocal: boolean
): string {
  const tunnelScript = `${relayUrl}/tunnel`;
  if (os.platform() === "win32") {
    const windowsScript = `${tunnelScript}?os=windows`;
    return (
      `$ProgressPreference = 'SilentlyContinue'; ` +
      `$env:HOST="${host}"; $env:NAME="${name}"; $env:RELAY_URL="${relayList}"; ` +
      `irm ${windowsScript} | iex`
    );
  }
  const curlFlags = isLocal ? "-kfsSL" : "-fsSL";
  return `curl ${curlFlags} ${tunnelScript} | APP_HOST=${host} APP_NAME=${name} RELAYS="${relayList}" sh`;
}

function isLocalhost(url: string): boolean {
  try {
    const h = new URL(url).hostname.toLowerCase();
    return h === "localhost" || h === "127.0.0.1" || h === "::1" || h.endsWith(".localhost");
  } catch {
    return false;
  }
}

export function deactivate() {
  tunnelTerminal?.dispose();
}
