import { useMemo, useState } from "react";
import { Check, Copy, Terminal, X } from "lucide-react";
import { cn } from "@/lib/utils";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogTrigger,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { API_PATHS } from "@/lib/apiPaths";

interface TunnelCommandModalProps {
  trigger?: React.ReactNode;
}

export function TunnelCommandModal({ trigger }: TunnelCommandModalProps) {
  const defaultHost = "localhost:3000";
  const defaultName = "your-app-name";

  // Get current host URL dynamically
  const currentOrigin = useMemo(() => {
    if (typeof window !== "undefined") {
      return window.location.origin;
    }
    return "https://localhost:4017";
  }, []);

  const [host, setHost] = useState(defaultHost);
  const [name, setName] = useState(defaultName);
  const [relayUrls, setRelayUrls] = useState<string[]>([currentOrigin]);
  const [urlInput, setUrlInput] = useState("");
  const [copied, setCopied] = useState(false);
  const [os, setOs] = useState<"unix" | "windows">("unix");
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
    const hostVal = host.trim() === "" ? defaultHost : host.trim();
    const nameVal = name.trim() === "" ? defaultName : name.trim();
    const relayUrlVal =
      relayUrls.length > 0 ? relayUrls.join(",") : currentOrigin;
    const tunnelScriptURL = new URL(API_PATHS.tunnel, currentOrigin).toString();
    const localhostRelay = isLocalRelayOrigin(currentOrigin);

    if (os === "windows") {
      const windowsScriptURL = new URL(tunnelScriptURL);
      windowsScriptURL.searchParams.set("os", "windows");
      const envAssignments = [
        "$ProgressPreference = 'SilentlyContinue'",
        `$env:APP_HOST=${quotePowerShellValue(hostVal)}`,
        `$env:APP_NAME=${quotePowerShellValue(nameVal)}`,
        `$env:RELAYS=${quotePowerShellValue(relayUrlVal)}`,
      ];
      if (normalizedThumbnailURL) {
        envAssignments.push(
          `$env:APP_THUMBNAIL=${quotePowerShellValue(normalizedThumbnailURL)}`
        );
      }
      return `${envAssignments.join("; ")}; irm ${quotePowerShellValue(
        windowsScriptURL.toString()
      )} | iex`;
    }

    const curlFlags = localhostRelay ? "-kfsSL" : "-fsSL";
    const envAssignments = [
      `APP_HOST=${quoteShellValue(hostVal)}`,
      `APP_NAME=${quoteShellValue(nameVal)}`,
      `RELAYS=${quoteShellValue(relayUrlVal)}`,
    ];
    if (normalizedThumbnailURL) {
      envAssignments.push(
        `APP_THUMBNAIL=${quoteShellValue(normalizedThumbnailURL)}`
      );
    }
    return `curl ${curlFlags} ${quoteShellValue(
      tunnelScriptURL
    )} | ${envAssignments.join(" ")} sh`;
  }, [currentOrigin, host, name, normalizedThumbnailURL, relayUrls, os]);

  const handleCopy = async () => {
    try {
      await navigator.clipboard.writeText(command);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch (err) {
      console.error("Failed to copy:", err);
    }
  };

  return (
    <Dialog>
      <DialogTrigger asChild>
        {trigger || (
          <Button className="cursor-pointer">
            <span className="truncate">Add Your Server</span>
          </Button>
        )}
      </DialogTrigger>
      <DialogContent className="sm:max-w-[500px] rounded-sm max-h-[85vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <Terminal className="w-5 h-5" />
            Tunnel Setup Command
          </DialogTitle>
          <DialogDescription>
            Set options and copy the command.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4 py-4 [&>div]:flex [&>div]:flex-col">
          {/* Host Input */}
          <div className="space-y-2">
            <label
              htmlFor="host"
              className="text-sm font-medium text-foreground"
            >
              Host
            </label>
            <div className="flex items-center rounded-md bg-border">
              <span className="px-3 text-sm text-text-muted">APP_HOST=</span>
              <Input
                id="host"
                type="text"
                value={host}
                onChange={(e) => setHost(e.target.value)}
                placeholder={defaultHost}
                className="rounded-l-none"
              />
            </div>
            <p className="text-xs text-text-muted">
              The hostname or IP:Port where your service is running
            </p>
          </div>

          {/* Name Input */}
          <div className="space-y-2">
            <label
              htmlFor="name"
              className="text-sm font-medium text-foreground"
            >
              Service Name
            </label>
            <div className="flex items-center rounded-md bg-border">
              <span className="px-3 text-sm text-text-muted">APP_NAME=</span>
              <Input
                id="name"
                type="text"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder={defaultName}
                className="rounded-l-none"
              />
            </div>
            <p className="text-xs text-text-muted">
              A unique identifier for your tunnel
            </p>
          </div>

          {/* Relay URLs Input */}
          <div className="space-y-2">
            <label className="text-sm font-medium text-foreground">
              Relay URLs
            </label>
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
            <p className="text-xs text-text-muted">
              Press Enter to add. Multiple relay servers for redundancy.
            </p>
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
            <p className="text-xs text-text-muted">
              Image URL passed to `portal-tunnel`.
            </p>
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
