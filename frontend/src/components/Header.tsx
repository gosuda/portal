import { LogOut } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { TunnelCommandModal } from "@/components/TunnelCommandModal";
import { getReleaseVersion } from "@/lib/releaseVersion";
import clsx from "clsx";

interface HeaderProps {
  title?: string;
  isAdmin?: boolean;
  onLogout?: () => void;
  ctaLabel?: string;
}

const repoURL = "https://github.com/gosuda/portal";
const architectureURL = `${repoURL}/blob/main/docs/architecture.md`;

export function Header({
  title = "PORTAL",
  isAdmin,
  onLogout,
  ctaLabel = "Add Your Server",
}: HeaderProps) {
  const releaseVersion = getReleaseVersion();

  return (
    <header className="flex flex-wrap items-center justify-between gap-4 px-1 py-2 sm:px-2">
      <div className="flex items-center gap-4 text-foreground">
        <div className="flex h-12 w-12 items-center justify-center rounded-2xl bg-background text-primary">
          <svg
            xmlns="http://www.w3.org/2000/svg"
            width="26"
            height="26"
            viewBox="0 0 906.26 1457.543"
            className="text-primary"
          >
            <path
              fill="currentColor"
              d="M254.854 137.158c-34.46 84.407-88.363 149.39-110.934 245.675 90.926-187.569 308.397-483.654 554.729-348.685 135.487 74.216 194.878 270.78 206.058 467.566 21.924 385.996-190.977 853.604-467.585 943.057-174.879 56.543-307.375-86.447-364.527-198.115-176.498-344.82 2.041-910.077 182.259-1109.498zm198.13 7.918C202.61 280.257 4.622 968.542 207.322 1270.414c51.713 77.029 194.535 160.648 285.294 71.318-209.061 31.529-288.389-176.143-301.145-340.765 31.411 147.743 139.396 326.12 309.075 253.588 251.957-107.723 376.778-648.46 269.433-966.817 22.394 134.616 15.572 317.711-47.551 412.087 86.655-230.615 7.903-704.478-269.444-554.749z"
            />
          </svg>
        </div>

        <div className="space-y-1">
          <div className="flex flex-wrap items-center gap-2">
            <h2 className="text-xl font-extrabold tracking-tight text-foreground">
              {title}
            </h2>
            {releaseVersion && (
              <span className="rounded-full bg-secondary px-2.5 py-0.5 text-xs font-semibold text-text-muted">
                {releaseVersion}
              </span>
            )}
          </div>
          <p className="text-sm text-text-muted">
            Instant public URLs for local and private apps.
          </p>
        </div>
      </div>

      <div className="flex flex-wrap items-center gap-3 sm:gap-4">
        {!isAdmin && (
          <nav className="hidden items-center gap-5 text-sm font-medium text-text-muted md:flex">
            <a href="#live-servers" className="transition-colors hover:text-foreground">
              Discover
            </a>
            <a
              href={repoURL}
              target="_blank"
              rel="noopener noreferrer"
              className="transition-colors hover:text-foreground"
            >
              Docs
            </a>
            <a
              href={architectureURL}
              target="_blank"
              rel="noopener noreferrer"
              className="transition-colors hover:text-foreground"
            >
              Architecture
            </a>
          </nav>
        )}

        <TunnelCommandModal
          trigger={
            <Button
              className={clsx(
                "h-11 cursor-pointer rounded-full px-5 text-base font-semibold shadow-none",
                isAdmin && "hidden sm:inline-flex"
              )}
            >
              <span className="truncate">{ctaLabel}</span>
            </Button>
          }
        />

        {isAdmin && onLogout && (
          <TooltipProvider>
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  variant="outline"
                  size="icon"
                  onClick={onLogout}
                  className="cursor-pointer rounded-full text-foreground hover:text-destructive"
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
