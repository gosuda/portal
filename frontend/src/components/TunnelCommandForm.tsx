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
  buildTunnelDisplayCommand,
  buildTunnelPreviewURL,
  buildTunnelStatusHostname,
  normalizeAbsoluteHTTPURL,
  type TunnelCommandOS,
} from "@/lib/tunnelCommand";

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

const DEFAULT_HOST = "3000";
const FALLBACK_ORIGIN = "https://localhost:4017";
const TUNNEL_NAME_SEED_STORAGE_KEY = "portal:tunnel-name-seed";

function readCurrentOrigin(): string {
  if (typeof window !== "undefined") {
    return window.location.origin;
  }

  return FALLBACK_ORIGIN;
}

function readTunnelNameSeed(): string {
  if (typeof window === "undefined") {
    return "web_portal";
  }

  try {
    const existing = window.localStorage.getItem(TUNNEL_NAME_SEED_STORAGE_KEY);
    if (existing && existing.trim() !== "") {
      return existing;
    }

    const next =
      typeof window.crypto?.randomUUID === "function"
        ? `web_${window.crypto.randomUUID()}`
        : `web_${Math.random().toString(36).slice(2)}${Date.now().toString(36)}`;

    window.localStorage.setItem(TUNNEL_NAME_SEED_STORAGE_KEY, next);
    return next;
  } catch {
    return "web_portal";
  }
}

function nextTunnelNameShuffleKey(): string {
  if (
    typeof window !== "undefined" &&
    typeof window.crypto?.randomUUID === "function"
  ) {
    return window.crypto.randomUUID();
  }

  return `${Date.now().toString(36)}${Math.random().toString(36).slice(2)}`;
}

export function TunnelCommandForm({
  className,
  theme = "light",
  mode = "full",
}: TunnelCommandFormProps) {
  if (mode === "hero") {
    return <HeroTunnelCommandForm className={className} theme={theme} />;
  }

  return <FullTunnelCommandForm className={className} theme={theme} />;
}

