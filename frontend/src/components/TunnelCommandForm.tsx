import {
  useEffect,
  useId,
  useMemo,
  useState,
  type ChangeEvent,
  type KeyboardEvent,
} from "react";
import { Check, Copy, RefreshCw, X } from "lucide-react";
import { Input } from "@/components/ui/input";
import { apiClient } from "@/lib/apiClient";
import { API_PATHS } from "@/lib/apiPaths";
import { cn } from "@/lib/utils";
import {
  buildDefaultTunnelName,
  buildTunnelCommand,
  buildTunnelPreviewURL,
  buildTunnelStatusHostname,
  normalizeAbsoluteHTTPURL,
  type TunnelCommandOS,
} from "@/lib/tunnelCommand";

const TUNNEL_STATUS_POLL_MS = 3000;
const DEFAULT_NAME_SEED = "web_portal";

interface TunnelCommandFormProps {
  className?: string;
  theme?: "light" | "terminal";
  mode?: "full" | "hero";
}

type TunnelStatus = "waiting" | "registered" | "alive";

interface TunnelStatusResponse {
  hostname: string;
  registered: boolean;
  service_alive: boolean;
}

export function TunnelCommandForm({
  className,
  theme = "light",
  mode = "full",
}: TunnelCommandFormProps) {
  const defaultHost = "3000";
  const tunnelNameSeedStorageKey = "portal:tunnel-name-seed";
  const inputId = useId();
  const isTerminal = theme === "terminal";
  const isHero = mode === "hero";

  const [currentOrigin] = useState(() => {
    if (typeof window !== "undefined") {
      return window.location.origin;
    }
    return "https://localhost:4017";
  });
  const [nameSeed] = useState(() => {
    if (typeof window === "undefined") {
      return DEFAULT_NAME_SEED;
    }

    try {
      const existing = window.localStorage.getItem(tunnelNameSeedStorageKey);
      if (existing && existing.trim() !== "") {
        return existing;
      }

      const next =
        typeof window.crypto?.randomUUID === "function"
          ? `web_${window.crypto.randomUUID()}`
          : `web_${Math.random().toString(36).slice(2)}${Date.now().toString(36)}`;

      window.localStorage.setItem(tunnelNameSeedStorageKey, next);
      return next;
    } catch {
      return DEFAULT_NAME_SEED;
    }
  });

  const [target, setTarget] = useState(defaultHost);
  const [name, setName] = useState("");
  const [isAutoName, setIsAutoName] = useState(true);
  const [nameShuffleKey, setNameShuffleKey] = useState("default");
  const [relayUrls, setRelayUrls] = useState<string[]>([currentOrigin]);
  const [defaultRelays, setDefaultRelays] = useState(true);
  const [urlInput, setUrlInput] = useState("");
  const [copied, setCopied] = useState(false);
  const [os, setOs] = useState<TunnelCommandOS>("unix");
  const [thumbnailURL, setThumbnailURL] = useState("");
  const [enableUDP, setEnableUDP] = useState(false);
  const [udpPort, setUdpPort] = useState("");
  const [tunnelStatus, setTunnelStatus] = useState<TunnelStatus>("waiting");

  const resolvedNameSeed = useMemo(
    () => `${nameSeed}:${nameShuffleKey}`,
    [nameSeed, nameShuffleKey]
  );
  const generatedName = useMemo(
    () => buildDefaultTunnelName(target, resolvedNameSeed),
    [resolvedNameSeed, target]
  );
  const effectiveName = isAutoName ? generatedName : name;
  const normalizedThumbnailURL = useMemo(
    () => normalizeAbsoluteHTTPURL(thumbnailURL),
    [thumbnailURL]
  );
  const thumbnailError = useMemo(() => {
    if (thumbnailURL.trim() === "" || normalizedThumbnailURL !== "") {
      return "";
    }

    return "Thumbnail must be an absolute http:// or https:// URL.";
  }, [thumbnailURL, normalizedThumbnailURL]);
  const resolvedRelayUrls = useMemo(
    () => (isHero ? [currentOrigin] : relayUrls),
    [currentOrigin, isHero, relayUrls]
  );
  const includeDefaultRelays = isHero || defaultRelays;
  const resolvedThumbnailURL = isHero ? "" : normalizedThumbnailURL;
  const resolvedEnableUDP = isHero ? false : enableUDP;
  const resolvedUdpAddr = isHero ? "" : udpPort;
  const commandOptions = useMemo(
    () => ({
      currentOrigin,
      target,
      name: effectiveName,
      nameSeed,
      relayUrls: resolvedRelayUrls,
      defaultRelays: includeDefaultRelays,
      thumbnailURL: resolvedThumbnailURL,
      enableUDP: resolvedEnableUDP,
      udpAddr: resolvedUdpAddr,
      os,
    }),
    [
      currentOrigin,
      effectiveName,
      includeDefaultRelays,
      nameSeed,
      os,
      resolvedEnableUDP,
      resolvedRelayUrls,
      resolvedThumbnailURL,
      resolvedUdpAddr,
      target,
    ]
  );

  const copyCommand = useMemo(
    () => buildTunnelCommand(commandOptions),
    [commandOptions]
  );
  const previewURL = useMemo(
    () => buildTunnelPreviewURL(currentOrigin, effectiveName, target, nameSeed),
    [currentOrigin, effectiveName, nameSeed, target]
  );
  const statusHostname = useMemo(
    () =>
      buildTunnelStatusHostname(currentOrigin, effectiveName, target, nameSeed),
    [currentOrigin, effectiveName, nameSeed, target]
  );

  useEffect(() => {
    if (!copied) {
      return;
    }

    const timer = window.setTimeout(() => {
      setCopied(false);
    }, 2000);

    return () => {
      window.clearTimeout(timer);
    };
  }, [copied]);

  useEffect(() => {
    if (!isHero || statusHostname === "") {
      return;
    }

    let cancelled = false;
    let intervalId: number | undefined;

    const poll = async () => {
      try {
        const params = new URLSearchParams({ hostname: statusHostname });
        const statusResponse = await apiClient.get<TunnelStatusResponse>(
          `${API_PATHS.tunnel.status}?${params.toString()}`
        );
        if (cancelled) {
          return;
        }

        if (!statusResponse.registered) {
          setTunnelStatus("waiting");
          return;
        }

        setTunnelStatus(statusResponse.service_alive ? "alive" : "registered");
      } catch {
        if (!cancelled) {
          setTunnelStatus("waiting");
        }
      }
    };

    const startPolling = () => {
      stopPolling();
      void poll();
      intervalId = window.setInterval(() => {
        void poll();
      }, TUNNEL_STATUS_POLL_MS);
    };

    const stopPolling = () => {
      if (intervalId !== undefined) {
        window.clearInterval(intervalId);
        intervalId = undefined;
      }
    };

    const handleVisibilityChange = () => {
      if (document.visibilityState === "visible") {
        startPolling();
      } else {
        stopPolling();
      }
    };

    setTunnelStatus("waiting");
    startPolling();
    document.addEventListener("visibilitychange", handleVisibilityChange);

    return () => {
      cancelled = true;
      stopPolling();
      document.removeEventListener("visibilitychange", handleVisibilityChange);
    };
  }, [isHero, statusHostname]);

  const addRelayURL = (url: string) => {
    const trimmed = url.trim();
    if (!trimmed || relayUrls.includes(trimmed)) {
      return;
    }

    try {
      new URL(trimmed);
      setRelayUrls((prev) => [...prev, trimmed]);
      setUrlInput("");
    } catch {
      // Ignore invalid relay URL input.
    }
  };

  const removeRelayURL = (url: string) => {
    setRelayUrls((prev) => prev.filter((candidate) => candidate !== url));
  };

  const handleURLKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === "Enter") {
      event.preventDefault();
      addRelayURL(urlInput);
      return;
    }

    if (event.key === "Backspace" && urlInput === "" && relayUrls.length > 0) {
      setRelayUrls((prev) => prev.slice(0, -1));
    }
  };

  const handleCopy = async () => {
    try {
      await navigator.clipboard.writeText(copyCommand);
      setCopied(true);
    } catch (error) {
      console.error("Failed to copy tunnel command", error);
    }
  };

  const handleNameChange = (event: ChangeEvent<HTMLInputElement>) => {
    const next = event.target.value;
    if (next.trim() === "") {
      setName("");
      setIsAutoName(true);
      return;
    }

    setName(next);
    setIsAutoName(false);
  };

  const handleShuffleName = () => {
    const next =
      typeof window !== "undefined" &&
      typeof window.crypto?.randomUUID === "function"
        ? window.crypto.randomUUID()
        : `${Date.now().toString(36)}${Math.random().toString(36).slice(2)}`;

    setName("");
    setIsAutoName(true);
    setNameShuffleKey(next);
  };

  const nameInputValue = isAutoName ? generatedName : name;

  // ── Hero mode (terminal layout) ──────────────────────────────────────

  if (isHero) {
    const commandLines = copyCommand.split("\n");

    const tunnelStatusDot = {
      alive: "bg-green-status animate-pulse",
      registered: "bg-primary",
      waiting: "bg-text-muted",
    }[tunnelStatus];
    const tunnelStatusLabel = {
      alive: "Tunnelling Active",
      registered: "URL Reserved",
      waiting: "Waiting for connection",
    }[tunnelStatus];
    const isLive = tunnelStatus !== "waiting";

    return (
      <div className={className}>
        {/* Top bar: inputs + OS toggle */}
        <div className="flex flex-wrap items-center justify-between gap-4 border-b border-white/10 px-4 py-3">
          <div className="flex items-center gap-4">
            <div className="flex flex-col">
              <label
                htmlFor={`${inputId}-host`}
                className="text-[10px] font-bold uppercase tracking-widest text-primary"
              >
                Port
              </label>
              <input
                id={`${inputId}-host`}
                type="text"
                value={target}
                onChange={(e) => setTarget(e.target.value)}
                className="w-12 border-none bg-transparent p-0 font-mono text-sm focus:ring-0"
                style={{ color: "var(--hero-terminal-foreground)" }}
              />
            </div>
            <div className="h-8 w-px bg-white/10" />
            <div className="flex flex-col">
              <label
                htmlFor={`${inputId}-name`}
                className="text-[10px] font-bold uppercase tracking-widest text-primary"
              >
                Public Name
              </label>
              <div className="flex items-center gap-1">
                <input
                  id={`${inputId}-name`}
                  type="text"
                  value={nameInputValue}
                  onChange={handleNameChange}
                  className="w-36 border-none bg-transparent p-0 font-mono text-sm focus:ring-0"
                  style={{ color: "var(--hero-terminal-foreground)" }}
                />
                <button
                  type="button"
                  onClick={handleShuffleName}
                  className="rounded p-0.5 text-text-muted transition-colors hover:text-primary"
                  aria-label="Shuffle public name"
                  title="Shuffle public name"
                >
                  <RefreshCw className="h-3 w-3" aria-hidden="true" />
                </button>
              </div>
            </div>
          </div>

          {/* OS toggle */}
          <div className="flex gap-2">
            <button
              type="button"
              onClick={() => setOs("unix")}
              className={cn(
                "rounded-lg px-3 py-1.5 text-xs font-bold transition-colors",
                os === "unix"
                  ? "bg-secondary text-primary"
                  : "text-text-muted hover:bg-white/5"
              )}
            >
              Linux / macOS
            </button>
            <button
              type="button"
              onClick={() => setOs("windows")}
              className={cn(
                "rounded-lg px-3 py-1.5 text-xs font-bold transition-colors",
                os === "windows"
                  ? "bg-secondary text-primary"
                  : "text-text-muted hover:bg-white/5"
              )}
            >
              Windows (PowerShell)
            </button>
          </div>
        </div>

        {/* Command area */}
        <div className="relative min-h-[240px] p-6 font-mono text-sm leading-relaxed">
          {commandLines.map((line, i) => (
            <div key={i} className="mb-4 flex items-start gap-3">
              <span className="shrink-0 text-primary">~</span>
              <code className="text-text-muted">{line}</code>
            </div>
          ))}

          {/* Status card */}
          <div className="mt-4 rounded-xl border border-primary/20 bg-card/10 p-4">
            <div className="mb-2 flex items-center gap-3">
              <span
                className={cn("h-2 w-2 rounded-full", tunnelStatusDot)}
                aria-hidden="true"
              />
              <span className="text-xs font-bold uppercase tracking-wider text-green-status">
                {tunnelStatusLabel}
              </span>
            </div>
            <p style={{ color: "var(--hero-terminal-foreground)" }}>
              Public URL:{" "}
              {isLive ? (
                <a
                  href={previewURL}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="text-primary underline underline-offset-4 decoration-primary/30"
                >
                  {previewURL}
                </a>
              ) : (
                <span className="text-text-muted opacity-60">
                  {previewURL}
                </span>
              )}
            </p>
          </div>

          {/* Copy button */}
          <button
            type="button"
            onClick={handleCopy}
            className="absolute right-6 top-6 rounded-lg bg-secondary p-2 text-text-muted transition-colors hover:text-primary"
            aria-label="Copy command"
          >
            {copied ? (
              <Check className="h-4 w-4 text-green-status" />
            ) : (
              <Copy className="h-4 w-4" />
            )}
          </button>
        </div>
      </div>
    );
  }

  // ── Full mode (dialog/modal layout) ──────────────────────────────────

  const shuffleButtonClass = cn(
    "inline-flex shrink-0 items-center justify-center border px-3 text-xs font-semibold transition-colors h-12 rounded-lg",
    isTerminal
      ? "border-white/8 bg-black/20 text-slate-300 hover:text-white"
      : "border-border bg-white text-text-muted hover:text-foreground"
  );

  const commandSection = (
    <div className="space-y-2">
      <label
        className={cn(
          "text-sm font-medium",
          isTerminal ? "text-slate-200" : "text-foreground"
        )}
      >
        Generated Command
      </label>
      <div className="relative">
        <pre
          className={cn(
            "overflow-x-auto whitespace-pre-wrap break-all font-mono",
            isTerminal
              ? "rounded-xl border border-white/10 bg-black/30 p-4 pr-12 text-sm leading-7 text-white"
              : "rounded-xl bg-border p-4 pr-12 text-sm leading-7 text-foreground"
          )}
        >
          {copyCommand}
        </pre>
        <button
          type="button"
          onClick={handleCopy}
          className={cn(
            "absolute right-2 top-2 rounded-md p-2 transition-colors",
            isTerminal ? "hover:bg-white/10" : "hover:bg-background/70"
          )}
          aria-label="Copy command"
        >
          {copied ? (
            <Check className="h-4 w-4 text-green-600" />
          ) : (
            <Copy className="h-4 w-4 text-text-muted" />
          )}
        </button>
      </div>
    </div>
  );

  return (
    <div className={cn("space-y-5", className)}>
      <div className="space-y-2">
        <label
          htmlFor={`${inputId}-host`}
          className={cn(
            "text-sm font-medium",
            isTerminal ? "text-slate-200" : "text-foreground"
          )}
        >
          Host
        </label>
        <Input
          id={`${inputId}-host`}
          type="text"
          value={target}
          onChange={(event) => setTarget(event.target.value)}
          placeholder={defaultHost}
          className={cn(
            "h-12 rounded-xl",
            isTerminal
              ? "border-white/10 bg-white/5 text-white placeholder:text-slate-500"
              : "border-border bg-white"
          )}
        />
      </div>

      <div className="space-y-2">
        <label
          htmlFor={`${inputId}-name`}
          className={cn(
            "text-sm font-medium",
            isTerminal ? "text-slate-200" : "text-foreground"
          )}
        >
          Service Name
        </label>
        <div className="flex items-center gap-2">
          <Input
            id={`${inputId}-name`}
            type="text"
            value={nameInputValue}
            onChange={handleNameChange}
            className={cn(
              "h-12 flex-1 rounded-xl",
              isTerminal
                ? "border-white/10 bg-white/5 text-white placeholder:text-slate-500"
                : "border-border bg-white"
            )}
          />
          <button
            type="button"
            onClick={handleShuffleName}
            className={shuffleButtonClass}
            aria-label="Shuffle public name"
            title="Shuffle public name"
          >
            <RefreshCw className="h-4 w-4" aria-hidden="true" />
          </button>
        </div>
      </div>

      <div className="space-y-2">
        <p
          className={cn(
            "text-sm font-medium",
            isTerminal ? "text-slate-200" : "text-foreground"
          )}
        >
          Relay URLs
        </p>

        <div className="flex items-center justify-between gap-3">
          <label
            className={cn(
              "ml-auto flex items-center gap-2 text-xs",
              isTerminal ? "text-slate-400" : "text-text-muted"
            )}
          >
            <input
              type="checkbox"
              checked={defaultRelays}
              onChange={(event) => setDefaultRelays(event.target.checked)}
              className="h-4 w-4"
            />
            <span>Include default registry</span>
          </label>
        </div>

        <div
          className={cn(
            "flex min-h-12 flex-wrap items-center gap-2 rounded-xl px-2.5 py-2",
            isTerminal
              ? "border border-white/10 bg-white/5"
              : "border border-border bg-white"
          )}
        >
          {relayUrls.map((url) => (
            <span
              key={url}
              className={cn(
                "inline-flex items-center gap-1 rounded-md px-2.5 py-1.5 text-xs font-medium",
                isTerminal
                  ? "bg-white/10 text-slate-100"
                  : "bg-secondary text-secondary-foreground"
              )}
            >
              {url}
              <button
                type="button"
                onClick={() => removeRelayURL(url)}
                className={cn(
                  "ml-1 rounded-sm p-0.5",
                  isTerminal ? "hover:bg-white/10" : "hover:bg-destructive/15"
                )}
                aria-label={`Remove ${url}`}
              >
                <X className="h-3 w-3" />
              </button>
            </span>
          ))}

          <input
            type="text"
            value={urlInput}
            onChange={(event) => setUrlInput(event.target.value)}
            onKeyDown={handleURLKeyDown}
            placeholder="Add relay URL..."
            className={cn(
              "min-w-[140px] flex-1 bg-transparent text-sm outline-none",
              isTerminal
                ? "text-white placeholder:text-slate-500"
                : "text-foreground placeholder:text-muted-foreground"
            )}
          />
        </div>
      </div>

      <div className="space-y-2">
        <label
          htmlFor={`${inputId}-thumbnail`}
          className={cn(
            "text-sm font-medium",
            isTerminal ? "text-slate-200" : "text-foreground"
          )}
        >
          Thumbnail URL
        </label>
        <Input
          id={`${inputId}-thumbnail`}
          type="url"
          value={thumbnailURL}
          onChange={(event) => setThumbnailURL(event.target.value)}
          placeholder="https://cdn.example.com/thumb.png"
          className={cn(
            "h-12 rounded-xl",
            isTerminal
              ? "border-white/10 bg-white/5 text-white placeholder:text-slate-500"
              : "border-border bg-white"
          )}
        />
        {thumbnailError && (
          <p className="text-xs text-destructive">{thumbnailError}</p>
        )}
      </div>

      <div className="space-y-2">
        <p
          className={cn(
            "text-sm font-medium",
            isTerminal ? "text-slate-200" : "text-foreground"
          )}
        >
          UDP Transport
        </p>
        <label className="flex cursor-pointer items-center gap-2">
          <input
            type="checkbox"
            checked={enableUDP}
            onChange={(event) => {
              setEnableUDP(event.target.checked);
              if (!event.target.checked) {
                setUdpPort("");
              }
            }}
            className="h-4 w-4"
          />
          <span
            className={cn(
              "text-sm",
              isTerminal ? "text-slate-400" : "text-text-muted"
            )}
          >
            Enable UDP transport (for game servers, VoIP, etc.)
          </span>
        </label>
        {enableUDP && (
          <div className="space-y-1.5">
            <Input
              id={`${inputId}-udp-port`}
              type="text"
              value={udpPort}
              onChange={(event) => setUdpPort(event.target.value)}
              placeholder={target.trim() || "3000"}
              aria-label="UDP address"
              className={cn(
                "h-12 rounded-xl",
                isTerminal
                  ? "border-white/10 bg-white/5 text-white placeholder:text-slate-500"
                  : "border-border bg-white"
              )}
            />
            <p
              className={cn(
                "text-xs",
                isTerminal ? "text-slate-500" : "text-text-muted"
              )}
            >
              Local UDP address to forward. Defaults to the same as Host.
            </p>
          </div>
        )}
      </div>

      <div className="space-y-2">
        <div
          className={cn(
            "flex rounded-xl p-1",
            isTerminal ? "bg-white/8" : "bg-border"
          )}
        >
          <button
            type="button"
            onClick={() => setOs("unix")}
            className={cn(
              "flex-1 rounded-lg px-3 py-2 text-sm font-medium transition-colors",
              os === "unix"
                ? isTerminal
                  ? "bg-white text-slate-950 shadow-sm"
                  : "bg-background text-foreground shadow-sm"
                : isTerminal
                  ? "text-slate-400 hover:text-white"
                  : "text-text-muted hover:text-foreground"
            )}
          >
            Linux / macOS
          </button>
          <button
            type="button"
            onClick={() => setOs("windows")}
            className={cn(
              "flex-1 rounded-lg px-3 py-2 text-sm font-medium transition-colors",
              os === "windows"
                ? isTerminal
                  ? "bg-white text-slate-950 shadow-sm"
                  : "bg-background text-foreground shadow-sm"
                : isTerminal
                  ? "text-slate-400 hover:text-white"
                  : "text-text-muted hover:text-foreground"
            )}
          >
            Windows (PowerShell)
          </button>
        </div>
      </div>

      {commandSection}
    </div>
  );
}
