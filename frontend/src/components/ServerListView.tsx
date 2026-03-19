import { useEffect, useMemo, useState } from "react";
import { Header } from "@/components/Header";
import { LandingHero } from "@/components/LandingHero";
import { SearchBar } from "@/components/SearchBar";
import { ServerCard } from "@/components/ServerCard";
import { TagCombobox } from "@/components/TagCombobox";
import type { ClientServer } from "@/hooks/useServerList";
import type { AdminServer, ApprovalMode } from "@/hooks/useAdmin";
import type { SortOption, StatusFilter } from "@/types/filters";
import { StatusSelect } from "@/components/select/StatusSelect";
import { BanStatusButtons } from "@/components/button/BanStatusButtons";
import { SortbySelect } from "@/components/select/SortbySelect";
import { ApprovalModeToggle } from "@/components/button/ApprovalModeToggle";
import { FloatingActionBar } from "@/components/FloatingActionBar";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

export type BanFilter = "all" | "banned" | "active";
type ListServer = ClientServer | AdminServer;

interface ServerListViewProps {
  title?: string;
  searchQuery: string;
  status: StatusFilter;
  sortBy: SortOption;
  selectedTags: string[];
  availableTags: string[];
  filteredServers: ClientServer[] | AdminServer[];
  favorites: number[];
  onSearchChange: (value: string) => void;
  onStatusChange: (value: StatusFilter) => void;
  onSortByChange: (value: SortOption) => void;
  onTagToggle: (tag: string) => void;
  onToggleFavorite: (serverId: number) => void;
  isAdmin?: boolean;
  banFilter?: BanFilter;
  approvalMode?: ApprovalMode;
  onBanFilterChange?: (value: BanFilter) => void;
  onBanStatusChange?: (
    leaseId: string,
    isBan: boolean
  ) => void | Promise<void>;
  onBPSChange?: (leaseId: string, bps: number) => void | Promise<void>;
  onApprovalModeChange?: (mode: ApprovalMode) => void;
  onApproveStatusChange?: (
    leaseId: string,
    approve: boolean
  ) => void | Promise<void>;
  onDenyStatusChange?: (
    leaseId: string,
    deny: boolean
  ) => void | Promise<void>;
  onIPBanStatusChange?: (ip: string, isBan: boolean) => void | Promise<void>;
  onBulkApprove?: (leaseIds: string[]) => void | Promise<void>;
  onBulkDeny?: (leaseIds: string[]) => void | Promise<void>;
  onBulkBan?: (leaseIds: string[]) => void | Promise<void>;
  onLogout?: () => void;
}

function isAdminServer(server: ListServer): server is AdminServer {
  return "peerId" in server;
}

