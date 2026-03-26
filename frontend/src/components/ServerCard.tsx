import { useMemo, useState } from "react";
import { Link } from "react-router-dom";
import clsx from "clsx";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";

interface ServerCardProps {
  serverId: number;
  name: string;
  description: string;
  tags: string[];
  thumbnail: string;
  owner: string;
  online: boolean;
  firstSeen?: string;
  dns: string;
  navigationPath: string;
  navigationState: any;
  isFavorite?: boolean;
  onToggleFavorite?: (serverId: number) => void;
  showAdminControls?: boolean;
  leaseId?: string;
  isBanned?: boolean;
  isApproved?: boolean;
  isDenied?: boolean;
  bps?: number;
  ip?: string;
  displayIP?: string;
  isIPBanned?: boolean;
  onBanStatusChange?: (
    leaseId: string,
    isBan: boolean
  ) => void | Promise<void>;
  onBPSChange?: (leaseId: string, bps: number) => void | Promise<void>;
  onApproveStatusChange?: (
    leaseId: string,
    approve: boolean
  ) => void | Promise<void>;
  onDenyStatusChange?: (leaseId: string, deny: boolean) => void | Promise<void>;
  onIPBanStatusChange?: (ip: string, isBan: boolean) => void | Promise<void>;
  transport?: string;
  isSelected?: boolean;
  onToggleSelect?: (leaseId: string) => void;
}

