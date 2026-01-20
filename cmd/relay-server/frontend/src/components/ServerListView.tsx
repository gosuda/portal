import { useState } from "react";
import { Header } from "@/components/Header";
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
  approvalMode?: ApprovalMode;
  onBanFilterChange?: (value: BanFilter) => void;
  onBanStatusChange?: (leaseId: string, isBan: boolean) => void;
  onBPSChange?: (leaseId: string, bps: number) => void;
  onApprovalModeChange?: (mode: ApprovalMode) => void;
  onApproveStatusChange?: (leaseId: string, approve: boolean) => void;
  onDenyStatusChange?: (leaseId: string, deny: boolean) => void;
  onIPBanStatusChange?: (ip: string, isBan: boolean) => void;
  // Bulk action handlers
  onBulkApprove?: (leaseIds: string[]) => void;
  onBulkDeny?: (leaseIds: string[]) => void;
  onBulkBan?: (leaseIds: string[]) => void;
  // Logout handler (admin only)
  onLogout?: () => void;
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
  approvalMode = "auto",
  onBanFilterChange,
  onBanStatusChange,
  onBPSChange,
  onApprovalModeChange,
  onApproveStatusChange,
  onDenyStatusChange,
  onIPBanStatusChange,
  // Bulk action handlers
  onBulkApprove,
  onBulkDeny,
  onBulkBan,
  // Logout handler
  onLogout,
}: ServerListViewProps) {
  const [showFilterModal, setShowFilterModal] = useState(false);
  const [selectedLeaseIds, setSelectedLeaseIds] = useState<Set<string>>(
    new Set()
  );

  // Toggle selection for a single card
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

  // Clear all selections
  const handleClearSelection = () => {
    setSelectedLeaseIds(new Set());
  };

  // Get all selectable lease IDs from filtered servers
  const allLeaseIds = (filteredServers as (ClientServer | AdminServer)[])
    .filter(isAdminServer)
    .map((server) => server.peerId);

  // Check if all items are selected
  const isAllSelected =
    allLeaseIds.length > 0 &&
    allLeaseIds.every((id) => selectedLeaseIds.has(id));

  // Select all / Deselect all
  const handleSelectAll = () => {
    if (isAllSelected) {
      setSelectedLeaseIds(new Set());
    } else {
      setSelectedLeaseIds(new Set(allLeaseIds));
    }
  };

  // Bulk action handlers
  const handleBulkApprove = () => {
    if (onBulkApprove && selectedLeaseIds.size > 0) {
      onBulkApprove(Array.from(selectedLeaseIds));
      handleClearSelection();
    }
  };

  const handleBulkDeny = () => {
    if (onBulkDeny && selectedLeaseIds.size > 0) {
      onBulkDeny(Array.from(selectedLeaseIds));
      handleClearSelection();
    }
  };

  const handleBulkBan = () => {
    if (onBulkBan && selectedLeaseIds.size > 0) {
      onBulkBan(Array.from(selectedLeaseIds));
      handleClearSelection();
    }
  };

  // Admin filter content (Ban Status + Approval) - for desktop only
  const AdminFilterContent = () => (
    <>
      {/* Ban Status Filter Buttons */}
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

      {/* Approval Mode Toggle */}
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

  return (
    <div className="relative flex h-auto min-h-screen w-full flex-col">
      <div className="flex h-full grow flex-col">
        <div className="flex flex-1 justify-center">
          <div className="flex flex-col w-full max-w-6xl flex-1 px-0 md:px-8">
            <div className="sticky top-0 z-10 bg-background pb-4 pt-5">
              <Header title={title} isAdmin={isAdmin} onLogout={onLogout} />
              <div className="flex items-center gap-2">
                <div className="flex-1">
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
                    setShowFilterModal={setShowFilterModal}
                  />
                </div>
              </div>
              {/* Desktop filters - hidden on mobile */}
              {isAdmin && (
                <div className="hidden sm:flex flex-wrap items-center gap-6 mt-4 px-4 sm:px-6">
                  <AdminFilterContent />
                </div>
              )}
              {/* Mobile-only Approval filter - always visible outside modal */}
              {isAdmin && onApprovalModeChange && (
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
            <main className="flex-1 z-0">
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
                      firstSeen={server.firstSeen}
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
                      isApproved={
                        isAdminServer(server) ? server.isApproved : undefined
                      }
                      isDenied={
                        isAdminServer(server) ? server.isDenied : undefined
                      }
                      bps={isAdminServer(server) ? server.bps : undefined}
                      ip={isAdminServer(server) ? server.ip : undefined}
                      isIPBanned={
                        isAdminServer(server) ? server.isIPBanned : undefined
                      }
                      onBanStatusChange={onBanStatusChange}
                      onBPSChange={onBPSChange}
                      onApproveStatusChange={onApproveStatusChange}
                      onDenyStatusChange={onDenyStatusChange}
                      onIPBanStatusChange={onIPBanStatusChange}
                      // Selection for bulk actions
                      isSelected={
                        isAdminServer(server)
                          ? selectedLeaseIds.has(server.peerId)
                          : false
                      }
                      onToggleSelect={handleToggleSelect}
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

      {/* Filter Modal for mobile - contains SearchBar filters (Status, Sort, Tag) */}
      <Dialog open={showFilterModal} onOpenChange={setShowFilterModal}>
        <DialogContent className="sm:hidden max-w-sm rounded-sm">
          <DialogHeader>
            <DialogTitle>Filters</DialogTitle>
          </DialogHeader>
          <div className="flex flex-col gap-4">
            {/* Online/Offline Status Filter - Select style */}
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

            {/* Admin Ban Status Filter - All/Active/Banned (button group style) */}
            {isAdmin && onBanFilterChange && (
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

            {/* Sort By */}
            <div className="flex flex-col gap-2">
              <span className="text-sm font-medium text-text-muted">
                Sort By
              </span>
              <SortbySelect
                className="w-full!"
                sortBy={sortBy}
                onSortByChange={onSortByChange}
              />
            </div>

            {/* Tag Filter */}
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

      {/* Floating Action Bar - shows when items are selected in admin mode */}
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
