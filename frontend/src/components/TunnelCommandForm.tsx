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
  normalizeTunnelCommandName,
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

function splitDisplayCommand(command: string, os: TunnelCommandOS) {
  const lines = command.split("\n");
  const installLineCount = os === "windows" ? 2 : 1;

  return {
    installBlock: lines.slice(0, installLineCount).join("\n"),
    runBlock: lines.slice(installLineCount).join("\n"),
  };
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
  const isTerminal = theme === "terminal";
  const currentOrigin = useMemo(readCurrentOrigin, []);
  const nameSeed = useMemo(readTunnelNameSeed, []);

  const [target, setTarget] = useState(DEFAULT_HOST);
  const [name, setName] = useState("");
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
  const normalizedName = useMemo(
    () => normalizeTunnelCommandName(name),
    [name]
  );
  const effectiveName = normalizedName === "" ? generatedName : normalizedName;
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
  const { installBlock, runBlock } = useMemo(
    () => splitDisplayCommand(displayCommand, os),
    [displayCommand, os]
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
    setName(event.target.value);
  };

  const handleShuffleName = () => {
    setName("");
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
    waiting: "Waiting for Connection",
  }[tunnelStatus];
  const isPreviewURLDisabled = tunnelStatus === "waiting";
  const heroSectionLabelClass = cn(
    "text-[13px] font-semibold tracking-[0.04em] sm:text-sm",
    isTerminal ? "text-slate-100" : "text-foreground/85"
  );
  const heroCommandTitleClass = cn(
    "min-w-0 text-[15px] font-bold tracking-tight sm:text-base",
    isTerminal ? "text-slate-50" : "text-foreground"
  );
  const heroURLClass = cn(
    "block overflow-x-auto whitespace-nowrap font-mono text-[15px] font-medium sm:text-base",
    isTerminal ? "text-sky-300" : "text-primary"
  );
  const platformButtonGroupClass = cn(
    "flex shrink-0 rounded-lg border p-0.5",
    isTerminal
      ? "border-white/6 bg-white/[0.035]"
      : "border-border bg-border"
  );
  const platformButtonClass = (selected: boolean) =>
    cn(
      "min-w-[72px] whitespace-nowrap rounded-md px-2.5 py-1.5 text-[11px] font-semibold transition-colors",
      selected
        ? isTerminal
          ? "bg-white/[0.08] text-slate-200"
          : "bg-background text-foreground/85"
        : isTerminal
          ? "text-slate-500 hover:text-slate-300"
          : "text-text-muted hover:text-foreground"
    );
  const heroControlLabelClass = cn(
    "shrink-0 text-[9px] font-semibold uppercase tracking-[0.16em]",
    isTerminal ? "text-slate-500" : "text-text-muted"
  );
  const heroControlInputClass = cn(
    "h-auto border-0 bg-transparent px-0 py-0 text-[13px] shadow-none focus-visible:ring-0",
    isTerminal
      ? "text-slate-200 placeholder:text-slate-600"
      : "text-foreground/85 placeholder:text-muted-foreground"
  );
  const heroShuffleButtonClass = cn(
    "inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md transition-colors",
    isTerminal
      ? "text-slate-500 hover:bg-white/[0.06] hover:text-slate-200"
      : "text-text-muted hover:bg-foreground/5 hover:text-foreground"
  );
  return (
    <div className={cn("space-y-5", className)}>
      <div className="space-y-2">
        <div className="space-y-1.5">
          <p className={heroSectionLabelClass}>
            1. Start your local app
            <span
              className={cn(
                "ml-1 normal-case tracking-normal",
                isTerminal ? "text-slate-400" : "text-text-muted"
              )}
            >
              (e.g.
              <span
                className={cn(
                  "mx-1 font-mono",
                  isTerminal ? "text-slate-200" : "text-foreground"
                )}
              >
                localhost:3000
              </span>
              )
            </span>
          </p>
        </div>
      </div>

      <div className="space-y-3">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex min-w-0 items-center gap-3">
            <span
              aria-hidden="true"
              className={cn(
                "shrink-0 font-mono text-lg leading-none",
                !isTerminal && "text-primary"
              )}
              style={isTerminal ? { color: "var(--hero-terminal-accent)" } : undefined}
            >
              {">"}
            </span>
            <h3 id="tunnel-preview" className={heroCommandTitleClass}>
              2. Run this command
            </h3>
          </div>
          <div className={platformButtonGroupClass}>
            <button
              type="button"
              onClick={() => setOs("unix")}
              className={platformButtonClass(os === "unix")}
            >
              Linux
            </button>
            <button
              type="button"
              onClick={() => setOs("windows")}
              className={platformButtonClass(os === "windows")}
            >
              Windows
            </button>
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-x-4 gap-y-2 sm:flex-nowrap">
          <div className="flex shrink-0 items-center gap-2">
            <span className={heroControlLabelClass}>Port</span>
            <Input
              type="text"
              value={target}
              onChange={(event) => setTarget(event.target.value)}
              placeholder={DEFAULT_HOST}
              aria-label="Local port or address"
              className={cn(heroControlInputClass, "w-[4.75rem] font-mono")}
            />
          </div>
          <div className="ml-auto flex min-w-0 items-center justify-end gap-2 sm:w-[22rem]">
            <span className={heroControlLabelClass}>Name</span>
            <Input
              type="text"
              value={name}
              onChange={handleNameChange}
              placeholder={generatedName}
              aria-label="Public name"
              className={cn(heroControlInputClass, "min-w-0 flex-1")}
            />
            <button
              type="button"
              onClick={handleShuffleName}
              className={heroShuffleButtonClass}
              aria-label="Shuffle public name"
              title="Shuffle public name"
            >
              <RefreshCw className="h-4 w-4" aria-hidden="true" />
            </button>
          </div>
        </div>
        <div
          className={cn(
            "relative min-h-[148px] rounded-xl border px-4 py-4 pr-14 font-mono text-sm leading-7",
            isTerminal
              ? "border-white/10 bg-black/55 text-white shadow-[inset_0_1px_0_rgba(255,255,255,0.05)]"
              : "border-border/80 bg-border/90 text-foreground"
          )}
        >
          <button
            type="button"
            onClick={handleCopy}
            className={cn(
              "absolute top-4 right-4 inline-flex h-8 w-8 items-center justify-center rounded-lg transition-colors",
              isTerminal
                ? "text-emerald-300/75 hover:bg-emerald-400/10 hover:text-emerald-200"
                : "text-emerald-600 hover:bg-emerald-500/10 hover:text-emerald-700"
            )}
            aria-label="Copy command"
            title={copied ? "Copied" : "Copy"}
          >
            {copied ? (
              <Check className="h-4 w-4" />
            ) : (
              <Copy className="h-4 w-4" />
            )}
          </button>
          <pre className="overflow-x-auto whitespace-pre-wrap break-all">
            <span className="block">{installBlock}</span>
            <span className="mt-2 block">{runBlock}</span>
          </pre>
        </div>
      </div>

      <div className="space-y-2 pt-1">
        <p className={heroSectionLabelClass}>3. Open this public URL</p>
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
  const normalizedName = useMemo(
    () => normalizeTunnelCommandName(name),
    [name]
  );
  const effectiveName = normalizedName === "" ? generatedName : normalizedName;
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
  const { installBlock, runBlock } = useMemo(
    () => splitDisplayCommand(displayCommand, os),
    [displayCommand, os]
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
    setName(event.target.value);
  };

  const handleShuffleName = () => {
    setName("");
    setNameShuffleKey(nextTunnelNameShuffleKey());
  };

  const shuffleButtonClass = cn(
    "inline-flex h-12 shrink-0 items-center justify-center rounded-lg border px-3 text-xs font-semibold transition-colors",
    isTerminal
      ? "border-white/10 bg-white/5 text-slate-400 hover:bg-white/10 hover:text-white"
      : "border-border bg-white text-text-muted hover:text-foreground"
  );

  return (
    <div className={cn("space-y-5", className)}>
      <div className="space-y-2">
        <p
          className={cn(
            "text-sm leading-6",
            isTerminal ? "text-slate-300" : "text-text-muted"
          )}
        >
          Start your local app, then point Portal at it with a port like
          <span className={cn("mx-1 font-mono", isTerminal ? "text-white" : "text-foreground")}>
            3000
          </span>
          or an address like
          <span className={cn("mx-1 font-mono", isTerminal ? "text-white" : "text-foreground")}>
            localhost:3000
          </span>
          .
        </p>
        <label
          htmlFor={`${inputId}-host`}
          className={cn(
            "text-sm font-medium",
            isTerminal ? "text-slate-200" : "text-foreground"
          )}
        >
          Local App
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
        <p
          className={cn(
            "text-xs",
            isTerminal ? "text-slate-400" : "text-muted-foreground"
          )}
        >
          Use a local port or address that is already running.
        </p>
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
            value={name}
            onChange={handleNameChange}
            placeholder={generatedName}
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
                  ? "bg-white/[0.08] text-slate-200"
                  : "bg-background text-foreground/85"
                : isTerminal
                  ? "text-slate-400 hover:text-slate-300"
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
                  ? "bg-white/[0.08] text-slate-200"
                  : "bg-background text-foreground/85"
                : isTerminal
                  ? "text-slate-400 hover:text-slate-300"
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
            <span className="block">{installBlock}</span>
            <span className="mt-2 block">{runBlock}</span>
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
