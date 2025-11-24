import { Search } from "lucide-react";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import type { SortOption, StatusFilter } from "@/types/filters";
import { TagCombobox } from "@/components/TagCombobox";

interface SearchBarProps {
  searchQuery: string;
  onSearchChange: (value: string) => void;
  status: StatusFilter;
  onStatusChange: (value: StatusFilter) => void;
  sortBy: SortOption;
  onSortByChange: (value: SortOption) => void;
  availableTags: string[];
  selectedTags: string[];
  onAddTag: (tag: string) => void;
  onRemoveTag: (tag: string) => void;
  tagMode: "AND" | "OR";
  onTagModeChange: (mode: "AND" | "OR") => void;
}

export function SearchBar({
  searchQuery,
  onSearchChange,
  status,
  onStatusChange,
  sortBy,
  onSortByChange,
  availableTags,
  selectedTags,
  onAddTag,
  onRemoveTag,
  tagMode,
  onTagModeChange,
}: SearchBarProps) {
  return (
    <div className="flex flex-wrap items-center gap-3 px-4 sm:px-6">
      <label className="flex min-w-[220px] flex-1 items-stretch h-11">
        <div className="text-text-muted flex items-center justify-center pl-4 pr-2 rounded-l-lg bg-border">
          <Search className="w-5 h-5" />
        </div>
        <Input
          placeholder="Search by server name..."
          value={searchQuery}
          onChange={(e) => onSearchChange(e.target.value)}
          className="rounded-l-none"
        />
      </label>

      <Select value={status} onValueChange={onStatusChange}>
        <SelectTrigger className="w-[130px] h-10">
          <SelectValue placeholder="Status" />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="all">All Status</SelectItem>
          <SelectItem value="online">Online</SelectItem>
          <SelectItem value="offline">Offline</SelectItem>
        </SelectContent>
      </Select>

      <Select value={sortBy} onValueChange={onSortByChange}>
        <SelectTrigger className="w-[150px] h-10">
          <SelectValue placeholder="Sort By" />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="default">Default</SelectItem>
          <SelectItem value="name-asc">Name (A-Z)</SelectItem>
          <SelectItem value="name-desc">Name (Z-A)</SelectItem>
          <SelectItem value="updated">Recently Updated</SelectItem>
          <SelectItem value="description">Description</SelectItem>
          <SelectItem value="tags">Tags</SelectItem>
          <SelectItem value="owner">Owner</SelectItem>
        </SelectContent>
      </Select>

      <div className="relative flex w-full sm:w-auto sm:min-w-[320px] flex-1">
        <TagCombobox
          availableTags={availableTags}
          selectedTags={selectedTags}
          onAdd={onAddTag}
          onRemove={onRemoveTag}
          mode={tagMode}
          onModeChange={onTagModeChange}
        />
      </div>
    </div>
  );
}