function HeroTunnelCommandForm({
  className,
  theme,
}: Required<Pick<TunnelCommandFormProps, "theme">> &
  Pick<TunnelCommandFormProps, "className">) {
  const inputId = useId();
  const isTerminal = theme === "terminal";
  const currentOrigin = useMemo(readCurrentOrigin, []);
  const nameSeed = useMemo(readTunnelNameSeed, []);

  const [target, setTarget] = useState(DEFAULT_HOST);
  const [name, setName] = useState("");
  const [isAutoName, setIsAutoName] = useState(true);
  const [nameShuffleKey, setNameShuffleKey] = useState("default");
  const [copied, setCopied] = useState(false);
  const [os, setOs] = useState<TunnelCommandOS>("unix");
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
  const commandOptions = useMemo(
    () => ({
      currentOrigin,
      target,
      name: effectiveName,
      nameSeed,
      relayUrls: [currentOrigin],
      defaultRelays: true,
      thumbnailURL: "",
      enableUDP: false,
      udpPort: "",
      os,
    }),
    [currentOrigin, effectiveName, nameSeed, os, target]
  );
  const copyCommand = useMemo(
    () => buildTunnelCommand(commandOptions),
    [commandOptions]
  );
  const displayCommand = useMemo(
    () => buildTunnelDisplayCommand(commandOptions),
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
    if (statusHostname === "") {
      return;
    }

    let cancelled = false;

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

    setTunnelStatus("waiting");
    void poll();
    const interval = window.setInterval(() => {
      void poll();
    }, 1500);

    return () => {
      cancelled = true;
      window.clearInterval(interval);
    };
  }, [statusHostname]);

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
    setName("");
    setIsAutoName(true);
    setNameShuffleKey(nextTunnelNameShuffleKey());
  };

  const tunnelStatusTone = {
    alive: isTerminal ? "bg-green-400" : "bg-green-600",
    registered: isTerminal ? "bg-sky-400" : "bg-sky-600",
    waiting: isTerminal ? "bg-slate-500" : "bg-slate-400",
  }[tunnelStatus];
  const tunnelStatusHeadline = {
    alive: "This URL is live now",
    registered: "URL reserved",
    waiting: "Waiting",
  }[tunnelStatus];
  const isPreviewURLDisabled = tunnelStatus === "waiting";
  const heroSectionLabelClass = cn(
    "text-xs font-semibold uppercase tracking-[0.24em]",
    isTerminal ? "text-slate-400" : "text-text-muted"
  );
  const heroURLClass = cn(
    "block overflow-x-auto whitespace-nowrap font-mono text-[15px] font-medium sm:text-base",
    isTerminal ? "text-sky-300" : "text-primary"
  );
  const heroInputClass = cn(
    "h-10 rounded-lg border px-3 text-sm shadow-none",
    isTerminal
      ? "border-white/6 bg-white/[0.035] text-slate-200 placeholder:text-slate-500"
      : "border-border bg-white"
  );
  const nameInputValue = isAutoName ? generatedName : name;
  const shuffleButtonClass = cn(
    "inline-flex h-10 w-10 shrink-0 items-center justify-center rounded-lg border px-0 text-xs font-semibold transition-colors",
    isTerminal
      ? "border-white/6 bg-white/[0.035] text-slate-400 hover:bg-white/5 hover:text-white"
      : "border-border bg-white text-text-muted hover:text-foreground"
  );

  return (
    <div className={cn("space-y-4", className)}>
      <div className="space-y-4">
        <label className={heroSectionLabelClass}>Command</label>
        <div className="relative">
          <pre
            className={cn(
              "min-h-[148px] overflow-x-auto whitespace-pre-wrap break-all rounded-xl border px-4 py-4 font-mono text-sm leading-7",
              isTerminal
                ? "border-primary/20 bg-black/50 text-white shadow-[inset_0_1px_0_rgba(255,255,255,0.05)]"
                : "bg-border text-foreground"
            )}
          >
            {displayCommand}
          </pre>
        </div>

        <button
          type="button"
          onClick={handleCopy}
          className={cn(
            "inline-flex w-full items-center justify-center gap-2 rounded-xl border px-4 py-2.5 text-sm font-semibold transition-all duration-200",
            copied
              ? "border-sky-400/45 bg-linear-to-r from-sky-400 via-cyan-300 to-blue-400 text-slate-950 shadow-[0_10px_24px_rgba(37,99,235,0.18)]"
              : "border-sky-400/45 bg-linear-to-r from-sky-500 via-cyan-400 to-blue-500 text-slate-950 shadow-[0_12px_30px_rgba(37,99,235,0.22)] hover:brightness-105 hover:saturate-125"
          )}
          aria-label="Copy command"
        >
          {copied ? <Check className="h-4 w-4" /> : <Copy className="h-4 w-4" />}
          <span>{copied ? "Copied" : "Copy command"}</span>
        </button>

        <div className="space-y-1.5 pt-0.5">
          <p className={heroSectionLabelClass}>Public URL</p>
          <div
            className={cn(
              "space-y-3 rounded-xl border px-3.5 py-3",
              isTerminal
                ? "border-white/8 bg-white/[0.045]"
                : "border-border bg-white"
            )}
          >
            <div
              className={cn(
                "flex items-center gap-2 text-[13px] font-semibold",
                isTerminal ? "text-slate-300" : "text-foreground"
              )}
            >
              <span
                className={cn("h-2 w-2 rounded-full", tunnelStatusTone)}
                aria-hidden="true"
              />
              <span>{tunnelStatusHeadline}</span>
            </div>
            {isPreviewURLDisabled ? (
              <span
                aria-disabled="true"
                className={cn(heroURLClass, "cursor-not-allowed opacity-70")}
              >
                {previewURL}
              </span>
            ) : (
              <a
                href={previewURL}
                target="_blank"
                rel="noopener noreferrer"
                className={cn(heroURLClass, "underline-offset-4 hover:underline")}
              >
                {previewURL}
              </a>
            )}
          </div>
        </div>
      </div>

      <div
        className={cn(
          "space-y-1.5 border-t pt-3",
          isTerminal ? "border-white/6" : "border-border/80"
        )}
      >
        <div
          className={cn(
            "grid grid-cols-[62px_minmax(0,1fr)_140px] gap-2 px-1 text-[9px] font-semibold uppercase tracking-[0.16em]",
            isTerminal ? "text-slate-500" : "text-text-muted"
          )}
        >
          <span>Port</span>
          <span>Name</span>
          <span>Platform</span>
        </div>
        <div className="grid grid-cols-[62px_minmax(0,1fr)_140px] items-center gap-2">
          <Input
            id={`${inputId}-host`}
            type="text"
            value={target}
            onChange={(event) => setTarget(event.target.value)}
            placeholder={DEFAULT_HOST}
            aria-label="Port"
            className={cn(heroInputClass, "w-full px-2.5 text-[13px] font-mono")}
          />

          <div className="flex min-w-0 flex-1 items-center gap-2">
            <Input
              id={`${inputId}-name`}
              type="text"
              value={nameInputValue}
              onChange={handleNameChange}
              aria-label="Public name"
              className={cn(heroInputClass, "min-w-0 flex-1 px-2.5 text-[13px]")}
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

          <div
            className={cn(
              "flex w-full shrink-0 rounded-lg border p-0.5",
              isTerminal
                ? "border-white/6 bg-white/[0.035]"
                : "border-border bg-border"
            )}
          >
            <div className="flex w-full">
              <button
                type="button"
                onClick={() => setOs("unix")}
                className={cn(
                  "flex-1 whitespace-nowrap rounded-md px-1.5 py-1.5 text-[11px] font-semibold transition-colors",
                  os === "unix"
                    ? isTerminal
                      ? "bg-white text-slate-950 shadow-sm"
                      : "bg-background text-foreground shadow-sm"
                    : isTerminal
                      ? "text-slate-500 hover:text-white"
                      : "text-text-muted hover:text-foreground"
                )}
              >
                Linux
              </button>
              <button
                type="button"
                onClick={() => setOs("windows")}
                className={cn(
                  "flex-1 whitespace-nowrap rounded-md px-1.5 py-1.5 text-[11px] font-semibold transition-colors",
                  os === "windows"
                    ? isTerminal
                      ? "bg-white text-slate-950 shadow-sm"
                      : "bg-background text-foreground shadow-sm"
                    : isTerminal
                      ? "text-slate-500 hover:text-white"
                      : "text-text-muted hover:text-foreground"
                )}
              >
                Windows
              </button>
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}