export function ServerCard({
  serverId,
  name,
  description,
  tags,
  thumbnail,
  owner,
  online,
  firstSeen,
  dns,
  navigationPath,
  navigationState,
  isFavorite = false,
  onToggleFavorite,
  showAdminControls = false,
  leaseId,
  isBanned = false,
  isApproved = false,
  isDenied = false,
  bps = 0,
  ip = "",
  displayIP,
  isIPBanned = false,
  onBanStatusChange,
  onBPSChange,
  onApproveStatusChange,
  onDenyStatusChange,
  onIPBanStatusChange,
  transport = "tcp",
  isSelected = false,
  onToggleSelect,
}: ServerCardProps) {
  const [showBPSModal, setShowBPSModal] = useState(false);
  const [bpsInput, setBpsInput] = useState(bps.toString());

  const bpsSteps = [0, 10, 100, 1000, 10000, 100000, 1000000, 10000000];

  const bpsToSliderIndex = (value: number): number => {
    if (value === 0) return 0;
    const idx = bpsSteps.findIndex((step) => step >= value);
    return idx === -1 ? bpsSteps.length - 1 : idx;
  };

  const [sliderIndex, setSliderIndex] = useState(bpsToSliderIndex(bps));

  const runAsyncAdminAction = (action?: () => void | Promise<void>) => {
    if (!action) {
      return;
    }

    try {
      const result = action();
      if (result && typeof (result as Promise<void>).then === "function") {
        void result.catch((error) => {
          console.error("Failed admin action", error);
        });
      }
    } catch (error) {
      console.error("Failed admin action", error);
    }
  };

  const handleSliderChange = (idx: number) => {
    setSliderIndex(idx);
    setBpsInput(bpsSteps[idx].toString());
  };

  const syncSliderFromInput = (value: number) => {
    const idx = bpsToSliderIndex(value);
    setSliderIndex(idx);
  };

  const handleFavoriteClick = (event: React.MouseEvent) => {
    event.preventDefault();
    event.stopPropagation();
    onToggleFavorite?.(serverId);
  };

  const handleSelectClick = (event: React.MouseEvent) => {
    event.preventDefault();
    event.stopPropagation();
    if (leaseId && onToggleSelect) {
      onToggleSelect(leaseId);
    }
  };

  const handleBanClick = (event: React.MouseEvent) => {
    event.preventDefault();
    event.stopPropagation();
    if (leaseId) {
      runAsyncAdminAction(() => onBanStatusChange?.(leaseId, !isBanned));
    }
  };

  const handleApproveClick = (event: React.MouseEvent) => {
    event.preventDefault();
    event.stopPropagation();
    if (leaseId) {
      runAsyncAdminAction(() => onApproveStatusChange?.(leaseId, !isApproved));
    }
  };

  const handleDenyClick = (event: React.MouseEvent) => {
    event.preventDefault();
    event.stopPropagation();
    if (leaseId) {
      runAsyncAdminAction(() => onDenyStatusChange?.(leaseId, !isDenied));
    }
  };

  const handleIPBanClick = (event: React.MouseEvent) => {
    event.preventDefault();
    event.stopPropagation();
    if (ip) {
      runAsyncAdminAction(() => onIPBanStatusChange?.(ip, !isIPBanned));
    }
  };

  const handleBPSSettingsClick = (event: React.MouseEvent) => {
    event.preventDefault();
    event.stopPropagation();
    setSliderIndex(bpsToSliderIndex(bps));
    setBpsInput(bps.toString());
    setShowBPSModal(true);
  };

  const handleBPSSave = () => {
    if (leaseId) {
      const newBps = parseInt(bpsInput, 10) || 0;
      runAsyncAdminAction(() => onBPSChange?.(leaseId, newBps));
    }
    setShowBPSModal(false);
  };

  const formatSliderLabel = (value: number): string => {
    if (value === 0) return "Unlimited";
    if (value >= 1000000) return `${value / 1000000} MB/s`;
    if (value >= 1000) return `${value / 1000} KB/s`;
    return `${value} B/s`;
  };

  const formatStepLabel = (value: number): string => {
    if (value === 0) return "unl";
    if (value >= 1000000) return `${value / 1000000}M`;
    if (value >= 1000) return `${value / 1000}K`;
    return value.toString();
  };

  const formatBPS = (value: number): string => {
    if (value === 0) return "Unlimited";
    if (value >= 1_000_000_000)
      return `${(value / 1_000_000_000).toFixed(1)} GB/s`;
    if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)} MB/s`;
    if (value >= 1_000) return `${(value / 1_000).toFixed(1)} KB/s`;
    return `${value} B/s`;
  };

  const formattedDuration = useMemo(() => {
    if (!firstSeen) return "";
    const start = new Date(firstSeen).getTime();
    const now = Date.now();
    const diff = Math.max(0, now - start);

    const seconds = Math.floor(diff / 1000);
    const minutes = Math.floor(seconds / 60);
    const hours = Math.floor(minutes / 60);
    const days = Math.floor(hours / 24);

    if (days > 0) return `${days}d ${hours % 24}h`;
    if (hours > 0) return `${hours}h ${minutes % 60}m`;
    if (minutes > 0) return `${minutes}m`;
    return `${seconds}s`;
  }, [firstSeen]);

  const cardBody = (
    <article
      data-hero-key={`server-bg-${serverId}`}
      className={clsx(
        "group flex h-full flex-col overflow-hidden rounded-xl border border-border bg-card shadow-[0_18px_42px_oklch(0%_0_0/0.06)] transition-transform duration-200 hover:-translate-y-1 hover:shadow-[0_24px_56px_oklch(0%_0_0/0.08)]",
        showAdminControls ? "min-h-90" : "min-h-77.5"
      )}
    >
      <div
        className="relative h-40 overflow-hidden border-b border-border bg-secondary"
        style={{
          backgroundImage: thumbnail
            ? `linear-gradient(rgba(255,255,255,0.08), rgba(255,255,255,0.08)), url(${thumbnail})`
            : "linear-gradient(135deg, var(--card-media-fallback-start) 0%, var(--card-media-fallback-end) 100%)",
          backgroundSize: "cover",
          backgroundPosition: "center",
        }}
      >
        {!thumbnail && (
          <div
            className="absolute inset-0"
            style={{ background: "var(--card-media-overlay)" }}
          />
        )}

        <div className="absolute inset-x-0 top-0 flex items-start justify-between p-4">
          <div className="inline-flex items-center gap-2 rounded-full border border-border bg-card/95 px-3 py-1 text-[11px] font-semibold uppercase tracking-[0.18em] text-foreground shadow-[0_8px_18px_oklch(0%_0_0/0.05)]">
            <span
              className={clsx(
                "h-2.5 w-2.5 rounded-full",
                online ? "bg-green-status" : "bg-muted-foreground"
              )}
            />
            <span>
              {online ? "Online" : "Offline"}
              {formattedDuration && online && ` · ${formattedDuration}`}
            </span>
          </div>
          {showAdminControls && transport !== "tcp" && (
            <span className="rounded-full bg-black/40 px-2.5 py-1 text-[10px] font-bold uppercase tracking-wider text-primary backdrop-blur-sm border border-primary/30">
              {transport}
            </span>
          )}

          {showAdminControls ? (
            <button
              onClick={handleSelectClick}
              className={clsx(
                "flex size-9 items-center justify-center rounded-full border border-border bg-card/95 text-text-muted shadow-[0_8px_18px_oklch(0%_0_0/0.05)] transition-colors cursor-pointer",
                isSelected
                  ? "bg-primary text-primary-foreground"
                  : "hover:text-foreground"
              )}
              aria-label={isSelected ? "Deselect" : "Select"}
            >
              <svg
                xmlns="http://www.w3.org/2000/svg"
                viewBox="0 0 24 24"
                className="h-4.5 w-4.5"
                fill="none"
                stroke="currentColor"
                strokeWidth="3"
                strokeLinecap="round"
                strokeLinejoin="round"
              >
                {isSelected && <polyline points="20 6 9 17 4 12" />}
              </svg>
            </button>
          ) : (
            <button
              onClick={handleFavoriteClick}
              className={clsx(
                "flex size-9 items-center justify-center rounded-full border border-border bg-card/95 text-text-muted shadow-[0_8px_18px_oklch(0%_0_0/0.05)] transition-colors cursor-pointer",
                isFavorite
                  ? "bg-primary text-primary-foreground"
                  : "hover:text-foreground"
              )}
              aria-label={
                isFavorite ? "Remove from favorites" : "Add to favorites"
              }
            >
              <svg
                xmlns="http://www.w3.org/2000/svg"
                viewBox="0 0 24 24"
                className="h-4.5 w-4.5"
                fill={isFavorite ? "currentColor" : "none"}
                stroke="currentColor"
                strokeWidth="2"
                strokeLinecap="round"
                strokeLinejoin="round"
              >
                <polygon points="12 2 15.09 8.26 22 9.27 17 14.14 18.18 21.02 12 17.77 5.82 21.02 7 14.14 2 9.27 8.91 8.26 12 2" />
              </svg>
            </button>
          )}
        </div>
      </div>

      <div className="flex flex-1 flex-col justify-between gap-4 p-5">
        <div className="space-y-4">
          <div className="space-y-2">
            <h3 className="truncate text-2xl font-bold tracking-tight text-foreground">
              {name}
            </h3>
            {description && (
              <p className="line-clamp-2 text-sm leading-6 text-text-muted">
                {description}
              </p>
            )}
          </div>

          {tags.length > 0 && (
            <div className="w-full overflow-x-auto">
              <div className="flex min-w-max gap-2 pb-1">
                {tags.map((tag, index) => (
                  <span
                    key={index}
                    className="whitespace-nowrap rounded-full border border-border bg-secondary px-3 py-1 text-xs font-medium text-text-muted"
                  >
                    #{tag}
                  </span>
                ))}
              </div>
            </div>
          )}

          <div className="flex flex-wrap items-center gap-3 text-sm text-text-muted">
            {owner && <span>by {owner}</span>}
            {dns && (
              <span className="rounded-full bg-secondary px-3 py-1 font-mono text-[11px] text-secondary-foreground">
                {dns}
              </span>
            )}
          </div>
        </div>

        {showAdminControls && leaseId && (
          <div className="flex flex-col gap-3 rounded-3xl border border-border bg-secondary/70 p-4">
            {onBPSChange && (
              <div className="flex items-center justify-between gap-4">
                <span className="text-xs text-text-muted">
                  BPS: <span className="font-medium text-foreground">{formatBPS(bps)}</span>
                </span>
                <button
                  onClick={handleBPSSettingsClick}
                  className="rounded-full border border-border bg-card px-3 py-1 text-[11px] font-semibold text-foreground transition-colors hover:bg-secondary cursor-pointer"
                >
                  Settings
                </button>
              </div>
            )}

            {isApproved && ip && (
              <div className="text-[11px] text-text-muted">
                IP: <span className="font-mono text-foreground">{displayIP || ip}</span>
                {isIPBanned && (
                  <span className="ml-2 font-medium text-destructive">
                    (Banned)
                  </span>
                )}
              </div>
            )}

            {!isApproved && !isDenied ? (
              <div className="flex gap-2">
                <button
                  onClick={handleApproveClick}
                  className="flex-1 rounded-xl bg-primary px-4 py-2 text-xs font-semibold text-primary-foreground transition-opacity hover:opacity-90 cursor-pointer"
                >
                  Approve
                </button>
                <button
                  onClick={handleDenyClick}
                  className="flex-1 rounded-xl bg-destructive px-4 py-2 text-xs font-semibold text-destructive-foreground transition-opacity hover:opacity-90 cursor-pointer"
                >
                  Deny
                </button>
              </div>
            ) : (
              <button
                onClick={ip ? handleIPBanClick : handleBanClick}
                className={clsx(
                  "w-full rounded-xl px-4 py-2 text-xs font-semibold transition-opacity hover:opacity-90 cursor-pointer",
                  (ip ? isIPBanned : isBanned)
                    ? "bg-green-status text-white"
                    : "bg-destructive text-destructive-foreground"
                )}
              >
                {ip
                  ? isIPBanned
                    ? "Unban IP"
                    : "Ban IP"
                  : isBanned
                    ? "Unban"
                    : "Ban"}
              </button>
            )}
          </div>
        )}
      </div>
    </article>
  );

  return (
    <>
      {showAdminControls ? (
        <div className="relative">{cardBody}</div>
      ) : (
        <Link
          to={navigationPath}
          state={navigationState}
          className="relative block h-full cursor-pointer"
        >
          {cardBody}
        </Link>
      )}

      <Dialog open={showBPSModal} onOpenChange={setShowBPSModal}>
        <DialogContent className="max-w-sm rounded-xl">
          <DialogHeader>
            <DialogTitle>BPS Settings</DialogTitle>
            <DialogDescription>
              Set bytes-per-second limit (0 = unlimited)
            </DialogDescription>
          </DialogHeader>
          <div className="text-center text-xl font-bold text-primary">
            {formatSliderLabel(parseInt(bpsInput, 10) || 0)}
          </div>
          <input
            type="range"
            min="0"
            max={bpsSteps.length - 1}
            value={sliderIndex}
            onChange={(event) => {
              const idx = parseInt(event.target.value, 10);
              handleSliderChange(idx);
            }}
            className="h-2 w-full cursor-pointer appearance-none rounded-md bg-secondary"
          />
          <div className="flex justify-between text-xs text-text-muted">
            {bpsSteps.map((step, idx) => (
              <span
                key={idx}
                className={clsx(
                  "cursor-pointer transition-colors hover:text-foreground",
                  sliderIndex === idx && "font-medium text-primary"
                )}
                onClick={() => handleSliderChange(idx)}
              >
                {formatStepLabel(step)}
              </span>
            ))}
          </div>
          <div>
            <label className="mb-1 block text-xs text-text-muted">
              Custom value (B/s)
            </label>
            <input
              type="number"
              value={bpsInput}
              onChange={(event) => {
                setBpsInput(event.target.value);
                syncSliderFromInput(parseInt(event.target.value, 10) || 0);
              }}
              className="w-full rounded border border-foreground/20 bg-background px-3 py-2 text-foreground"
              placeholder="Enter BPS limit"
              min="0"
            />
          </div>
          <DialogFooter className="gap-2 sm:gap-0">
            <Button
              className="cursor-pointer"
              variant="secondary"
              onClick={() => setShowBPSModal(false)}
            >
              Cancel
            </Button>
            <Button className="cursor-pointer" onClick={handleBPSSave}>
              Save
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
