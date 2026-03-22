import { useEffect, useMemo, useState } from "react";
import { Code, Search } from "lucide-react";
import { Header } from "@/components/Header";
import { LandingHero } from "@/components/LandingHero";
import { SearchBar } from "@/components/SearchBar";
import { ServerCard } from "@/components/ServerCard";
import { TagCombobox } from "@/components/TagCombobox";
import type { ClientServer } from "@/hooks/useServerList";
import type { AdminServer, ApprovalMode, UDPSettings } from "@/hooks/useAdmin";
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

interface OfficialRegistryDocument {
  relays?: string[];
}

const OFFICIAL_REGISTRY_URL =
  "https://raw.githubusercontent.com/gosuda/portal/main/registry.json";

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
  udpSettings?: UDPSettings;
  onUDPSettingsChange?: (settings: UDPSettings) => void | Promise<void>;
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
  udpSettings,
  onUDPSettingsChange,
  onApproveStatusChange,
  onDenyStatusChange,
  onIPBanStatusChange,
  onBulkApprove,
  onBulkDeny,
  onBulkBan,
  onLogout,
}: ServerListViewProps) {
  const [showFilterModal, setShowFilterModal] = useState(false);
  const [officialRegistryRelays, setOfficialRegistryRelays] = useState<string[] | null>(null);
  const [officialRegistryFailed, setOfficialRegistryFailed] = useState(false);
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

  useEffect(() => {
    if (isAdmin) {
      return;
    }

    let cancelled = false;

    const loadOfficialRegistry = async () => {
      try {
        const response = await fetch(OFFICIAL_REGISTRY_URL, {
          headers: { Accept: "application/json" },
        });
        if (!response.ok) {
          throw new Error(`registry request failed with status ${response.status}`);
        }
        const document = (await response.json()) as OfficialRegistryDocument;
        if (cancelled) {
          return;
        }

        setOfficialRegistryFailed(false);
        setOfficialRegistryRelays(
          Array.isArray(document.relays)
            ? document.relays.filter(
                (relay): relay is string =>
                  typeof relay === "string" && relay.trim().length > 0
              )
            : []
        );
      } catch (error) {
        if (!cancelled) {
          console.error("Failed to load official registry", error);
          setOfficialRegistryFailed(true);
          setOfficialRegistryRelays([]);
        }
      }
    };

    void loadOfficialRegistry();

    return () => {
      cancelled = true;
    };
  }, [isAdmin]);

  const isAllSelected =
    allLeaseIds.length > 0 &&
    allLeaseIds.every((id) => selectedLeaseIds.has(id));
  const officialRegistryURL = OFFICIAL_REGISTRY_URL;
  const officialRegistryAvailable =
    officialRegistryRelays !== null && officialRegistryRelays.length > 0;

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

  const [maxLeasesInput, setMaxLeasesInput] = useState(
    String(udpSettings?.maxLeases ?? 0)
  );

  useEffect(() => {
    setMaxLeasesInput(String(udpSettings?.maxLeases ?? 0));
  }, [udpSettings?.maxLeases]);

  const handleUDPToggle = (enabled: boolean) => {
    if (onUDPSettingsChange && udpSettings) {
      void onUDPSettingsChange({ ...udpSettings, enabled });
    }
  };

  const handleMaxLeasesSave = () => {
    if (onUDPSettingsChange && udpSettings) {
      const value = Math.max(0, parseInt(maxLeasesInput, 10) || 0);
      setMaxLeasesInput(String(value));
      void onUDPSettingsChange({ ...udpSettings, maxLeases: value });
    }
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
      {onUDPSettingsChange && udpSettings && (
        <>
          <div className="flex items-center gap-3">
            <span className="text-sm font-medium text-text-muted">UDP</span>
            <div className="flex rounded-lg overflow-hidden border border-foreground/20">
              <button
                onClick={() => handleUDPToggle(false)}
                className={`cursor-pointer px-4 h-10 text-sm font-medium transition-colors ${
                  !udpSettings.enabled
                    ? "bg-primary text-primary-foreground"
                    : "bg-secondary text-secondary-foreground hover:bg-secondary/80"
                }`}
              >
                Disabled
              </button>
              <button
                onClick={() => handleUDPToggle(true)}
                className={`cursor-pointer px-4 h-10 text-sm font-medium transition-colors border-l border-foreground/20 ${
                  udpSettings.enabled
                    ? "bg-primary text-primary-foreground"
                    : "bg-secondary text-secondary-foreground hover:bg-secondary/80"
                }`}
              >
                Enabled
              </button>
            </div>
          </div>
          <div className="flex items-center gap-3">
            <span className="text-sm font-medium text-text-muted">Max UDP</span>
            <div className="flex items-center gap-2">
              <input
                type="number"
                min="0"
                value={maxLeasesInput}
                onChange={(e) => setMaxLeasesInput(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter") handleMaxLeasesSave();
                }}
                className="w-20 h-10 px-3 text-sm border border-foreground/20 rounded-lg bg-secondary text-foreground"
                placeholder="0"
              />
              <button
                onClick={handleMaxLeasesSave}
                className="cursor-pointer h-10 px-4 text-sm font-medium rounded-lg bg-primary text-primary-foreground hover:bg-primary/90 transition-colors"
              >
                Save
              </button>
            </div>
          </div>
        </>
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
        transport={server.transport}
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
        {isAdmin ? (
          <div className="flex flex-1 justify-center">
            <div className="flex w-full max-w-6xl flex-1 flex-col px-0 md:px-8">
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
            </div>
          </div>
        ) : (
          <>
            <Header title={title} isAdmin={isAdmin} onLogout={onLogout} />

            <main className="pb-20">
              <LandingHero />

              {/* Live Apps Section — sample.html line 146-284 */}
              <section className="max-w-7xl mx-auto px-6 mb-32 scroll-mt-24" id="live-servers">
                <div className="mb-12 space-y-4">
                  <div className="flex flex-col md:flex-row md:items-end justify-between gap-6">
                    <div>
                      <h2 className="text-3xl font-extrabold tracking-tight mb-2">Browse live apps</h2>
                      <p className="text-text-muted">Explore public tunnels currently active on the network.</p>
                    </div>
                    <div className="flex flex-col gap-2">
                      <div className="flex flex-wrap gap-3">
                        {/* search pill — replaces mockup line 153-158 static input */}
                        <div className="bg-secondary px-4 py-2 rounded-xl flex items-center gap-2 border border-border/10">
                          <Search className="text-text-muted h-4.5 w-4.5" />
                          <input
                            className="bg-transparent border-none p-0 text-sm w-32 text-foreground outline-none placeholder:text-text-muted"
                            placeholder="Search apps..."
                            type="text"
                            value={searchQuery}
                            onChange={(e) => onSearchChange(e.target.value)}
                          />
                        </div>
                        {/* status select — replaces mockup line 159-163 static button */}
                        <StatusSelect
                          status={status}
                          onStatusChange={onStatusChange}
                        />
                        {/* sort select — replaces mockup line 164-168 static button */}
                        <SortbySelect
                          sortBy={sortBy}
                          onSortByChange={onSortByChange}
                        />
                      </div>
                      {/* tag combobox — replaces mockup line 169-173 static button */}
                      <TagCombobox
                        availableTags={availableTags}
                        selectedTags={selectedTags}
                        onAdd={onTagToggle}
                        onRemove={onTagToggle}
                      />
                    </div>
                  </div>
                </div>
                {/* App Grid (Bento Style) — sample.html line 177, cards replaced with dynamic ServerCard */}
                <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-6">
                  {serverRows.length > 0 ? (
                    serverRows.map(renderServerCard)
                  ) : (
                    <div className="col-span-full py-12 text-center">
                      <p className="text-lg text-text-muted">No servers match these filters</p>
                    </div>
                  )}
                </div>
              </section>

              {/* Official Registry Section — sample.html line 286-348 그대로 */}
              <section className="max-w-7xl mx-auto px-6 mb-32 scroll-mt-24" id="official-registry">
                <div className="bg-secondary rounded-xl p-8 md:p-12 border border-border/10 relative overflow-hidden">
                  {/* Decoration */}
                  <div
                    className="absolute top-0 right-0 w-64 h-64 opacity-10 blur-[100px] pointer-events-none"
                    style={{ background: "linear-gradient(to right, #00FFFF, #FFA500, #FF00FF)" }}
                  />
                  <div className="flex flex-col md:flex-row justify-between items-start md:items-center gap-8 mb-12">
                    <div>
                      <h2 className="text-3xl font-extrabold tracking-tight mb-2">Official registry</h2>
                      <p className="text-text-muted">Trusted public relays provided by the community.</p>
                    </div>
                    <a
                      className="flex items-center gap-2 text-sm font-bold text-primary px-4 py-2 rounded-xl bg-primary/10 hover:bg-primary/20 transition-all"
                      href={officialRegistryURL}
                      target="_blank"
                      rel="noopener noreferrer"
                    >
                      <Code className="h-4.5 w-4.5" />
                      Open registry.json
                    </a>
                  </div>
                  <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-y-4 gap-x-12">
                    {officialRegistryRelays === null && !officialRegistryFailed ? (
                      <p className="text-sm text-text-muted">Loading official registry...</p>
                    ) : officialRegistryAvailable && officialRegistryRelays.length > 0 ? (
                      officialRegistryRelays.map((relay) => (
                        <a
                          key={relay}
                          href={relay}
                          target="_blank"
                          rel="noopener noreferrer"
                          className="flex items-center justify-between p-4 rounded-lg bg-card border border-border/5 hover:border-primary/30 transition-colors group"
                        >
                          <span className="text-sm font-mono text-text-muted group-hover:text-primary transition-colors">
                            {relay}
                          </span>
                          <span className="text-[10px] px-2 py-0.5 rounded bg-green-status/10 text-green-status font-bold">
                            STABLE
                          </span>
                        </a>
                      ))
                    ) : (
                      <p className="text-sm text-text-muted">Registry entries are unavailable right now.</p>
                    )}
                  </div>
                </div>
              </section>
            </main>

            {/* Footer — sample.html line 351-365 그대로 */}
            <footer className="w-full py-12 border-t border-border/15 bg-background">
              <div className="flex flex-col md:flex-row justify-between items-center px-8 max-w-7xl mx-auto gap-4">
                <div className="flex flex-col gap-2">
                  <span className="text-lg font-bold text-primary">{title}</span>
                  <p className="text-sm text-text-muted">Self-hosted HTTP/TCP/UDP tunneling relay. Open source.</p>
                </div>
                <div className="flex gap-8">
                  <a className="text-sm text-text-muted hover:text-foreground transition-colors" href="https://github.com/gosuda/portal" target="_blank" rel="noopener noreferrer">Documentation</a>
                  <a className="text-sm text-text-muted hover:text-foreground transition-colors" href="https://github.com/gosuda/portal" target="_blank" rel="noopener noreferrer">Status</a>
                  <a className="text-sm text-text-muted hover:text-foreground transition-colors" href="https://github.com/gosuda/portal" target="_blank" rel="noopener noreferrer">Privacy</a>
                  <a className="text-sm text-text-muted hover:text-foreground transition-colors" href="https://github.com/gosuda/portal" target="_blank" rel="noopener noreferrer">Terms</a>
                </div>
              </div>
            </footer>
          </>
        )}
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