function FullTunnelCommandForm({
  className,
  theme,
}: Required<Pick<TunnelCommandFormProps, "theme">> &
  Pick<TunnelCommandFormProps, "className">) {
  const inputId = useId();
  const isTerminal = theme === "terminal";
  const currentOrigin = useMemo(readCurrentOrigin, []);
  const nameSeed = useMemo(readTunnelNameSeed, []);

  const [target, setTarget] = useState(DEFAULT_HOST);
  const [name, setName] = useState("");
  const [isAutoName, setIsAutoName] = useState(true);
  const [nameShuffleKey, setNameShuffleKey] = useState("default");
  const [relayUrls, setRelayUrls] = useState<string[]>([currentOrigin]);
  const [defaultRelays, setDefaultRelays] = useState(true);
  const [urlInput, setUrlInput] = useState("");
  const [copied, setCopied] = useState(false);
  const [os, setOs] = useState<TunnelCommandOS>("unix");
  const [enableUDP, setEnableUDP] = useState(false);
  const [udpPort, setUDPPort] = useState("");
  const [thumbnailURL, setThumbnailURL] = useState("");

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
  }, [normalizedThumbnailURL, thumbnailURL]);
  const commandOptions = useMemo(
    () => ({
      currentOrigin,
      target,
      name: effectiveName,
      nameSeed,
      relayUrls,
      defaultRelays,
      thumbnailURL: normalizedThumbnailURL,
      enableUDP,
      udpPort,
      os,
    }),
    [
      currentOrigin,
      defaultRelays,
      effectiveName,
      enableUDP,
      nameSeed,
      normalizedThumbnailURL,
      os,
      relayUrls,
      target,
      udpPort,
    ]
  );
  const copyCommand = useMemo(
    () => buildTunnelCommand(commandOptions),
    [commandOptions]
  );
  const displayCommand = useMemo(
    () => buildTunnelDisplayCommand(commandOptions),
    [commandOptions]
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
    setName("");
    setIsAutoName(true);
    setNameShuffleKey(nextTunnelNameShuffleKey());
  };

  const nameInputValue = isAutoName ? generatedName : name;
  const shuffleButtonClass = cn(
    "inline-flex h-12 shrink-0 items-center justify-center rounded-lg border px-3 text-xs font-semibold transition-colors",
    isTerminal
      ? "border-white/10 bg-white/5 text-slate-400 hover:bg-white/10 hover:text-white"
      : "border-border bg-white text-text-muted hover:text-foreground"
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
          placeholder={DEFAULT_HOST}
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
        <label
          className={cn(
            "text-sm font-medium",
            isTerminal ? "text-slate-200" : "text-foreground"
          )}
        >
          Relay URLs
        </label>

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
          className={cn(
            "text-sm font-medium",
            isTerminal ? "text-slate-200" : "text-foreground"
          )}
        >
          UDP Transport
        </label>
        <label
          className={cn(
            "flex items-center gap-2 text-sm",
            isTerminal ? "text-slate-300" : "text-muted-foreground"
          )}
        >
          <input
            type="checkbox"
            checked={enableUDP}
            onChange={(event) => {
              const nextEnabled = event.target.checked;
              setEnableUDP(nextEnabled);
              if (!nextEnabled) {
                setUDPPort("");
              }
            }}
            className="h-4 w-4"
          />
          <span>Enable UDP transport</span>
        </label>

        {enableUDP && (
          <div className="space-y-1.5">
            <Input
              id={`${inputId}-udp-port`}
              type="text"
              value={udpPort}
              onChange={(event) => setUDPPort(event.target.value)}
              placeholder={target.trim() || DEFAULT_HOST}
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
                isTerminal ? "text-slate-400" : "text-muted-foreground"
              )}
            >
              Local UDP port to forward. Defaults to the same as Host.
            </p>
          </div>
        )}
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
        {normalizedThumbnailURL && (
          <div
            className={cn(
              "flex h-20 w-20 items-center justify-center overflow-hidden rounded-md border",
              isTerminal
                ? "border-white/10 bg-white/5"
                : "border-border bg-background"
            )}
          >
            <img
              src={normalizedThumbnailURL}
              alt="Thumbnail preview"
              className="h-full w-full object-cover"
            />
          </div>
        )}
        {thumbnailError && <p className="text-xs text-destructive">{thumbnailError}</p>}
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
              "overflow-x-auto whitespace-pre-wrap break-all rounded-xl p-4 pr-12 font-mono text-sm leading-7",
              isTerminal
                ? "border border-white/10 bg-black/30 text-white"
                : "bg-border text-foreground"
            )}
          >
            {displayCommand}
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
    </div>
  );
}
