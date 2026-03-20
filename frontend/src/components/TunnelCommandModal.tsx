import { useMemo, useState } from "react";
import { Check, Copy, Terminal, X } from "lucide-react";
import { cn } from "@/lib/utils";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { API_PATHS } from "@/lib/apiPaths";

interface TunnelCommandModalProps {
  trigger?: React.ReactNode;
}

export function TunnelCommandModal({ trigger }: TunnelCommandModalProps) {
  const defaultHost = "3000";

  // Get current host URL dynamically
  const currentOrigin = useMemo(() => {
    if (typeof window !== "undefined") {
      return window.location.origin;
    }
    return "https://localhost:4017";
  }, []);

  const [open, setOpen] = useState(false);
  const [target, setTarget] = useState(defaultHost);
  const [name, setName] = useState("");
  const [relayUrls, setRelayUrls] = useState<string[]>([currentOrigin]);
  const [defaultRelays, setDefaultRelays] = useState(true);
  const [urlInput, setUrlInput] = useState("");
  const [copied, setCopied] = useState(false);
  const [os, setOs] = useState<"unix" | "windows">("unix");
  const [enableUDP, setEnableUDP] = useState(false);
  const [udpPort, setUdpPort] = useState("");
  const [thumbnailURL, setThumbnailURL] = useState("");
  const normalizedThumbnailURL = useMemo(
    () => normalizeAbsoluteHTTPURL(thumbnailURL),
    [thumbnailURL]
  );
  const thumbnailError = useMemo(() => {
    if (thumbnailURL.trim() === "") {
      return "";
    }
    if (normalizedThumbnailURL !== "") {
      return "";
    }
    return "Thumbnail must be an absolute http:// or https:// URL.";
  }, [thumbnailURL, normalizedThumbnailURL]);

  const addRelayUrl = (url: string) => {
    const trimmed = url.trim();
    if (!trimmed || relayUrls.includes(trimmed)) return;
    // Basic URL validation
    try {
      new URL(trimmed);
      setRelayUrls([...relayUrls, trimmed]);
      setUrlInput("");
    } catch {
      // Invalid URL, ignore
    }
  };

  const removeRelayUrl = (url: string) => {
    setRelayUrls(relayUrls.filter((u) => u !== url));
  };

  const handleUrlKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Enter") {
      e.preventDefault();
      addRelayUrl(urlInput);
    } else if (
      e.key === "Backspace" &&
      urlInput === "" &&
      relayUrls.length > 0
    ) {
      // Remove last URL when backspace on empty input
      setRelayUrls(relayUrls.slice(0, -1));
    }
  };

  // Generate the tunnel command
  const command = useMemo(() => {
    const targetVal = target.trim() === "" ? defaultHost : target.trim();
    const nameVal = name.trim();
    const relayUrlVal =
      relayUrls.length > 0 ? relayUrls.join(",") : currentOrigin;
    const installScriptURL = new URL(
      API_PATHS.install.shell,
      currentOrigin
    ).toString();
    const installPowerShellURL = new URL(
      API_PATHS.install.powershell,
      currentOrigin
    ).toString();
    const localhostRelay = isLocalRelayOrigin(currentOrigin);

    const exposeArgs: string[] = [];

    if (nameVal !== "") {
      exposeArgs.push(`--name ${formatToken(nameVal, os)}`);
    }
    if (relayUrls.length > 0) {
      exposeArgs.push(`--relays ${formatToken(relayUrlVal, os)}`);
    }
    if (!defaultRelays) {
      exposeArgs.push("--default-relays=false");
    }
    if (normalizedThumbnailURL) {
      exposeArgs.push(`--thumbnail ${formatToken(normalizedThumbnailURL, os)}`);
    }
    if (enableUDP) {
      exposeArgs.push("--udp");
      const udpAddrVal = udpPort.trim();
      if (udpAddrVal !== "") {
        exposeArgs.push(`--udp-addr ${formatToken(udpAddrVal, os)}`);
      }
    }

    if (os === "windows") {
      return [
        `$ProgressPreference = 'SilentlyContinue'`,
        `irm ${formatToken(installPowerShellURL, os)} | iex`,
        `portal expose ${[...exposeArgs, formatToken(targetVal, os)].join(" ")}`,
      ].join("\n");
    }

    const curlFlags = localhostRelay ? "-ksSL" : "-sSL";
    return [
      `curl ${curlFlags} ${formatToken(installScriptURL, os)} | bash`,
      `portal expose ${[...exposeArgs, formatToken(targetVal, os)].join(" ")}`,
    ].join("\n");
  }, [
    currentOrigin,
    defaultRelays,
    enableUDP,
    name,
    normalizedThumbnailURL,
    os,
    relayUrls,
    target,
    udpPort,
  ]);

  const handleCopy = async () => {
    try {
      await navigator.clipboard.writeText(command);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch (err) {
      console.error("Failed to copy:", err);
    }
  };

  const handleOpenChange = (nextOpen: boolean) => {
    setOpen(nextOpen);
    if (!nextOpen) {
      return;
    }
    setTarget(defaultHost);
    setName("");
    setRelayUrls([currentOrigin]);
    setDefaultRelays(true);
    setUrlInput("");
    setCopied(false);
    setOs("unix");
    setEnableUDP(false);
    setUdpPort("");
    setThumbnailURL("");
  };

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogTrigger asChild>
        {trigger || (
          <Button className="cursor-pointer">
            <span className="truncate">Add Your Server</span>
          </Button>
        )}
      </DialogTrigger>
      <DialogContent className="sm:max-w-[520px] rounded-sm max-h-[85vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <Terminal className="w-5 h-5" />
            Tunnel Setup Command
          </DialogTitle>
        </DialogHeader>

        <div className="space-y-4 py-4 [&>div]:flex [&>div]:flex-col">
          {/* Host Input */}
          <div className="space-y-2">
            <label
              htmlFor="target"
              className="text-sm font-medium text-foreground"
            >
              Host
            </label>
            <Input
              id="target"
              type="text"
              value={target}
              onChange={(e) => setTarget(e.target.value)}
              placeholder={defaultHost}
            />
          </div>

          {/* Name Input */}
          <div className="space-y-2">
            <label
              htmlFor="name"
              className="text-sm font-medium text-foreground"
            >
              Service Name
            </label>
            <Input
              id="name"
              type="text"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="auto-generated when empty"
            />
          </div>

          {/* Relay URLs Input */}
          <div className="space-y-2">
            <div className="flex items-center justify-between gap-3">
              <label className="text-sm font-medium text-foreground">
                Relay URLs
              </label>
              <label className="flex items-center gap-2 text-xs text-muted-foreground">
                <input
                  type="checkbox"
                  checked={defaultRelays}
                  onChange={(e) => setDefaultRelays(e.target.checked)}
                  className="h-4 w-4"
                />
                <span>Include default registry</span>
              </label>
            </div>
            <div className="flex flex-wrap items-center gap-2 rounded-md border border-input bg-transparent p-2 min-h-10">
              {relayUrls.map((url) => (
                <span
                  key={url}
                  className="inline-flex items-center gap-1 rounded-md bg-secondary px-2 py-1 text-xs font-medium text-secondary-foreground"
                >
                  {url}
                  <button
                    type="button"
                    onClick={() => removeRelayUrl(url)}
                    className="ml-1 rounded-sm hover:bg-destructive/20 p-0.5"
                    aria-label={`Remove ${url}`}
                  >
                    <X className="h-3 w-3" />
                  </button>
                </span>
              ))}
              <input
                type="text"
                value={urlInput}
                onChange={(e) => setUrlInput(e.target.value)}
                onKeyDown={handleUrlKeyDown}
                placeholder="Add relay URL..."
                className="min-w-[140px] flex-1 bg-transparent text-sm outline-none placeholder:text-muted-foreground"
                />
              </div>
            </div>

          {/* UDP Transport */}
          <div className="space-y-2">
            <label className="text-sm font-medium text-foreground">
              UDP Transport
            </label>
            <label className="flex items-center gap-2 cursor-pointer">
              <input
                type="checkbox"
                checked={enableUDP}
                onChange={(e) => {
                  setEnableUDP(e.target.checked);
                  if (!e.target.checked) {
                    setUdpPort("");
                  }
                }}
                className="h-4 w-4"
              />
              <span className="text-sm text-muted-foreground">
                Enable UDP transport (for game servers, VoIP, etc.)
              </span>
            </label>
            {enableUDP && (
              <div className="space-y-1.5">
                <Input
                  id="udp-port"
                  type="text"
                  value={udpPort}
                  onChange={(e) => setUdpPort(e.target.value)}
                  placeholder={target.trim() || defaultHost}
                />
                <p className="text-xs text-muted-foreground">
                  Local UDP port to forward. Defaults to the same as Host.
                </p>
              </div>
            )}
          </div>

          <div className="space-y-2">
            <label
              htmlFor="thumbnail-url"
              className="text-sm font-medium text-foreground"
            >
              Thumbnail URL
            </label>
            <Input
              id="thumbnail-url"
              type="url"
              value={thumbnailURL}
              onChange={(e) => setThumbnailURL(e.target.value)}
              placeholder="https://cdn.example.com/thumb.png"
            />
            {normalizedThumbnailURL && (
              <div className="flex h-20 w-20 items-center justify-center overflow-hidden rounded-md border border-input bg-background">
                <img
                  src={normalizedThumbnailURL}
                  alt="Thumbnail preview"
                  className="h-full w-full object-cover"
                />
              </div>
            )}
            {thumbnailError && (
              <p className="text-xs text-destructive">{thumbnailError}</p>
            )}
          </div>

          {/* OS Selection */}
          <div className="space-y-2">
            <label className="text-sm font-medium text-foreground">
              Operating System
            </label>
            <div className="flex p-1 bg-border rounded-md">
              <button
                onClick={() => setOs("unix")}
                className={cn(
                  "flex-1 px-3 py-1.5 text-sm font-medium rounded-sm transition-all",
                  os === "unix"
                    ? "bg-background text-foreground shadow-sm"
                    : "text-muted-foreground hover:text-foreground"
                )}
              >
                Linux / macOS
              </button>
              <button
                onClick={() => setOs("windows")}
                className={cn(
                  "flex-1 px-3 py-1.5 text-sm font-medium rounded-sm transition-all",
                  os === "windows"
                    ? "bg-background text-foreground shadow-sm"
                    : "text-muted-foreground hover:text-foreground"
                )}
              >
                Windows (PowerShell)
              </button>
            </div>
          </div>

          {/* Generated Command */}
          <div className="space-y-2">
            <label className="text-sm font-medium text-foreground">
              Generated Command
            </label>
            <div className="relative">
              <pre className="p-3 pr-12 rounded-md bg-border text-sm text-foreground overflow-x-auto whitespace-pre-wrap break-all font-mono">
                {command}
              </pre>
              <button
                onClick={handleCopy}
                className="cursor-pointer absolute right-2 top-1/2 -translate-y-1/2 p-2 rounded-md hover:bg-background/50 transition-colors"
                aria-label="Copy command"
              >
                {copied ? (
                  <Check className="w-4 h-4 text-green-500" />
                ) : (
                  <Copy className="w-4 h-4 text-text-muted" />
                )}
              </button>
            </div>
            <p className="text-xs text-muted-foreground">
              After installation, run <code>portal list</code> to inspect the
              configured public relays.
            </p>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}

function isLocalRelayOrigin(origin: string): boolean {
  try {
    const parsed = new URL(origin);
    const host = parsed.hostname.trim().toLowerCase();
    return (
      host === "localhost" ||
      host === "127.0.0.1" ||
      host === "::1" ||
      host.endsWith(".localhost")
    );
  } catch {
    return false;
  }
}

function quoteShellValue(value: string): string {
  return "'" + value.replace(/'/g, `'"'"'`) + "'";
}

function quotePowerShellValue(value: string): string {
  return `'${value.replace(/'/g, "''")}'`;
}

function formatToken(value: string, os: "unix" | "windows"): string {
  if (/^[A-Za-z0-9:/.=_-]+$/.test(value)) {
    return value;
  }
  return os === "windows" ? quotePowerShellValue(value) : quoteShellValue(value);
}

function normalizeAbsoluteHTTPURL(raw: string): string {
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
