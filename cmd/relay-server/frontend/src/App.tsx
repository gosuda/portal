import { useState, useMemo } from "react";
import { Header } from "@/components/Header";
import { SearchBar } from "@/components/SearchBar";
import { ServerCard } from "@/components/ServerCard";
import { TagList } from "@/components/TagList";
import { useSSRData } from "@/hooks/useSSRData";
import { useIntersectionObserver } from "@/hooks/useIntersectionObserver";
import type { ServerData, Metadata } from "@/hooks/useSSRData";

const ITEMS_PER_BATCH = 12;

// Helper function to convert SSR ServerData to frontend format
function convertSSRDataToServers(ssrData: ServerData[]) {
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

    return {
      id: index + 1,
      name: row.Name || row.DNS || "(unnamed)",
      description: metadata.description || "",
      tags: Array.isArray(metadata.tags) ? metadata.tags : [],
      thumbnail: metadata.thumbnail || "",
      owner: metadata.owner || "",
      online: row.Connected,
      dns: row.DNS || "",
      link: row.Link,
    };
  });
}

function App() {
  const [visibleCount, setVisibleCount] = useState(ITEMS_PER_BATCH);
  const [searchQuery, setSearchQuery] = useState("");
  const [status, setStatus] = useState("all");
  const [sortBy, setSortBy] = useState("default");
  const [selectedTags, setSelectedTags] = useState<string[]>([]);

  // Get SSR data
  const ssrData = useSSRData();

  // Use SSR data if available, otherwise fall back to sample servers
  const servers = useMemo(() => {
    if (ssrData.length > 0) {
      return convertSSRDataToServers(ssrData);
    }
    return [];
  }, [ssrData]);

  // Extract all available tags from servers
  const allTags = useMemo(() => {
    const tags = new Set<string>();
    servers.forEach((server) => {
      server.tags.forEach((tag) => tags.add(tag));
    });
    return Array.from(tags).sort();
  }, [servers]);

  // Filter available tags (exclude already selected ones)
  const availableTags = useMemo(() => {
    return allTags.filter((tag) => !selectedTags.includes(tag));
  }, [allTags, selectedTags]);

  // Filter and sort servers
  const filteredServers = useMemo(() => {
    let filtered = servers.filter((server) => {
      // Search filter - searches name, description, and tags
      const matchesSearch =
        searchQuery === "" ||
        server.name.toLowerCase().includes(searchQuery.toLowerCase()) ||
        server.description.toLowerCase().includes(searchQuery.toLowerCase()) ||
        server.tags.some((tag) =>
          tag.toLowerCase().includes(searchQuery.toLowerCase())
        );

      // Status filter
      const matchesStatus =
        status === "all" ||
        (status === "online" && server.online) ||
        (status === "offline" && !server.online);

      // Tags filter (AND logic: must have all selected tags)
      const matchesTags =
        selectedTags.length === 0 ||
        selectedTags.every((tag) => server.tags.includes(tag));

      return matchesSearch && matchesStatus && matchesTags;
    });

    // Sort based on sortBy value
    if (sortBy === "description") {
      filtered = [...filtered].sort((a, b) =>
        a.description.localeCompare(b.description)
      );
    } else if (sortBy === "tags") {
      filtered = [...filtered].sort((a, b) => {
        const aTag = a.tags[0] || "";
        const bTag = b.tags[0] || "";
        return aTag.localeCompare(bTag);
      });
    } else if (sortBy === "owner") {
      filtered = [...filtered].sort((a, b) => a.owner.localeCompare(b.owner));
    } else if (sortBy === "name-asc") {
      filtered = [...filtered].sort((a, b) => a.name.localeCompare(b.name));
    } else if (sortBy === "name-desc") {
      filtered = [...filtered].sort((a, b) => b.name.localeCompare(a.name));
    }

    return filtered;
  }, [servers, searchQuery, status, sortBy, selectedTags]);

  // Infinite scroll data
  const visibleServers = useMemo(() => {
    return filteredServers.slice(0, visibleCount);
  }, [filteredServers, visibleCount]);

  // Intersection Observer for Infinite Scroll
  const { elementRef: loadMoreRef } = useIntersectionObserver({
    onIntersect: () => {
      if (visibleCount < filteredServers.length) {
        setVisibleCount((prev) => prev + ITEMS_PER_BATCH);
      }
    },
    enabled: visibleCount < filteredServers.length,
    threshold: 0.1,
    rootMargin: "100px",
  });

  // Reset visible count when filters change
  const handleFilterChange = (
    updater: (prev: any) => any,
    value: any
  ) => {
    updater(value);
    setVisibleCount(ITEMS_PER_BATCH);
  };

  const handleTagSelect = (tag: string) => {
    if (!selectedTags.includes(tag)) {
      setSelectedTags([...selectedTags, tag]);
      setVisibleCount(ITEMS_PER_BATCH);
    }
  };

  const handleRemoveTag = (tag: string) => {
    setSelectedTags(selectedTags.filter((t) => t !== tag));
    setVisibleCount(ITEMS_PER_BATCH);
  };

  return (
    <div className="relative flex h-auto min-h-screen w-full flex-col overflow-x-hidden">
      <div className="layout-container flex h-full grow flex-col">
        <div className="flex flex-1 justify-center py-5">
          <div className="layout-content-container flex flex-col w-full max-w-6xl flex-1 px-4 md:px-8">
            <Header />
            <main className="flex-1 mt-6">
              <SearchBar
                searchQuery={searchQuery}
                onSearchChange={(val) => handleFilterChange(setSearchQuery, val)}
                status={status}
                onStatusChange={(val) => handleFilterChange(setStatus, val)}
                sortBy={sortBy}
                onSortByChange={(val) => handleFilterChange(setSortBy, val)}
                availableTags={availableTags}
                onTagSelect={handleTagSelect}
              />
              
              <TagList tags={selectedTags} onRemoveTag={handleRemoveTag} />

              <div className="grid grid-cols-1 min-[500px]:grid-cols-2 md:grid-cols-3 gap-6 p-4 min-[500px]:p-6 mt-4">
                {visibleServers.length > 0 ? (
                  visibleServers.map((server) => (
                    <ServerCard
                      key={server.id}
                      name={server.name}
                      description={server.description}
                      tags={server.tags}
                      thumbnail={server.thumbnail}
                      owner={server.owner}
                      online={server.online}
                      dns={server.dns}
                      serverUrl={server.link}
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
              
              {/* Infinite Scroll Trigger */}
              {visibleCount < filteredServers.length && (
                <div 
                  ref={loadMoreRef} 
                  className="h-20 w-full flex justify-center items-center"
                >
                  <span className="text-text-muted text-sm">Loading more...</span>
                </div>
              )}
            </main>
          </div>
        </div>
      </div>
    </div>
  );
}

export default App;