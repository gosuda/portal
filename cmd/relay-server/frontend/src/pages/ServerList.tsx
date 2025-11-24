import { useEffect, useMemo, useRef, useState } from "react";
import { Header } from "@/components/Header";
import { SearchBar } from "@/components/SearchBar";
import { ServerCard } from "@/components/ServerCard";
import { TagFilter } from "@/components/TagFilter";
import { useSSRData } from "@/hooks/useSSRData";
import type { ServerData, Metadata } from "@/hooks/useSSRData";
import { SsgoiTransition } from "@ssgoi/react";
import type { SortOption, StatusFilter, TagMode } from "@/types/filters";

const INITIAL_VISIBLE = 12;
const LOAD_CHUNK = 6;

type ClientServer = {
  id: number;
  name: string;
  description: string;
  tags: string[];
  thumbnail: string;
  owner: string;
  online: boolean;
  dns: string;
  link: string;
  lastUpdated?: string;
};

// Helper function to convert SSR ServerData to frontend format
function convertSSRDataToServers(ssrData: ServerData[]): ClientServer[] {
  return ssrData.map((row, index) => {
    // Parse metadata JSON string
    let metadata: Metadata = {
      description: "",
      tags: [],
      thumbnail: "",
      owner: "",
      hide: false,
    };

    try {
      if (row.Metadata) {
        metadata = JSON.parse(row.Metadata);
      }
    } catch (err) {
      console.error("[App] Failed to parse metadata:", err, row.Metadata);
    }

    const normalizedTags = Array.isArray(metadata.tags)
      ? metadata.tags
          .map((tag) => (typeof tag === "string" ? tag.trim() : ""))
          .filter(Boolean)
      : [];

    return {
      id: index + 1,
      name: row.Name || row.DNS || "(unnamed)",
      description: metadata.description || "",
      tags: normalizedTags,
      thumbnail: metadata.thumbnail || "",
      owner: metadata.owner || "",
      online: row.Connected,
      dns: row.DNS || "",
      link: row.Link,
      lastUpdated: row.LastSeenISO || row.LastSeen || undefined,
    };
  });
}

