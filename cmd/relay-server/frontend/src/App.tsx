import { useState, useMemo } from "react";
import { Header } from "@/components/Header";
import { SearchBar } from "@/components/SearchBar";
import { ServerCard } from "@/components/ServerCard";
import { Pagination } from "@/components/Pagination";
import { useSSRData } from "@/hooks/useSSRData";
import type { ServerData, Metadata } from "@/hooks/useSSRData";

const ITEMS_PER_PAGE = 6;

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
  const [currentPage, setCurrentPage] = useState(1);
  const [searchQuery, setSearchQuery] = useState("");
  const [status, setStatus] = useState("all");
  const [sortBy, setSortBy] = useState("default");

  // Get SSR data
  const ssrData = useSSRData();

  // Use SSR data if available, otherwise fall back to sample servers
  const servers = useMemo(() => {
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

      return matchesSearch && matchesStatus;
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
    }

    return filtered;
  }, [servers, searchQuery, status, sortBy]);

  // Pagination
  const totalPages = Math.ceil(filteredServers.length / ITEMS_PER_PAGE);
  const paginatedServers = useMemo(() => {
    const startIndex = (currentPage - 1) * ITEMS_PER_PAGE;
    return filteredServers.slice(startIndex, startIndex + ITEMS_PER_PAGE);
  }, [filteredServers, currentPage]);

  // Reset to page 1 when filters change
  const handleSearchChange = (value: string) => {
    setSearchQuery(value);
    setCurrentPage(1);
  };

  const handleStatusChange = (value: string) => {
    setStatus(value);
    setCurrentPage(1);
  };

  const handleSortByChange = (value: string) => {
    setSortBy(value);
    setCurrentPage(1);
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
                onSearchChange={handleSearchChange}
                status={status}
                onStatusChange={handleStatusChange}
                sortBy={sortBy}
                onSortByChange={handleSortByChange}
              />
              <div className="grid grid-cols-1 min-[500px]:grid-cols-2 md:grid-cols-3 gap-6 p-4 min-[500px]:p-6 mt-4">
                {paginatedServers.length > 0 ? (
                  paginatedServers.map((server) => (
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
              {totalPages > 0 && (
                <Pagination
                  currentPage={currentPage}
                  totalPages={totalPages}
                  onPageChange={setCurrentPage}
                />
              )}
            </main>
          </div>
        </div>
      </div>
    </div>
  );
}

export default App;
