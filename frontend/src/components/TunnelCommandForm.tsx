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
      return "web_portal";
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
      return "web_portal";
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
  const commandOptions = useMemo(
    () => ({
      currentOrigin,
      target,
      name: effectiveName,
      nameSeed,
      relayUrls: resolvedRelayUrls,
      defaultRelays: includeDefaultRelays,
      thumbnailURL: resolvedThumbnailURL,
      os,
    }),
    [
      currentOrigin,
      effectiveName,
      includeDefaultRelays,
      nameSeed,
      os,
      resolvedRelayUrls,
      resolvedThumbnailURL,
      target,
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

  const commandSection = (
    <div className={cn("space-y-2", isHero && "space-y-3")}>
      {isHero ? (
        <label className={heroSectionLabelClass}>Command</label>
      ) : (
        <label
          className={cn(
            "text-sm font-medium",
            isTerminal ? "text-slate-200" : "text-foreground"
          )}
        >
          Generated Command
        </label>
      )}
      <div className="relative">
        <pre
          className={cn(
            "overflow-x-auto whitespace-pre-wrap break-all font-mono",
            isTerminal
              ? isHero
                ? "min-h-[104px] rounded-xl border border-green-status/20 bg-black/45 p-4 text-sm leading-7 text-white shadow-[inset_0_1px_0_rgba(255,255,255,0.05)]"
                : "rounded-xl border border-white/10 bg-black/30 p-4 pr-12 text-sm leading-7 text-white"
              : "rounded-xl bg-border p-4 pr-12 text-sm leading-7 text-foreground"
          )}
        >
          {displayCommand}
        </pre>
        {!isHero && (
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
        )}
      </div>
      {isHero && (
        <button
          type="button"
          onClick={handleCopy}
          className={cn(
            "inline-flex w-full items-center justify-center gap-2 rounded-xl px-4 py-2.5 text-sm font-semibold transition-colors",
            copied
              ? "bg-white text-slate-950"
              : "bg-green-status text-slate-950 hover:bg-green-300"
          )}
          aria-label="Copy command"
        >
          {copied ? <Check className="h-4 w-4" /> : <Copy className="h-4 w-4" />}
          <span>{copied ? "Copied" : "Copy command"}</span>
        </button>
      )}

      {isHero && (
        <div className="space-y-1.5 pt-1">
          <p className={heroSectionLabelClass}>Public URL</p>
          <div
            className={cn(
              "space-y-4 rounded-xl border px-4 py-3",
              isTerminal ? "border-white/10 bg-white/5" : "border-border bg-white"
            )}
          >
            <div
              className={cn(
                "flex items-center gap-2 text-sm font-semibold",
                isTerminal ? "text-slate-200" : "text-foreground"
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
                className={cn(
                  "block cursor-not-allowed overflow-x-auto whitespace-nowrap font-mono text-base font-medium opacity-60 sm:text-lg",
                  isTerminal ? "text-green-status" : "text-primary"
                )}
              >
                {previewURL}
              </span>
            ) : (
              <a
                href={previewURL}
                target="_blank"
                rel="noopener noreferrer"
                className={cn(
                  "block overflow-x-auto whitespace-nowrap font-mono text-base font-medium underline-offset-4 hover:underline sm:text-lg",
                  isTerminal ? "text-green-status" : "text-primary"
                )}
              >
                {previewURL}
              </a>
            )}
          </div>
        </div>
      )}
    </div>
  );

  const heroFieldLabelClass = cn(
    "text-[11px] font-semibold uppercase tracking-[0.2em]",
    isTerminal ? "text-slate-400" : "text-text-muted"
  );
  const heroInputClass = cn(
    "h-12 rounded-xl border px-4 text-[15px] shadow-none",
    isTerminal
      ? "border-white/8 bg-black/20 text-slate-100 placeholder:text-slate-500"
      : "border-border bg-white"
  );
  const nameInputValue = isAutoName ? generatedName : name;
  const shuffleButtonClass = cn(
    "inline-flex shrink-0 items-center justify-center border px-3 text-xs font-semibold transition-colors",
    isHero ? "h-12 rounded-xl px-4" : "h-12 rounded-lg",
    isTerminal
      ? "border-white/8 bg-black/20 text-slate-300 hover:text-white"
      : "border-border bg-white text-text-muted hover:text-foreground"
  );
  const optionSectionLabel = isHero ? (
    <div className="pt-1">
      <p className={heroSectionLabelClass}>OPTIONS</p>
    </div>
  ) : null;

  return (
    <div className={cn("space-y-5", className)}>
      {isHero && commandSection}
      {optionSectionLabel}

      {isHero ? (
        <div className="grid gap-4 sm:grid-cols-2">
          <div className="space-y-2">
            <label htmlFor={`${inputId}-host`} className={heroFieldLabelClass}>
              Host
            </label>
            <Input
              id={`${inputId}-host`}
              type="text"
              value={target}
              onChange={(event) => setTarget(event.target.value)}
              placeholder={defaultHost}
              className={heroInputClass}
            />
          </div>

          <div className="space-y-2">
            <label htmlFor={`${inputId}-name`} className={heroFieldLabelClass}>
              Public Name
            </label>
            <div className="flex items-center gap-2">
              <Input
                id={`${inputId}-name`}
                type="text"
                value={nameInputValue}
                onChange={handleNameChange}
                className={cn(heroInputClass, "flex-1")}
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
        </div>
      ) : (
        <>
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
        </>
      )}

      {!isHero && (
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
      )}

      {!isHero && (
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
      )}

      <div className={cn("space-y-2", isHero && "pt-1")}>
        <div
          className={cn(
            "flex p-1",
            isHero ? "rounded-lg" : "rounded-xl",
            isTerminal
              ? isHero
                ? "bg-black/20"
                : "bg-white/8"
              : "bg-border"
          )}
        >
          <button
            type="button"
            onClick={() => setOs("unix")}
            className={cn(
              "flex-1 rounded-lg px-3 transition-colors",
              isHero ? "py-2 text-sm font-semibold" : "py-2 text-sm font-medium",
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
              "flex-1 rounded-lg px-3 transition-colors",
              isHero ? "py-2 text-sm font-semibold" : "py-2 text-sm font-medium",
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

      {!isHero && commandSection}
    </div>
  );
}
