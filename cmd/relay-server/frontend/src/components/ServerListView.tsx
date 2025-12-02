import { Header } from "@/components/Header";
import { SearchBar } from "@/components/SearchBar";
import { ServerCard } from "@/components/ServerCard";
import type { ClientServer } from "@/hooks/useServerList";
import type { AdminServer } from "@/hooks/useAdmin";
import type { SortOption, StatusFilter } from "@/types/filters";

// Admin-specific filter for ban status
export type BanFilter = "all" | "banned" | "active";

interface ServerListViewProps {
  // Header customization
  title?: string;
  // Search & Filter state
  searchQuery: string;
  status: StatusFilter;
  sortBy: SortOption;
  selectedTags: string[];
  availableTags: string[];
  // Server data
  filteredServers: ClientServer[] | AdminServer[];
  favorites: number[];
  // Handlers
  onSearchChange: (value: string) => void;
  onStatusChange: (value: StatusFilter) => void;
  onSortByChange: (value: SortOption) => void;
  onTagToggle: (tag: string) => void;
  onToggleFavorite: (serverId: number) => void;
  // Admin mode (optional)
  isAdmin?: boolean;
  banFilter?: BanFilter;
  onBanFilterChange?: (value: BanFilter) => void;
  onBanStatusChange?: (leaseId: string, isBan: boolean) => void;
  onBPSChange?: (leaseId: string, bps: number) => void;
}

function isAdminServer(
  server: ClientServer | AdminServer
): server is AdminServer {
  return "peerId" in server;
}

export function ServerListView({
  title = "PORTAL",
  searchQuery,
  status,
  sortBy,
  selectedTags,
  availableTags,
  filteredServers,
  favorites,
  onSearchChange,
  onStatusChange,
  onSortByChange,
  onTagToggle,
  onToggleFavorite,
  // Admin props
  isAdmin = false,
  banFilter = "all",
  onBanFilterChange,
  onBanStatusChange,
  onBPSChange,
}: ServerListViewProps) {
  return (
    <div className="relative flex h-auto min-h-screen w-full flex-col">
      <div className="flex h-full grow flex-col">
        <div className="flex flex-1 justify-center">
          <div className="flex flex-col w-full max-w-6xl flex-1 px-4 md:px-8">
            <div className="sticky top-0 z-10 bg-background pb-4 pt-5">
              <Header title={title} />
              <SearchBar
                searchQuery={searchQuery}
                onSearchChange={onSearchChange}
                status={status}
                onStatusChange={onStatusChange}
                sortBy={sortBy}
                onSortByChange={onSortByChange}
                availableTags={availableTags}
                selectedTags={selectedTags}
                onAddTag={onTagToggle}
                onRemoveTag={onTagToggle}
              />
              {isAdmin && onBanFilterChange && (
                <div className="flex gap-2 mt-4 px-4 sm:px-6">
                  <button
                    onClick={() => onBanFilterChange("all")}
                    className={`px-4 py-2 rounded font-medium transition-colors ${
                      banFilter === "all"
                        ? "bg-primary text-primary-foreground"
                        : "bg-secondary text-secondary-foreground hover:bg-secondary/80"
                    }`}
                  >
                    All
                  </button>
                  <button
                    onClick={() => onBanFilterChange("active")}
                    className={`px-4 py-2 rounded font-medium transition-colors ${
                      banFilter === "active"
                        ? "bg-green-600 text-white"
                        : "bg-secondary text-secondary-foreground hover:bg-secondary/80"
                    }`}
                  >
                    Active
                  </button>
                  <button
                    onClick={() => onBanFilterChange("banned")}
                    className={`px-4 py-2 rounded font-medium transition-colors ${
                      banFilter === "banned"
                        ? "bg-red-600 text-white"
                        : "bg-secondary text-secondary-foreground hover:bg-secondary/80"
                    }`}
                  >
                    Banned
                  </button>
                </div>
              )}
            </div>
            <main className="flex-1">
              <div className="grid grid-cols-1 min-[500px]:grid-cols-2 md:grid-cols-3 gap-6 p-4 min-[500px]:p-6">
                {filteredServers.length > 0 ? (
                  filteredServers.map((server) => (
                    <ServerCard
                      key={server.id}
                      serverId={server.id}
                      name={server.name}
                      description={server.description}
                      tags={server.tags}
                      thumbnail={server.thumbnail}
                      owner={server.owner}
                      online={server.online}
                      dns={server.dns}
                      serverUrl={server.link}
                      navigationPath={server.link || "#"}
                      navigationState={{
                        id: server.id,
                        name: server.name,
                        description: server.description,
                        tags: server.tags,
                        thumbnail: server.thumbnail,
                        owner: server.owner,
                        online: server.online,
                        serverUrl: server.link,
                      }}
                      isFavorite={favorites.includes(server.id)}
                      onToggleFavorite={onToggleFavorite}
                      // Admin controls
                      showAdminControls={isAdmin && isAdminServer(server)}
                      leaseId={
                        isAdminServer(server) ? server.peerId : undefined
                      }
                      isBanned={
                        isAdminServer(server) ? server.isBanned : undefined
                      }
                      bps={isAdminServer(server) ? server.bps : undefined}
                      onBanStatusChange={onBanStatusChange}
                      onBPSChange={onBPSChange}
                    />
                  ))
                ) : (
                  <div className="col-span-full text-center py-12">
                    <p className="text-text-muted text-lg">
                      No servers found matching your criteria
                    </p>
                  </div>
                )}
              </div>
            </main>
          </div>
        </div>
      </div>
    </div>
  );
}
