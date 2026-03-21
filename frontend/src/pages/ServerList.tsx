import { SsgoiTransition } from "@ssgoi/react";
import { useServerList } from "@/hooks/useServerList";
import { ServerListView } from "@/components/ServerListView";

const LANDING_PAGE_ENABLED_META_NAME = "portal-landing-page-enabled";

function readLandingPageEnabled(doc?: Document): boolean {
  const targetDoc =
    doc ?? (typeof document !== "undefined" ? document : undefined);
  if (!targetDoc) {
    return true;
  }

  const value =
    targetDoc
      .querySelector<HTMLMetaElement>(
        `meta[name="${LANDING_PAGE_ENABLED_META_NAME}"]`
      )
      ?.content.trim()
      .toLowerCase() || "";

  return value !== "false" && value !== "0" && value !== "no";
}

export function ServerList() {
  // Controller: useServerList hook handles all server list logic
  const {
    searchQuery,
    status,
    sortBy,
    selectedTags,
    availableTags,
    filteredServers,
    favorites,
    handleSearchChange,
    handleStatusChange,
    handleSortByChange,
    handleTagToggle,
    handleToggleFavorite,
  } = useServerList();
  const landingPageEnabled = readLandingPageEnabled();

  return (
    <SsgoiTransition id="/">
      <ServerListView
        landingPageEnabled={landingPageEnabled}
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
      />
    </SsgoiTransition>
  );
}
