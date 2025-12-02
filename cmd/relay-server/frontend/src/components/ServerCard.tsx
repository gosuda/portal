import { Link } from "react-router-dom";
import { ScrollArea, ScrollBar } from "@/components/ui/scroll-area";
import clsx from "clsx";
import { ReactNode, useState } from "react";

interface ServerCardProps {
  serverId: number;
  name: string;
  description: string;
  tags: string[];
  thumbnail: string;
  owner: string;
  online: boolean;
  dns: string;
  serverUrl: string;
  navigationPath: string;
  navigationState: any;
  isFavorite?: boolean;
  onToggleFavorite?: (serverId: number) => void;
  // Admin controls
  showAdminControls?: boolean;
  leaseId?: string;
  isBanned?: boolean;
  bps?: number;
  onBanStatusChange?: (leaseId: string, isBan: boolean) => void;
  onBPSChange?: (leaseId: string, bps: number) => void;
}

export function ServerCard({
  serverId,
  name,
  description,
  tags,
  thumbnail,
  owner,
  online,
  navigationPath,
  navigationState,
  isFavorite = false,
  onToggleFavorite,
  // Admin controls
  showAdminControls = false,
  leaseId,
  isBanned = false,
  bps = 0,
  onBanStatusChange,
  onBPSChange,
}: ServerCardProps) {
  const [showBPSModal, setShowBPSModal] = useState(false);
  const [bpsInput, setBpsInput] = useState(bps.toString());

  // BPS slider steps: 0 (Unlimited), 10, 100, 1K, 10K, 100K, 1M, 10M
  const bpsSteps = [0, 10, 100, 1000, 10000, 100000, 1000000, 10000000];

  const bpsToSliderIndex = (value: number): number => {
    if (value === 0) return 0;
    const idx = bpsSteps.findIndex(step => step >= value);
    return idx === -1 ? bpsSteps.length - 1 : idx;
  };

  const [sliderIndex, setSliderIndex] = useState(bpsToSliderIndex(bps));

  // Sync input with slider
  const handleSliderChange = (idx: number) => {
    setSliderIndex(idx);
    setBpsInput(bpsSteps[idx].toString());
  };

  // Sync slider with input (find closest step)
  const syncSliderFromInput = (value: number) => {
    const idx = bpsToSliderIndex(value);
    setSliderIndex(idx);
  };
  const handleFavoriteClick = (e: React.MouseEvent) => {
    e.preventDefault();
    e.stopPropagation();
    onToggleFavorite?.(serverId);
  };

  const handleBanClick = (e: React.MouseEvent) => {
    e.preventDefault();
    e.stopPropagation();
    if (leaseId && onBanStatusChange) {
      onBanStatusChange(leaseId, !isBanned);
    }
  };

  const handleBPSSettingsClick = (e: React.MouseEvent) => {
    e.preventDefault();
    e.stopPropagation();
    setSliderIndex(bpsToSliderIndex(bps));
    setBpsInput(bps.toString());
    setShowBPSModal(true);
  };

  const handleBPSSave = () => {
    if (leaseId && onBPSChange) {
      const newBps = parseInt(bpsInput, 10) || 0;
      onBPSChange(leaseId, newBps);
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
    if (value === 0) return "∞";
    if (value >= 1000000) return `${value / 1000000}M`;
    if (value >= 1000) return `${value / 1000}K`;
    return value.toString();
  };

  const handleBPSModalClose = (e: React.MouseEvent) => {
    e.preventDefault();
    e.stopPropagation();
    setShowBPSModal(false);
  };

  const formatBPS = (value: number): string => {
    if (value === 0) return "Unlimited";
    if (value >= 1_000_000_000)
      return `${(value / 1_000_000_000).toFixed(1)} GB/s`;
    if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)} MB/s`;
    if (value >= 1_000) return `${(value / 1_000).toFixed(1)} KB/s`;
    return `${value} B/s`;
  };

  const Wrapper = ({ children }: { children: ReactNode }) =>
    showAdminControls ? (
      <div className="relative">{children}</div>
    ) : (
      <Link
        to={navigationPath}
        state={navigationState}
        className="cursor-pointer"
      >
        {children}
      </Link>
    );

  return (
    <Wrapper>
      <div
        data-hero-key={`server-bg-${serverId}`}
        className={clsx(
          "relative bg-center bg-no-repeat bg-cover rounded-xl shadow-lg hover:shadow-xl transition-shadow duration-300 z-1 border border-foreground dark:border-foreground/40",
          showAdminControls ? "h-[263.5px]" : "h-[174.5px]"
        )}
        style={{ ...(thumbnail && { backgroundImage: `url(${thumbnail})` }) }}
      >
        {/* Favorite button */}
        <button
          onClick={handleFavoriteClick}
          className="absolute top-3 right-3 z-10 p-2 rounded-full bg-background/80 hover:bg-background transition-colors duration-200 cursor-pointer"
          aria-label={isFavorite ? "Remove from favorites" : "Add to favorites"}
        >
          <svg
            xmlns="http://www.w3.org/2000/svg"
            viewBox="0 0 24 24"
            className="w-5 h-5 transition-colors duration-200"
            fill={isFavorite ? "currentColor" : "none"}
            stroke="currentColor"
            strokeWidth="2"
            strokeLinecap="round"
            strokeLinejoin="round"
            style={{ color: isFavorite ? "var(--primary)" : "currentColor" }}
          >
            <polygon points="12 2 15.09 8.26 22 9.27 17 14.14 18.18 21.02 12 17.77 5.82 21.02 7 14.14 2 9.27 8.91 8.26 12 2" />
          </svg>
        </button>

        {/* Content overlay - not part of hero transition */}
        <div className="relative h-full w-full bg-background/80 rounded-xl flex flex-col gap-4 p-4 items-start text-start">
          <div className="w-full flex flex-1 flex-col justify-between gap-4">
            <div className="flex flex-col gap-2">
              <div className="flex items-center gap-2">
                <div
                  className={clsx(
                    "w-2.5 h-2.5 rounded-full",
                    online ? "bg-green-status" : "bg-red-500"
                  )}
                />
                <p
                  className={clsx(
                    "text-sm font-medium leading-normal",
                    online ? "text-green-status" : "text-red-500"
                  )}
                >
                  {online ? "Online" : "Offline"}
                </p>
              </div>
              <p className="text-foreground text-lg font-bold leading-tight truncate max-w-full">
                {name}
              </p>
              {description && (
                <p className="text-text-muted text-sm font-normal leading-normal truncate max-w-full">
                  {description}
                </p>
              )}
              {tags && tags.length > 0 && (
                <ScrollArea className="w-full mt-1">
                  <div className="flex gap-1.5 min-w-max">
                    {tags.map((tag, index) => (
                      <span
                        key={index}
                        className="px-2 py-1 bg-secondary text-primary text-xs font-medium rounded whitespace-nowrap"
                      >
                        {tag}
                      </span>
                    ))}
                  </div>
                  <ScrollBar orientation="horizontal" />
                </ScrollArea>
              )}
              {owner && (
                <p className="text-text-muted text-xs font-normal leading-normal truncate max-w-full">
                  by {owner}
                </p>
              )}
            </div>
            {showAdminControls && leaseId && (
              <div className="flex flex-col gap-2 w-full">
                {/* BPS Row: display on left, settings button on right */}
                <div className="flex items-center justify-between w-full">
                  <span className="text-sm text-text-muted">
                    BPS:{" "}
                    <span className="font-medium text-foreground">
                      {formatBPS(bps)}
                    </span>
                  </span>
                  <button
                    onClick={handleBPSSettingsClick}
                    className="px-3 py-1 text-xs rounded bg-secondary hover:bg-secondary/80 text-secondary-foreground transition-colors cursor-pointer"
                  >
                    Settings
                  </button>
                </div>
                {/* Ban button */}
                <button
                  onClick={handleBanClick}
                  className={clsx(
                    "w-full px-4 py-2 rounded font-medium transition-colors cursor-pointer text-white",
                    isBanned
                      ? "bg-green-600 hover:bg-green-700"
                      : "bg-red-600 hover:bg-red-700"
                  )}
                >
                  {isBanned ? "Unban" : "Ban"}
                </button>
              </div>
            )}
          </div>
        </div>
      </div>
      <div className="absolute top-2 left-2 h-full w-full bg-secondary/70 rounded-xl z-0" />

      {/* BPS Settings Modal */}
      {showBPSModal && (
        <div
          className="fixed inset-0 bg-black/50 flex items-center justify-center z-50"
          onClick={handleBPSModalClose}
        >
          <div
            className="bg-background rounded-lg p-6 w-96 shadow-xl border border-foreground/20"
            onClick={(e) => e.stopPropagation()}
          >
            <h3 className="text-lg font-bold mb-4">BPS Settings</h3>
            <p className="text-sm text-text-muted mb-2">
              Set bytes-per-second limit (0 = unlimited)
            </p>
            {/* Current value display */}
            <div className="text-center text-xl font-bold mb-4 text-primary">
              {formatSliderLabel(parseInt(bpsInput, 10) || 0)}
            </div>
            {/* Slider */}
            <input
              type="range"
              min="0"
              max={bpsSteps.length - 1}
              value={sliderIndex}
              onChange={(e) => {
                const idx = parseInt(e.target.value, 10);
                handleSliderChange(idx);
              }}
              className="w-full h-2 bg-secondary rounded-lg appearance-none cursor-pointer mb-2"
            />
            {/* Step labels */}
            <div className="flex justify-between text-xs text-text-muted mb-4">
              {bpsSteps.map((step, idx) => (
                <span
                  key={idx}
                  className={clsx(
                    "cursor-pointer hover:text-foreground transition-colors",
                    sliderIndex === idx && "text-primary font-medium"
                  )}
                  onClick={() => handleSliderChange(idx)}
                >
                  {formatStepLabel(step)}
                </span>
              ))}
            </div>
            {/* Manual input */}
            <div className="mb-4">
              <label className="text-xs text-text-muted mb-1 block">Custom value (B/s)</label>
              <input
                type="number"
                value={bpsInput}
                onChange={(e) => {
                  setBpsInput(e.target.value);
                  syncSliderFromInput(parseInt(e.target.value, 10) || 0);
                }}
                className="w-full px-3 py-2 border border-foreground/20 rounded bg-background text-foreground"
                placeholder="Enter BPS limit"
                min="0"
              />
            </div>
            <div className="flex gap-2">
              <button
                onClick={handleBPSModalClose}
                className="flex-1 px-4 py-2 rounded bg-secondary hover:bg-secondary/80 text-secondary-foreground transition-colors cursor-pointer"
              >
                Cancel
              </button>
              <button
                onClick={handleBPSSave}
                className="flex-1 px-4 py-2 rounded bg-primary hover:bg-primary/90 text-primary-foreground transition-colors cursor-pointer"
              >
                Save
              </button>
            </div>
          </div>
        </div>
      )}
    </Wrapper>
  );
}
