import { SsgoiTransition } from "@ssgoi/react";
import { useAdmin } from "@/hooks/useAdmin";
import { ServerListView } from "@/components/ServerListView";

export function Admin() {
  const {
    filteredServers,
    availableTags,
    searchQuery,
    status,
    sortBy,
    selectedTags,
    banFilter,
    favorites,
    loading,
    error,
    handleSearchChange,
    handleStatusChange,
    handleSortByChange,
    handleTagToggle,
    handleBanFilterChange,
    handleToggleFavorite,
    handleBanStatus,
    handleBPSChange,
  } = useAdmin();

  if (loading) return <div className="p-8 text-foreground">Loading...</div>;
  if (error) return <div className="p-8 text-red-500">Error: {error}</div>;

  return (
    <SsgoiTransition id="admin">
      <ServerListView
        title="PORTAL ADMIN"
        searchQuery={searchQuery}
        status={status}
        sortBy={sortBy}
        selectedTags={selectedTags}
        availableTags={availableTags}
        filteredServers={filteredServers}
        favorites={favorites}
        onSearchChange={handleSearchChange}
        onStatusChange={handleStatusChange}
        onSortByChange={handleSortByChange}
        onTagToggle={handleTagToggle}
        onToggleFavorite={handleToggleFavorite}
        // Admin-specific props
        isAdmin={true}
        banFilter={banFilter}
        onBanFilterChange={handleBanFilterChange}
        onBanStatusChange={handleBanStatus}
        onBPSChange={handleBPSChange}
      />
    </SsgoiTransition>
  );
}
