import * as vscode from "vscode";

import {
  buildCommand,
  defaultRelayRegistryURL,
  isLocalhost,
  validateRelayUrl,
} from "./command";

let tunnelTerminal: vscode.Terminal | undefined;
const defaultTunnelHost = "localhost:3000";

interface RelaySelection {
  relayUrls: string[];
  installRelayUrl: string;
}

export function activate(context: vscode.ExtensionContext) {
  context.subscriptions.push(
    vscode.commands.registerCommand("portal.startTunnel", startTunnel),
    vscode.commands.registerCommand("portal.startTunnelAdvanced", startTunnelAdvanced),
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

  const relaySelection = await resolveRelaySelection(false);
  if (!relaySelection) { return; }

  runTunnelCommand({
    host,
    name: "",
    relaySelection,
    thumbnail: "",
  });
}

async function startTunnelAdvanced() {
  const host = await promptHost();
  if (!host) { return; }

  const name = await promptName();
  if (name === undefined) { return; }

  const relaySelection = await resolveRelaySelection(true);
  if (!relaySelection) { return; }

  const thumbnail = await promptThumbnail();
  if (thumbnail === undefined) { return; }

  runTunnelCommand({
    host,
    name,
    relaySelection,
    thumbnail,
  });
}

function runTunnelCommand(args: {
  host: string;
  name: string;
  relaySelection: RelaySelection;
  thumbnail: string;
}) {
  const command = buildCommand({
    host: args.host,
    name: args.name,
    nameSeed: vscode.env.machineId,
    relayList: args.relaySelection.relayUrls.join(","),
    relayUrl: args.relaySelection.installRelayUrl,
    thumbnail: args.thumbnail,
    isLocal: isLocalhost(args.relaySelection.installRelayUrl),
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
  const defaultHost = config.get<string>("defaultHost") ?? defaultTunnelHost;
  return vscode.window.showInputBox({
    title: "Portal: Local Host",
    prompt: "Hostname or IP:Port where your service is running",
    value: defaultHost,
    validateInput: (v) => (v.trim() ? undefined : "Required"),
  });
}

async function promptName(): Promise<string | undefined> {
  const config = vscode.workspace.getConfiguration("portal");
  const defaultName = config.get<string>("defaultName") ?? "";
  return vscode.window.showInputBox({
    title: "Portal: Service Name",
    prompt: "Optional public hostname prefix. Leave empty to auto-generate a stable default name.",
    value: defaultName,
  });
}

async function resolveRelaySelection(interactive: boolean): Promise<RelaySelection | undefined> {
  const config = vscode.workspace.getConfiguration("portal");
  const saved = config.get<string[]>("relayUrls") ?? [];
  if (saved.length > 0) {
    const invalid = saved.find((url) => validateRelayUrl(url) !== undefined);
    if (invalid) {
      vscode.window.showErrorMessage("portal.relayUrls must contain only valid https:// relay URLs.");
      return undefined;
    }
    const relayUrls = saved.map((url) => url.trim());
    return {
      relayUrls,
      installRelayUrl: relayUrls[0],
    };
  }

  if (!interactive) {
    return {
      relayUrls: [],
      installRelayUrl: "",
    };
  }

  const choice = await vscode.window.showQuickPick([
    {
      label: "Use default public registry",
      description: defaultRelayRegistryURL,
    },
    {
      label: "Enter relay URL",
      description: "Install from a specific https:// relay",
    },
  ], {
    title: "Portal: Relay Source",
    placeHolder: "Choose a public registry or a specific relay URL",
  });
  if (!choice) {
    return undefined;
  }
  if (choice.label === "Use default public registry") {
    vscode.window.showInformationMessage(`Portal will use the default public registry: ${defaultRelayRegistryURL}`);
    return {
      relayUrls: [],
      installRelayUrl: "",
    };
  }

  const input = await vscode.window.showInputBox({
    title: "Portal: Relay URL",
    prompt: "Relay server URL (e.g. https://my-relay.example.com)",
    validateInput: validateRelayUrl,
  });
  if (!input) {
    return undefined;
  }
  const relayUrl = input.trim();
  return {
    relayUrls: [relayUrl],
    installRelayUrl: relayUrl,
  };
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
  return result;
}

function createTunnelTerminal(): vscode.Terminal {
  if (process.platform !== "win32") {
    return vscode.window.createTerminal("Portal Tunnel");
  }

  return vscode.window.createTerminal({
    name: "Portal Tunnel",
    shellPath: "powershell.exe",
  });
}
