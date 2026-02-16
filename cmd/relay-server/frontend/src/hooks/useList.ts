import { useCallback, useEffect, useMemo, useState } from "react";
import type { SortOption, StatusFilter } from "@/types/filters";
import type { BaseServer } from "@/types/server";

export interface UseListOptions<T extends BaseServer> {
  servers: T[];
  storageKey: string;
  // Optional additional filter function for extended filtering (e.g., ban filter)
  additionalFilter?: (server: T) => boolean;
}

export interface UseListReturn<T extends BaseServer> {
  // Filter states
  searchQuery: string;
  status: StatusFilter;
  sortBy: SortOption;
  selectedTags: string[];
  favorites: number[];
  // Derived data
  availableTags: string[];
  filteredServers: T[];
  // Handlers
  handleSearchChange: (value: string) => void;
  handleStatusChange: (value: StatusFilter) => void;
  handleSortByChange: (value: SortOption) => void;
  handleTagToggle: (tag: string) => void;
  handleToggleFavorite: (serverId: number) => void;
}

export function useList<T extends BaseServer>({
  servers,
  storageKey,
  additionalFilter,
}: UseListOptions<T>): UseListReturn<T> {
  const [searchQuery, setSearchQuery] = useState("");
  const [status, setStatus] = useState<StatusFilter>("all");
  const [sortBy, setSortBy] = useState<SortOption>("duration");
  const [selectedTags, setSelectedTags] = useState<string[]>([]);
  const [favorites, setFavorites] = useState<number[]>(() => {
    const stored = localStorage.getItem(storageKey);
    return stored ? JSON.parse(stored) : [];
  });

  // Save favorites to localStorage whenever they change
  useEffect(() => {
    localStorage.setItem(storageKey, JSON.stringify(favorites));
  }, [favorites, storageKey]);

  // Extract available tags
  const availableTags = useMemo(() => {
    const counts = new Map<string, number>();
    servers.forEach((server) => {
      server.tags.forEach((tag) => {
        counts.set(tag, (counts.get(tag) || 0) + 1);
      });
    });
    return Array.from(counts.entries())
      .sort((a, b) => b[1] - a[1])
      .map(([tag]) => tag);
  }, [servers]);

  // Filter and sort servers
  const filteredServers = useMemo(() => {
    const query = searchQuery.toLowerCase();

    const matchesTags = (server: T) => {
      if (selectedTags.length === 0) return true;
      const tagsLower = server.tags.map((t) => t.toLowerCase());
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

      const matchesAdditional = additionalFilter ? additionalFilter(server) : true;

      return matchesSearch && matchesStatus && matchesTags(server) && matchesAdditional;
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
      case "duration":
        sorted.sort((a, b) => {
          // Duration = Now - FirstSeen.
          // Longer duration = Older FirstSeen.
          // Sort Descending (Longest/Oldest first) -> Ascending FirstSeen timestamp.
          const aTime = a.firstSeen ? Date.parse(a.firstSeen) : Date.now();
          const bTime = b.firstSeen ? Date.parse(b.firstSeen) : Date.now();
          return aTime - bTime;
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

    // Sort by favorites first
    sorted.sort((a, b) => {
      const aIsFav = favorites.includes(a.id);
      const bIsFav = favorites.includes(b.id);
      if (aIsFav && !bIsFav) return -1;
      if (!aIsFav && bIsFav) return 1;
      return 0;
    });

    return sorted;
  }, [servers, searchQuery, status, sortBy, selectedTags, favorites, additionalFilter]);

  // Handlers
  const handleSearchChange = useCallback((value: string) => {
    setSearchQuery(value);
  }, []);

  const handleStatusChange = useCallback((value: StatusFilter) => {
    setStatus(value);
  }, []);

  const handleSortByChange = useCallback((value: SortOption) => {
    setSortBy(value);
  }, []);

  const handleTagToggle = useCallback((tag: string) => {
    setSelectedTags((prev) =>
      prev.includes(tag) ? prev.filter((t) => t !== tag) : [...prev, tag]
    );
  }, []);

  const handleToggleFavorite = useCallback((serverId: number) => {
    setFavorites((prev) =>
      prev.includes(serverId)
        ? prev.filter((id) => id !== serverId)
        : [...prev, serverId]
    );
  }, []);

  return {
    // Filter states
    searchQuery,
    status,
    sortBy,
    selectedTags,
    favorites,
    // Derived data
    availableTags,
    filteredServers,
    // Handlers
    handleSearchChange,
    handleStatusChange,
    handleSortByChange,
    handleTagToggle,
    handleToggleFavorite,
  };
}
