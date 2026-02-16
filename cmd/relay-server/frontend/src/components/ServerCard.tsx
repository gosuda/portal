import { Link } from "react-router-dom";
import { ScrollArea, ScrollBar } from "@/components/ui/scroll-area";
import clsx from "clsx";
import { type ReactNode, useEffect, useMemo, useState } from "react";
import type { ServerNavigationState } from "@/types/server";
import { BPSSettingsModal } from "@/components/BPSSettingsModal";

function formatBPS(value: number): string {
  if (value === 0) return "Unlimited";
  if (value >= 1_000_000_000) return `${(value / 1_000_000_000).toFixed(1)} GB/s`;
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)} MB/s`;
  if (value >= 1_000) return `${(value / 1_000).toFixed(1)} KB/s`;
  return `${value} B/s`;
}

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
  serverUrl: string;
  navigationPath: string;
  navigationState: ServerNavigationState;
  isFavorite?: boolean;
  onToggleFavorite?: (serverId: number) => void;
  // Admin controls
  showAdminControls?: boolean;
  leaseId?: string;
  isBanned?: boolean;
  isApproved?: boolean;
  isDenied?: boolean;
  bps?: number;
  ip?: string;
  isIPBanned?: boolean;
  onBanStatusChange?: (leaseId: string, isBan: boolean) => void;
  onBPSChange?: (leaseId: string, bps: number) => void;
  onApproveStatusChange?: (leaseId: string, approve: boolean) => void;
  onDenyStatusChange?: (leaseId: string, deny: boolean) => void;
  onIPBanStatusChange?: (ip: string, isBan: boolean) => void;
  // Selection for bulk actions
  isSelected?: boolean;
  onToggleSelect?: (leaseId: string) => void;
}

interface CardWrapperProps {
  showAdminControls: boolean;
  navigationPath: string;
  navigationState: ServerNavigationState;
  children: ReactNode;
}

function CardWrapper({
  showAdminControls,
  navigationPath,
  navigationState,
  children,
}: CardWrapperProps) {
  if (showAdminControls) {
    return <div className="relative">{children}</div>;
  }

  return (
    <Link
      to={navigationPath}
      state={navigationState}
      className="relative cursor-pointer block"
    >
      {children}
    </Link>
  );
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
  navigationPath,
  navigationState,
  isFavorite = false,
  onToggleFavorite,
  // Admin controls
  showAdminControls = false,
  leaseId,
  isBanned = false,
  isApproved = false,
  isDenied = false,
  bps = 0,
  ip = "",
  isIPBanned = false,
  onBanStatusChange,
  onBPSChange,
  onApproveStatusChange,
  onDenyStatusChange,
  onIPBanStatusChange,
  // Selection for bulk actions
  isSelected = false,
  onToggleSelect,
}: ServerCardProps) {
  const [showBPSModal, setShowBPSModal] = useState(false);
  const [currentTime, setCurrentTime] = useState(() => Date.now());

  useEffect(() => {
    const intervalID = window.setInterval(() => {
      setCurrentTime(Date.now());
    }, 60_000);

    return () => {
      window.clearInterval(intervalID);
    };
  }, []);

  const handleFavoriteClick = (e: React.MouseEvent) => {
    e.preventDefault();
    e.stopPropagation();
    onToggleFavorite?.(serverId);
  };

  const handleSelectClick = (e: React.MouseEvent) => {
    e.preventDefault();
    e.stopPropagation();
    if (leaseId && onToggleSelect) {
      onToggleSelect(leaseId);
    }
  };

  const handleBanClick = (e: React.MouseEvent) => {
    e.preventDefault();
    e.stopPropagation();
    if (leaseId && onBanStatusChange) {
      onBanStatusChange(leaseId, !isBanned);
    }
  };

  const handleApproveClick = (e: React.MouseEvent) => {
    e.preventDefault();
    e.stopPropagation();
    if (leaseId && onApproveStatusChange) {
      onApproveStatusChange(leaseId, !isApproved);
    }
  };

  const handleDenyClick = (e: React.MouseEvent) => {
    e.preventDefault();
    e.stopPropagation();
    if (leaseId && onDenyStatusChange) {
      onDenyStatusChange(leaseId, !isDenied);
    }
  };

  const handleIPBanClick = (e: React.MouseEvent) => {
    e.preventDefault();
    e.stopPropagation();
    if (ip && onIPBanStatusChange) {
      onIPBanStatusChange(ip, !isIPBanned);
    }
  };

  const handleBPSSettingsClick = (e: React.MouseEvent) => {
    e.preventDefault();
    e.stopPropagation();
    setShowBPSModal(true);
  };

  const formattedDuration = useMemo(() => {
    if (!firstSeen) return "";
    const start = new Date(firstSeen).getTime();
    const diff = Math.max(0, currentTime - start);

    const seconds = Math.floor(diff / 1000);
    const minutes = Math.floor(seconds / 60);
    const hours = Math.floor(minutes / 60);
    const days = Math.floor(hours / 24);

    if (days > 0) return `${days}d ${hours % 24}h`;
    if (hours > 0) return `${hours}h ${minutes % 60}m`;
    if (minutes > 0) return `${minutes}m`;
    return `${seconds}s`;
  }, [currentTime, firstSeen]);

  return (
    <CardWrapper
      showAdminControls={showAdminControls}
      navigationPath={navigationPath}
      navigationState={navigationState}
    >
      <article
        data-hero-key={`server-bg-${serverId}`}
        className={clsx(
          "relative w-full overflow-hidden rounded-3xl group border border-white/10 shadow-lg",
          showAdminControls ? "h-[286px]" : "h-[174.5px]"
        )}
      >
        {/* Background Image */}
        <div
          className="absolute inset-0 bg-cover bg-center transition-transform duration-700 group-hover:scale-105"
          style={{
            backgroundImage: thumbnail
              ? `url(${thumbnail})`
              : "linear-gradient(135deg, var(--card) 0%, var(--background) 100%)",
          }}
        />

        {/* Gradient Overlay */}
        <div className="absolute inset-0 bg-linear-to-t from-black via-black/60 to-transparent" />

        {/* Content */}
        <div className="relative z-10 flex h-full flex-col justify-between p-5">
          {/* Top Row: Status Badge + Action Button */}
          <div className="flex items-start justify-between">
            {/* Online/Offline Status Badge */}
            <div className="flex items-center gap-2 rounded-full bg-black/40 px-3 py-1 backdrop-blur-sm border border-white/5">
              <div
                className={clsx(
                  "size-2 rounded-full",
                  online
                    ? "bg-primary shadow-[0_0_8px_rgba(0,219,219,0.8)] animate-pulse"
                    : "bg-gray-500"
                )}
              />
              <span
                className={clsx(
                  "text-[10px] font-bold uppercase tracking-wider",
                  online ? "text-white" : "text-white/60"
                )}
              >
                {online ? "Online" : "Offline"}
                {formattedDuration && online && ` · ${formattedDuration}`}
              </span>
            </div>

            {/* Admin mode: Checkbox / Normal mode: Favorite star */}
            {showAdminControls ? (
              <button
                onClick={handleSelectClick}
                className={clsx(
                  "flex size-8 items-center justify-center rounded-full backdrop-blur-md transition-colors border border-white/5 cursor-pointer",
                  isSelected
                    ? "bg-primary text-black"
                    : "bg-black/40 text-white/70 hover:bg-primary hover:text-black"
                )}
                aria-label={isSelected ? "Deselect" : "Select"}
              >
                <svg
                  xmlns="http://www.w3.org/2000/svg"
                  viewBox="0 0 24 24"
                  className="w-[18px] h-[18px]"
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
                  "flex size-8 items-center justify-center rounded-full backdrop-blur-md transition-colors border border-white/5 cursor-pointer",
                  isFavorite
                    ? "bg-primary text-black"
                    : "bg-black/40 text-white/70 hover:bg-primary hover:text-black"
                )}
                aria-label={
                  isFavorite ? "Remove from favorites" : "Add to favorites"
                }
              >
                <svg
                  xmlns="http://www.w3.org/2000/svg"
                  viewBox="0 0 24 24"
                  className="w-[18px] h-[18px]"
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

          {/* Bottom Content */}
          <div className="flex flex-col gap-3">
            {/* Server Info Row */}
            <div className="flex items-end justify-between gap-3">
              <div className="flex flex-col gap-1.5 flex-1 min-w-0">
                {/* Server Name */}
                <h3 className="font-display text-xl font-bold leading-tight text-white truncate">
                  {name}
                </h3>

                {/* Description */}
                {description && (
                  <p className="text-xs text-white/70 line-clamp-1 font-medium">
                    {description}
                  </p>
                )}

                {/* Tags */}
                {tags && tags.length > 0 && (
                  <ScrollArea className="w-full mt-1">
                    <div className="flex gap-1.5 min-w-max">
                      {tags.map((tag, index) => (
                        <span
                          key={index}
                          className="rounded bg-primary/20 px-2 py-0.5 text-[10px] font-bold uppercase tracking-wider text-primary border border-primary/30 whitespace-nowrap"
                        >
                          #{tag}
                        </span>
                      ))}
                    </div>
                    <ScrollBar orientation="horizontal" />
                  </ScrollArea>
                )}

                {/* Owner */}
                {owner && (
                  <span className="text-[10px] font-medium text-white/50">
                    by {owner}
                  </span>
                )}
              </div>

              {/* Thumbnail Avatar (if no admin controls) */}
              {!showAdminControls && thumbnail && (
                <div className="shrink-0">
                  <div className="size-10 overflow-hidden rounded-xl border border-white/20 shadow-lg">
                    <img
                      alt={`${name} avatar`}
                      className="h-full w-full object-cover"
                      src={thumbnail}
                    />
                  </div>
                </div>
              )}
            </div>

            {/* Admin Controls */}
            {showAdminControls && leaseId && (
              <div className="flex flex-col gap-2 w-full mt-2">
                {/* BPS Row */}
                <div className="flex items-center justify-between w-full">
                  <span className="text-xs text-white/60">
                    BPS:{" "}
                    <span className="font-medium text-white">
                      {formatBPS(bps)}
                    </span>
                  </span>
                  <button
                    onClick={handleBPSSettingsClick}
                    className="px-3 py-1 text-[10px] rounded-full bg-white/10 hover:bg-white/20 text-white/80 transition-colors cursor-pointer border border-white/10"
                  >
                    Settings
                  </button>
                </div>

                {/* IP display */}
                {isApproved && ip && (
                  <div className="text-[10px] text-white/50">
                    IP: <span className="font-mono">{ip}</span>
                    {isIPBanned && (
                      <span className="ml-2 text-red-400">(Banned)</span>
                    )}
                  </div>
                )}

                {/* Approve/Deny buttons */}
                {!isApproved && !isDenied ? (
                  <div className="flex gap-2 w-full">
                    <button
                      onClick={handleApproveClick}
                      className="flex-1 px-4 py-2 rounded-lg font-medium text-xs transition-colors cursor-pointer text-white bg-green-600/80 hover:bg-green-600 backdrop-blur-sm"
                    >
                      Approve
                    </button>
                    <button
                      onClick={handleDenyClick}
                      className="flex-1 px-4 py-2 rounded-lg font-medium text-xs transition-colors cursor-pointer text-white bg-red-600/80 hover:bg-red-600 backdrop-blur-sm"
                    >
                      Deny
                    </button>
                  </div>
                ) : (
                  <button
                    onClick={ip ? handleIPBanClick : handleBanClick}
                    className={clsx(
                      "w-full px-4 py-2 rounded-lg font-medium text-xs transition-colors cursor-pointer text-white backdrop-blur-sm",
                      (ip ? isIPBanned : isBanned)
                        ? "bg-green-600/80 hover:bg-green-600"
                        : "bg-red-600/80 hover:bg-red-600"
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
        </div>
      </article>

      {/* BPS Settings Modal */}
      {showAdminControls && leaseId && onBPSChange && (
        <BPSSettingsModal
          open={showBPSModal}
          onOpenChange={setShowBPSModal}
          bps={bps}
          leaseId={leaseId}
          onBPSChange={onBPSChange}
        />
      )}
    </CardWrapper>
  );
}