function toAdminServer(server: ListServer): AdminServer | undefined {
  return isAdminServer(server) ? server : undefined;
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
  isAdmin = false,
  banFilter = "all",
  approvalMode = "auto",
  onBanFilterChange,
  onBanStatusChange,
  onBPSChange,
  onApprovalModeChange,
  onApproveStatusChange,
  onDenyStatusChange,
  onIPBanStatusChange,
  onBulkApprove,
  onBulkDeny,
  onBulkBan,
  onLogout,
}: ServerListViewProps) {
  const [showFilterModal, setShowFilterModal] = useState(false);
  const [selectedLeaseIds, setSelectedLeaseIds] = useState<Set<string>>(
    new Set()
  );
  const serverItems = filteredServers as ListServer[];
  const favoriteIds = useMemo(() => new Set(favorites), [favorites]);

  const handleToggleSelect = (leaseId: string) => {
    setSelectedLeaseIds((prev) => {
      const next = new Set(prev);
      if (next.has(leaseId)) {
        next.delete(leaseId);
      } else {
        next.add(leaseId);
      }
      return next;
    });
  };

  const handleClearSelection = () => {
    setSelectedLeaseIds(new Set());
  };

  const serverRows = useMemo(
    () =>
      serverItems.map((server) => ({
        server,
        adminServer: toAdminServer(server),
      })),
    [serverItems]
  );
  const featuredPublicRows = serverRows.slice(0, 3);
  const remainingPublicRows = serverRows.slice(3);

  const allLeaseIds = useMemo(
    () => [
      ...new Set(
        serverRows
        .map(({ adminServer }) => adminServer?.peerId)
          .filter(
            (leaseId): leaseId is string =>
              typeof leaseId === "string" && leaseId.trim().length > 0
          )
      ),
    ],
    [serverRows]
  );

  useEffect(() => {
    const validLeaseIDs = new Set(allLeaseIds);
    setSelectedLeaseIds((prev) => {
      if (prev.size === 0) {
        return prev;
      }

      const next = new Set<string>();
      prev.forEach((leaseId) => {
        if (validLeaseIDs.has(leaseId)) {
          next.add(leaseId);
        }
      });

      if (next.size === prev.size) {
        return prev;
      }

      return next;
    });
  }, [allLeaseIds]);

  useEffect(() => {
    if (isAdmin) {
      return;
    }
    setSelectedLeaseIds((prev) => (prev.size === 0 ? prev : new Set()));
  }, [isAdmin]);

  const isAllSelected =
    allLeaseIds.length > 0 &&
    allLeaseIds.every((id) => selectedLeaseIds.has(id));

  const handleSelectAll = () => {
    if (isAllSelected) {
      setSelectedLeaseIds(new Set());
    } else {
      setSelectedLeaseIds(new Set(allLeaseIds));
    }
  };

  const runBulkAction = async (
    handler?: (leaseIds: string[]) => void | Promise<void>
  ) => {
    if (!handler || selectedLeaseIds.size === 0) {
      return;
    }

    try {
      await handler(Array.from(selectedLeaseIds));
      handleClearSelection();
    } catch (err) {
      console.error("Failed bulk admin action", err);
    }
  };

  const handleBulkApprove = () => {
    void runBulkAction(onBulkApprove);
  };
  const handleBulkDeny = () => {
    void runBulkAction(onBulkDeny);
  };
  const handleBulkBan = () => {
    void runBulkAction(onBulkBan);
  };

  const adminFilterControls = (
    <>
      {onBanFilterChange && (
        <div className="flex items-center gap-3">
          <span className="text-sm font-medium text-text-muted">
            Ban Status
          </span>
          <BanStatusButtons
            banFilter={banFilter}
            onBanFilterChange={onBanFilterChange}
          />
        </div>
      )}
      {onApprovalModeChange && (
        <div className="flex items-center gap-3">
          <span className="text-sm font-medium text-text-muted">Approval</span>
          <ApprovalModeToggle
            approvalMode={approvalMode}
            onApprovalModeChange={onApprovalModeChange}
          />
        </div>
      )}
    </>
  );

  const renderServerCard = ({
    server,
    adminServer,
  }: {
    server: ListServer;
    adminServer?: AdminServer;
  }) => {
    const isSelected = adminServer
      ? selectedLeaseIds.has(adminServer.peerId)
      : false;

    return (
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
        firstSeen={server.firstSeen}
        isFavorite={favoriteIds.has(server.id)}
        onToggleFavorite={onToggleFavorite}
        showAdminControls={isAdmin && !!adminServer}
        leaseId={adminServer?.peerId}
        isBanned={adminServer?.isBanned}
        isApproved={adminServer?.isApproved}
        isDenied={adminServer?.isDenied}
        bps={adminServer?.bps}
        ip={adminServer?.ip}
        isIPBanned={adminServer?.isIPBanned}
        onBanStatusChange={onBanStatusChange}
        onBPSChange={onBPSChange}
        onApproveStatusChange={onApproveStatusChange}
        onDenyStatusChange={onDenyStatusChange}
        onIPBanStatusChange={onIPBanStatusChange}
        isSelected={isSelected}
        onToggleSelect={handleToggleSelect}
      />
    );
  };

  const gridClasses =
    "grid grid-cols-1 gap-6 p-4 min-[500px]:grid-cols-2 min-[500px]:p-6 md:grid-cols-3";

  const serverGrid = (
    <div className={gridClasses}>
      {serverRows.length > 0 ? (
        serverRows.map(renderServerCard)
      ) : (
        <div className="col-span-full py-12 text-center">
          <p className="text-lg text-text-muted">
            No servers match these filters
          </p>
        </div>
      )}
    </div>
  );

  const searchBar = (
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
      hideFiltersOnMobile={isAdmin}
      setShowFilterModal={isAdmin ? setShowFilterModal : undefined}
    />
  );

  return (
    <div className="relative flex h-auto min-h-screen w-full flex-col">
      <div className="flex h-full grow flex-col">
        <div className="flex flex-1 justify-center">
          <div className="flex w-full max-w-6xl flex-1 flex-col px-0 md:px-8">
            {isAdmin ? (
              <>
                <div className="sticky top-0 z-10 bg-background pb-4 pt-5">
                  <Header title={title} isAdmin={isAdmin} onLogout={onLogout} />
                  <div className="flex items-center gap-2">
                    <div className="flex-1">{searchBar}</div>
                  </div>
                  <div className="hidden sm:flex flex-wrap items-center gap-6 mt-4 px-4 sm:px-6">
                    {adminFilterControls}
                  </div>
                  {onApprovalModeChange && (
                    <div className="sm:hidden flex items-center gap-3 mt-4 px-4">
                      <span className="text-sm font-medium text-text-muted">
                        Approval
                      </span>
                      <ApprovalModeToggle
                        approvalMode={approvalMode}
                        onApprovalModeChange={onApprovalModeChange}
                      />
                    </div>
                  )}
                </div>
                <main className="z-0 flex-1">{serverGrid}</main>
              </>
            ) : (
              <>
                <div className="sticky top-0 z-20 bg-background/95 pt-5">
                  <Header
                    title={title}
                    isAdmin={isAdmin}
                    onLogout={onLogout}
                    ctaLabel="Get Started"
                  />
                </div>
                <main className="z-0 flex-1 px-4 pb-14 pt-6 sm:px-6">
                  <LandingHero />

                  <section
                    id="live-servers"
                    aria-labelledby="live-servers-title"
                    className="mt-8 border-t border-border pt-8"
                  >
                    <div className="space-y-2">
                      <p className="text-sm font-semibold uppercase tracking-[0.3em] text-primary">
                        Discovery
                      </p>
                      <h2
                        id="live-servers-title"
                        className="text-3xl font-semibold tracking-tight text-foreground"
                      >
                        Browse live apps
                      </h2>
                      <p className="max-w-2xl text-sm leading-6 text-text-muted">
                        Explore public services exposed through Portal.
                      </p>
                    </div>

                    {serverRows.length > 0 ? (
                      <>
                        <div className="mt-6">
                          <div className={gridClasses}>
                            {featuredPublicRows.map(renderServerCard)}
                          </div>
                        </div>

                        <div className="mt-4 border-t border-border pt-6">
                          {searchBar}
                          <div className="px-1 pt-3 text-sm text-text-muted">
                            {filteredServers.length.toLocaleString()} services
                            visible
                          </div>
                          {remainingPublicRows.length > 0 && (
                            <div className={gridClasses}>
                              {remainingPublicRows.map(renderServerCard)}
                            </div>
                          )}
                        </div>
                      </>
                    ) : (
                      <div className="mt-6">
                        {searchBar}
                        <div className="px-1 pt-3 text-sm text-text-muted">
                          0 services visible
                        </div>
                        <div className="py-12 text-center">
                          <p className="text-lg text-text-muted">
                            No servers match these filters
                          </p>
                        </div>
                      </div>
                    )}
                  </section>
                </main>
              </>
            )}
          </div>
        </div>
      </div>

      {isAdmin && (
        <Dialog open={showFilterModal} onOpenChange={setShowFilterModal}>
          <DialogContent className="sm:hidden max-w-sm rounded-sm">
            <DialogHeader>
              <DialogTitle>Filters</DialogTitle>
            </DialogHeader>
            <div className="flex flex-col gap-4">
              <div className="flex flex-col gap-2">
                <span className="text-sm font-medium text-text-muted">
                  Status
                </span>
                <StatusSelect
                  status={status}
                  onStatusChange={onStatusChange}
                  className="w-full!"
                />
              </div>
              {onBanFilterChange && (
                <div className="flex flex-col gap-2">
                  <span className="text-sm font-medium text-text-muted">
                    Ban Status
                  </span>
                  <BanStatusButtons
                    className="[&>button]:w-full"
                    banFilter={banFilter}
                    onBanFilterChange={onBanFilterChange}
                  />
                </div>
              )}
              <div className="flex flex-col gap-2">
                <span className="text-sm font-medium text-text-muted">Sort</span>
                <SortbySelect
                  className="w-full!"
                  sortBy={sortBy}
                  onSortByChange={onSortByChange}
                />
              </div>
              <div className="flex flex-col gap-2">
                <span className="text-sm font-medium text-text-muted">Tags</span>
                <TagCombobox
                  availableTags={availableTags}
                  selectedTags={selectedTags}
                  onAdd={onTagToggle}
                  onRemove={onTagToggle}
                />
              </div>
            </div>
          </DialogContent>
        </Dialog>
      )}

      {isAdmin && (
        <FloatingActionBar
          selectedCount={selectedLeaseIds.size}
          totalCount={allLeaseIds.length}
          isAllSelected={isAllSelected}
          onSelectAll={handleSelectAll}
          onApprove={handleBulkApprove}
          onDeny={handleBulkDeny}
          onBan={handleBulkBan}
        />
      )}
    </div>
  );
}
