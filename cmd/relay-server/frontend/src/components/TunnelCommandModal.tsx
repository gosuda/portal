import { useState, useMemo, useRef } from "react";
import { Copy, Check, Terminal, X } from "lucide-react";
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

interface TunnelCommandModalProps {
  trigger?: React.ReactNode;
}

type TLSMode = "no-tls" | "self" | "keyless";

export function TunnelCommandModal({ trigger }: TunnelCommandModalProps) {
  const defaultHost = "localhost:3000";
  const defaultName = "your-app-name";

  // Get current host URL dynamically
  const currentOrigin = useMemo(() => {
    if (typeof window !== "undefined") {
      return window.location.origin;
    }
    return "http://localhost:4017";
  }, []);

  const [host, setHost] = useState(defaultHost);
  const [name, setName] = useState(defaultName);
  const [relayUrls, setRelayUrls] = useState<string[]>([currentOrigin]);
  const [urlInput, setUrlInput] = useState("");
  const [copied, setCopied] = useState(false);
  const [os, setOs] = useState<"unix" | "windows">("unix");
  const [tlsMode, setTlsMode] = useState<TLSMode>("keyless");
  const [tlsCertFile, setTlsCertFile] = useState("");
  const [tlsKeyFile, setTlsKeyFile] = useState("");
  const urlInputRef = useRef<HTMLInputElement>(null);

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
    const hostVal = host === "" ? defaultHost : host;
    const nameVal = name === "" ? defaultName : name;
    const relayUrlVal =
      relayUrls.length > 0 ? relayUrls.join(",") : currentOrigin;

    if (os === "windows") {
      let tlsEnv = `$env:TLS_MODE="${tlsMode}"; `;
      if (tlsMode === "self" && tlsCertFile.trim() !== "") {
        tlsEnv += `$env:TLS_CERT_FILE="${tlsCertFile}"; `;
      }
      if (tlsMode === "self" && tlsKeyFile.trim() !== "") {
        tlsEnv += `$env:TLS_KEY_FILE="${tlsKeyFile}"; `;
      }
      return `$ProgressPreference = 'SilentlyContinue'; ${tlsEnv}$env:HOST="${hostVal}"; $env:NAME="${nameVal}"; $env:RELAY_URL="${relayUrlVal}"; irm ${currentOrigin}/tunnel?os=windows | iex`;
    }

    let tlsEnv = `TLS_MODE=${tlsMode} `;
    if (tlsMode === "self" && tlsCertFile.trim() !== "") {
      tlsEnv += `TLS_CERT_FILE="${tlsCertFile}" `;
    }
    if (tlsMode === "self" && tlsKeyFile.trim() !== "") {
      tlsEnv += `TLS_KEY_FILE="${tlsKeyFile}" `;
    }
    return `curl -fsSL ${currentOrigin}/tunnel | ${tlsEnv}HOST=${hostVal} NAME=${nameVal} RELAY_URL="${relayUrlVal}" sh`;
  }, [
    currentOrigin,
    host,
    name,
    relayUrls,
    os,
    tlsMode,
    tlsCertFile,
    tlsKeyFile,
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

  const isNoTLS = tlsMode === "no-tls";
  const isSelfTLS = tlsMode === "self";

  const onNoTLSToggle = (checked: boolean) => {
    if (checked) {
      setTlsMode("no-tls");
      return;
    }
    if (tlsMode === "no-tls") {
      setTlsMode("keyless");
    }
  };

  const onSelfTLSToggle = (checked: boolean) => {
    if (checked) {
      setTlsMode("self");
      return;
    }
    if (tlsMode === "self") {
      setTlsMode("keyless");
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
              <span className="px-3 text-sm text-text-muted">HOST=</span>
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
              <span className="px-3 text-sm text-text-muted">NAME=</span>
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
                ref={urlInputRef}
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

          {/* TLS Mode */}
          <div className="space-y-2">
            <label className="text-sm font-medium text-foreground">
              TLS Mode
            </label>
            <div className="space-y-2 rounded-md border border-input/80 px-3 py-2">
              <label className="flex items-center justify-between gap-3 text-sm text-foreground">
                <span className="flex items-center gap-2">
                  <input
                    type="checkbox"
                    checked={isNoTLS}
                    onChange={(e) => onNoTLSToggle(e.target.checked)}
                    className="h-4 w-4"
                  />
                  No TLS
                </span>
                <span className="text-xs text-text-muted text-right">
                  Local-only testing mode.
                </span>
              </label>
              <label className="flex items-center justify-between gap-3 text-sm text-foreground">
                <span className="flex items-center gap-2">
                  <input
                    type="checkbox"
                    checked={isSelfTLS}
                    onChange={(e) => onSelfTLSToggle(e.target.checked)}
                    className="h-4 w-4"
                  />
                  Self TLS
                </span>
                <span className="text-xs text-text-muted text-right">
                  Bring your own cert and key.
                </span>
              </label>
            </div>
            <p className="text-xs text-text-muted">
              {tlsMode === "no-tls"
                ? "Local testing only (no TLS)."
                : tlsMode === "keyless"
                  ? "Recommended: Keyless TLS."
                  : "Advanced: Self-managed TLS cert/key."}
            </p>
          </div>

          {tlsMode === "self" && (
            <div className="space-y-3 rounded-md border border-input/80 p-3">
              <p className="text-xs font-medium text-foreground">Self TLS Files</p>
              <div className="space-y-2">
                <label
                  htmlFor="tls-cert-file"
                  className="text-sm font-medium text-foreground"
                >
                  TLS Cert File
                </label>
                <div className="flex items-center rounded-md bg-border">
                  <span className="px-3 text-sm text-text-muted">TLS_CERT_FILE=</span>
                  <Input
                    id="tls-cert-file"
                    type="text"
                    value={tlsCertFile}
                    onChange={(e) => setTlsCertFile(e.target.value)}
                    placeholder="/path/to/fullchain.pem"
                    className="rounded-l-none"
                  />
                </div>
              </div>
              <div className="space-y-2">
                <label
                  htmlFor="tls-key-file"
                  className="text-sm font-medium text-foreground"
                >
                  TLS Key File
                </label>
                <div className="flex items-center rounded-md bg-border">
                  <span className="px-3 text-sm text-text-muted">TLS_KEY_FILE=</span>
                  <Input
                    id="tls-key-file"
                    type="text"
                    value={tlsKeyFile}
                    onChange={(e) => setTlsKeyFile(e.target.value)}
                    placeholder="/path/to/privkey.pem"
                    className="rounded-l-none"
                  />
                </div>
              </div>
              <p className="text-xs text-text-muted">
                Self TLS selected. These files are included in the command.
              </p>
            </div>
          )}

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