export function ServerList() {
  const [searchQuery, setSearchQuery] = useState("");
  const [status, setStatus] = useState<StatusFilter>("all");
  const [sortBy, setSortBy] = useState<SortOption>("default");
  const [selectedTags, setSelectedTags] = useState<string[]>([]);
  const [tagMode, setTagMode] = useState<TagMode>("OR");
  const [visibleCount, setVisibleCount] = useState(INITIAL_VISIBLE);

  const sentinelRef = useRef<HTMLDivElement | null>(null);
  const observerRef = useRef<IntersectionObserver | null>(null);

  // Get SSR data
  const ssrData = useSSRData();

  // Use SSR data if available, otherwise fall back to sample servers
  const servers: ClientServer[] = useMemo(() => {
    console.log("[App] SSR data length:", ssrData.length);
    if (ssrData.length > 0) {
      console.log("[App] Using SSR data");
      const converted = convertSSRDataToServers(ssrData);
      console.log("[App] Converted servers:", converted);
      return converted;
    }
    console.log("[App] Using sample servers");
    return [];
  }, [ssrData]);

  const availableTags = useMemo(() => {
    const counts = new Map<string, number>();
    servers.forEach((server) => {
      server.tags.forEach((tag) => {
        counts.set(tag, (counts.get(tag) || 0) + 1);
      });
    });
    return Array.from(counts.entries()).sort((a, b) => b[1] - a[1]).map(([tag]) => tag);
  }, [servers]);

  // Filter and sort servers
  const filteredServers = useMemo(() => {
    const query = searchQuery.toLowerCase();

    const matchesTags = (server: ClientServer) => {
      if (selectedTags.length === 0) return true;
      const tagsLower = server.tags.map((t) => t.toLowerCase());
      if (tagMode === "AND") {
        return selectedTags.every((tag) => tagsLower.includes(tag.toLowerCase()));
      }
      return selectedTags.some((tag) => tagsLower.includes(tag.toLowerCase()));
    };

    const filtered = servers.filter((server) => {
      const matchesSearch =
        query === "" ||
        server.name.toLowerCase().includes(query) ||
        server.description.toLowerCase().includes(query) ||
        server.tags.some((tag) => tag.toLowerCase().includes(query));

      const matchesStatus =
        status === "all" ||
        (status === "online" && server.online) ||
        (status === "offline" && !server.online);

      return matchesSearch && matchesStatus && matchesTags(server);
    });

    const sorted = [...filtered];
    switch (sortBy) {
      case "name-asc":
        sorted.sort((a, b) => a.name.localeCompare(b.name));
        break;
      case "name-desc":
        sorted.sort((a, b) => b.name.localeCompare(a.name));
        break;
      case "updated":
        sorted.sort((a, b) => {
          const aTime = a.lastUpdated ? Date.parse(a.lastUpdated) : 0;
          const bTime = b.lastUpdated ? Date.parse(b.lastUpdated) : 0;
          return bTime - aTime;
        });
        break;
      case "description":
        sorted.sort((a, b) => a.description.localeCompare(b.description));
        break;
      case "tags":
        sorted.sort((a, b) => {
          const aTag = a.tags[0] || "";
          const bTag = b.tags[0] || "";
          return aTag.localeCompare(bTag);
        });
        break;
      case "owner":
        sorted.sort((a, b) => a.owner.localeCompare(b.owner));
        break;
      default:
        break;
    }

    return sorted;
  }, [servers, searchQuery, status, sortBy, selectedTags, tagMode]);

  const visibleServers = useMemo(
    () => filteredServers.slice(0, visibleCount),
    [filteredServers, visibleCount]
  );

  const hasMore = visibleCount < filteredServers.length;

  // Reset visible items when filters change
  useEffect(() => {
    setVisibleCount(INITIAL_VISIBLE);
  }, [searchQuery, status, sortBy, selectedTags, tagMode]);

  useEffect(() => {
    if (!sentinelRef.current) return;

    // Disconnect any existing observer before creating a new one
    if (observerRef.current) {
      observerRef.current.disconnect();
    }

    const observer = new IntersectionObserver(
      (entries) => {
        const entry = entries[0];
        if (entry.isIntersecting && hasMore) {
          setVisibleCount((count) => count + LOAD_CHUNK);
        }
      },
      { rootMargin: "200px 0px" }
    );

    observer.observe(sentinelRef.current);
    observerRef.current = observer;

    return () => observer.disconnect();
  }, [hasMore]);

  const handleSearchChange = (value: string) => {
    setSearchQuery(value);
  };

  const handleStatusChange = (value: StatusFilter) => {
    setStatus(value);
  };

  const handleSortByChange = (value: SortOption) => {
    setSortBy(value);
  };

  const handleTagToggle = (tag: string) => {
    setSelectedTags((prev) =>
      prev.includes(tag) ? prev.filter((t) => t !== tag) : [...prev, tag]
    );
  };

  const handleClearTags = () => setSelectedTags([]);

  return (
    <SsgoiTransition id="/">
      <div className="relative flex h-auto min-h-screen w-full flex-col overflow-x-hidden">
        <div className="flex h-full grow flex-col">
          <div className="flex flex-1 justify-center py-5">
            <div className="flex flex-col w-full max-w-6xl flex-1 px-4 md:px-8">
              <Header />
              <main className="flex-1 mt-6">
                <SearchBar
                  searchQuery={searchQuery}
                  onSearchChange={handleSearchChange}
                  status={status}
                  onStatusChange={handleStatusChange}
                  sortBy={sortBy}
                  onSortByChange={handleSortByChange}
                />
                {availableTags.length > 0 && (
                  <div className="mt-4 px-4 sm:px-6">
                    <TagFilter
                      availableTags={availableTags}
                      selectedTags={selectedTags}
                      mode={tagMode}
                      onModeChange={setTagMode}
                      onToggleTag={handleTagToggle}
                      onClear={handleClearTags}
                    />
                  </div>
                )}
                <div className="grid grid-cols-1 min-[500px]:grid-cols-2 md:grid-cols-3 gap-6 p-4 min-[500px]:p-6 mt-4">
                  {visibleServers.length > 0 ? (
                    visibleServers.map((server) => (
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
                        navigationPath={`/server/${server.id}`}
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
                <div ref={sentinelRef} className="h-8 w-full" />
                {!hasMore && filteredServers.length > 0 && (
                  <div className="px-4 sm:px-6 pb-10 text-center text-sm text-text-muted">
                    You have reached the end of the list.
                  </div>
                )}
              </main>
            </div>
          </div>
        </div>
      </div>
    </SsgoiTransition>
  );
}
