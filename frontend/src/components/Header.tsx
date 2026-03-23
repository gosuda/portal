import { useEffect, useState } from "react";
import { Check, Copy, LogOut } from "lucide-react";
import { Button } from "@/components/ui/button";
import { ThemeToggleButton } from "@/components/ThemeToggleButton";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { getReleaseVersion } from "@/lib/releaseVersion";

interface HeaderProps {
  title?: string;
  isAdmin?: boolean;
  onLogout?: () => void;
}

const repoURL = "https://github.com/gosuda/portal";
const SERVER_OWNER_ADDRESS_META_NAME = "portal-server-owner-address";

function getServerOwnerAddress(doc?: Document): string {
  const targetDoc =
    doc ?? (typeof document !== "undefined" ? document : undefined);
  if (!targetDoc) {
    return "";
  }

  return (
    targetDoc
      .querySelector<HTMLMetaElement>(
        `meta[name="${SERVER_OWNER_ADDRESS_META_NAME}"]`
      )
      ?.content.trim() || ""
  );
}

function formatOwnerAddress(address: string): string {
  const trimmed = address.trim();
  if (trimmed.length <= 14) {
    return trimmed;
  }

  return `${trimmed.slice(0, 6)}...${trimmed.slice(-4)}`;
}

export function Header({
  title = "PORTAL",
  isAdmin,
  onLogout,
}: HeaderProps) {
  const releaseVersion = getReleaseVersion();
  const serverOwnerAddress = getServerOwnerAddress();
  const [ownerAddressCopied, setOwnerAddressCopied] = useState(false);
  const displayOwnerAddress = formatOwnerAddress(serverOwnerAddress);

  useEffect(() => {
    if (!ownerAddressCopied) {
      return;
    }

    const timer = window.setTimeout(() => {
      setOwnerAddressCopied(false);
    }, 1800);

    return () => {
      window.clearTimeout(timer);
    };
  }, [ownerAddressCopied]);

  const handleCopyOwnerAddress = async () => {
    if (!serverOwnerAddress) {
      return;
    }

    try {
      await navigator.clipboard.writeText(serverOwnerAddress);
      setOwnerAddressCopied(true);
    } catch (error) {
      console.error("Failed to copy owner address", error);
    }
  };

  return (
    <header className="flex flex-wrap items-center justify-between gap-x-4 gap-y-3 py-2 lg:flex-nowrap">
      <div className="flex min-w-0 flex-1 flex-wrap items-center gap-x-4 gap-y-2 text-foreground sm:gap-x-6 lg:gap-x-7">
        <div className="min-w-0 text-foreground">
          <div className="flex min-w-0 items-center gap-1.5 sm:gap-2">
            <div className="flex h-10 w-10 shrink-0 items-center justify-center">
              <svg
                xmlns="http://www.w3.org/2000/svg"
                width="27"
                height="27"
                viewBox="0 0 906.26 1457.543"
                className="h-6 w-6 text-primary"
              >
                <path
                  fill="currentColor"
                  d="M254.854 137.158c-34.46 84.407-88.363 149.39-110.934 245.675 90.926-187.569 308.397-483.654 554.729-348.685 135.487 74.216 194.878 270.78 206.058 467.566 21.924 385.996-190.977 853.604-467.585 943.057-174.879 56.543-307.375-86.447-364.527-198.115-176.498-344.82 2.041-910.077 182.259-1109.498zm198.13 7.918C202.61 280.257 4.622 968.542 207.322 1270.414c51.713 77.029 194.535 160.648 285.294 71.318-209.061 31.529-288.389-176.143-301.145-340.765 31.411 147.743 139.396 326.12 309.075 253.588 251.957-107.723 376.778-648.46 269.433-966.817 22.394 134.616 15.572 317.711-47.551 412.087 86.655-230.615 7.903-704.478-269.444-554.749z"
                />
              </svg>
            </div>

            <div className="flex min-w-0 flex-wrap items-center gap-2.5">
              <h2 className="min-w-0 break-words text-xl leading-none font-extrabold tracking-tight text-foreground sm:text-2xl">
                {title}
              </h2>
              {releaseVersion && (
                <span className="inline-flex h-6 items-center rounded-full bg-secondary px-2.5 text-xs font-semibold text-text-muted">
                  {releaseVersion}
                </span>
              )}
            </div>
          </div>
        </div>
        {!isAdmin && (
          <nav className="hidden items-center gap-6 pl-2 text-base font-semibold text-text-muted xl:flex xl:pl-3">
            <a
              href="#quick-start"
              className="transition-colors hover:text-foreground"
            >
              Quick Start
            </a>
            <a
              href="#live-servers"
              className="transition-colors hover:text-foreground"
            >
              Live apps
            </a>
            <a
              href="#official-registry"
              className="transition-colors hover:text-foreground"
            >
              Official registry
            </a>
          </nav>
        )}
      </div>

      <div className="flex shrink-0 flex-wrap items-center gap-2 sm:gap-3">
        {serverOwnerAddress && (
          <div
            title={serverOwnerAddress}
            className="inline-flex h-11 max-w-full items-center gap-2 rounded-full border border-sky-500/20 bg-background/85 pl-1.5 pr-1 shadow-[0_10px_28px_rgba(15,23,42,0.08)] backdrop-blur"
          >
            <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-linear-to-br from-sky-400 via-cyan-300 to-blue-500 shadow-[0_8px_20px_rgba(56,189,248,0.28)]">
              <svg
                xmlns="http://www.w3.org/2000/svg"
                width="16"
                height="16"
                viewBox="0 0 906.26 1457.543"
                className="h-4 w-4 text-slate-950"
              >
                <path
                  fill="currentColor"
                  d="M254.854 137.158c-34.46 84.407-88.363 149.39-110.934 245.675 90.926-187.569 308.397-483.654 554.729-348.685 135.487 74.216 194.878 270.78 206.058 467.566 21.924 385.996-190.977 853.604-467.585 943.057-174.879 56.543-307.375-86.447-364.527-198.115-176.498-344.82 2.041-910.077 182.259-1109.498zm198.13 7.918C202.61 280.257 4.622 968.542 207.322 1270.414c51.713 77.029 194.535 160.648 285.294 71.318-209.061 31.529-288.389-176.143-301.145-340.765 31.411 147.743 139.396 326.12 309.075 253.588 251.957-107.723 376.778-648.46 269.433-966.817 22.394 134.616 15.572 317.711-47.551 412.087 86.655-230.615 7.903-704.478-269.444-554.749z"
                />
              </svg>
            </div>

            <span className="max-w-[6.75rem] truncate font-mono text-[13px] font-semibold tracking-tight text-foreground sm:max-w-[8.5rem]">
              {displayOwnerAddress}
            </span>

            <button
              type="button"
              onClick={() => {
                void handleCopyOwnerAddress();
              }}
              className="inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-full border border-border/70 bg-background/90 text-text-muted transition-colors hover:text-foreground"
              aria-label="Copy owner address"
            >
              {ownerAddressCopied ? (
                <Check className="h-4 w-4 text-sky-500" />
              ) : (
                <Copy className="h-4 w-4" />
              )}
            </button>
          </div>
        )}
        {!isAdmin && (
          <a
            href={repoURL}
            target="_blank"
            rel="noopener noreferrer"
            className="hidden h-11 w-11 items-center justify-center text-foreground transition-transform transition-colors hover:-translate-y-0.5 hover:text-primary xl:inline-flex"
            aria-label="View source on GitHub"
          >
            <svg
              height="20"
              width="20"
              viewBox="0 0 24 24"
              fill="currentColor"
              className="opacity-80 transition-opacity hover:opacity-100"
            >
              <path d="M12 1C5.923 1 1 5.923 1 12c0 4.867 3.149 8.979 7.521 10.436.55.096.756-.233.756-.522 0-.262-.013-1.128-.013-2.049-2.764.509-3.479-.674-3.699-1.292-.124-.317-.66-1.293-1.127-1.554-.385-.207-.936-.715-.014-.729.866-.014 1.485.797 1.691 1.128.99 1.663 2.571 1.196 3.204.907.096-.715.385-1.196.701-1.471-2.448-.275-5.005-1.224-5.005-5.432 0-1.196.426-2.186 1.128-2.956-.111-.275-.496-1.402.11-2.915 0 0 .921-.288 3.024 1.128a10.193 10.193 0 0 1 2.75-.371c.936 0 1.871.123 2.75.371 2.104-1.43 3.025-1.128 3.025-1.128.605 1.513.221 2.64.111 2.915.701.77 1.127 1.747 1.127 2.956 0 4.222-2.571 5.157-5.019 5.432.399.344.743 1.004.743 2.035 0 1.471-.014 2.654-.014 3.025 0 .289.206.632.756.522C19.851 20.979 23 16.854 23 12c0-6.077-4.922-11-11-11Z" />
            </svg>
          </a>
        )}

        <ThemeToggleButton className="hidden xl:inline-flex" />

        {isAdmin && onLogout && (
          <TooltipProvider>
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  variant="outline"
                  size="icon"
                  onClick={onLogout}
                  className="h-11 w-11 cursor-pointer rounded-full text-foreground hover:text-destructive"
                  aria-label="Logout"
                >
                  <LogOut className="h-5 w-5" />
                </Button>
              </TooltipTrigger>
              <TooltipContent>
                <p>Logout</p>
              </TooltipContent>
            </Tooltip>
          </TooltipProvider>
        )}
      </div>
    </header>
  );
}
