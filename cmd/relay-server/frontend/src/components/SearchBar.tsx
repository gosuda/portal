import { Search } from "lucide-react";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

interface SearchBarProps {
  searchQuery: string;
  onSearchChange: (value: string) => void;
  status: string;
  onStatusChange: (value: string) => void;
  sortBy: string;
  onSortByChange: (value: string) => void;
  availableTags: string[];
  onTagSelect: (value: string) => void;
}

export function SearchBar({
  searchQuery,
  onSearchChange,
  status,
  onStatusChange,
  sortBy,
  onSortByChange,
  availableTags,
  onTagSelect,
}: SearchBarProps) {
  return (
    <div className="space-y-4 px-4 sm:px-6">
      <label className="flex flex-col min-w-40 h-12 w-full">
        <div className="flex w-full flex-1 items-stretch rounded-lg h-full">
          <div className="text-text-muted flex border-none bg-border-dark items-center justify-center pl-4 rounded-l-lg border-r-0">
            <Search className="w-5 h-5" />
          </div>
          <Input
            placeholder="Search by server name..."
            value={searchQuery}
            onChange={(e) => onSearchChange(e.target.value)}
          />
        </div>
      </label>
      <div className="flex flex-wrap gap-3">
        <Select value={status} onValueChange={onStatusChange}>
          <SelectTrigger className="w-[140px] h-8">
            <SelectValue placeholder="Status" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All Status</SelectItem>
            <SelectItem value="online">Online</SelectItem>
            <SelectItem value="offline">Offline</SelectItem>
          </SelectContent>
        </Select>

        <Select value={sortBy} onValueChange={onSortByChange}>
          <SelectTrigger className="w-[140px] h-8">
            <SelectValue placeholder="Sort By" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="default">Default</SelectItem>
            <SelectItem value="name-asc">Name (A-Z)</SelectItem>
            <SelectItem value="name-desc">Name (Z-A)</SelectItem>
            <SelectItem value="description">Description</SelectItem>
            <SelectItem value="tags">Tags</SelectItem>
            <SelectItem value="owner">Owner</SelectItem>
          </SelectContent>
        </Select>

        <Select value="" onValueChange={onTagSelect}>
          <SelectTrigger className="w-[140px] h-8">
            <SelectValue placeholder="Filter Tags" />
          </SelectTrigger>
          <SelectContent>
            {availableTags.length > 0 ? (
              availableTags.map((tag) => (
                <SelectItem key={tag} value={tag}>
                  {tag}
                </SelectItem>
              ))
            ) : (
              <SelectItem value="no-tags" disabled>
                No tags available
              </SelectItem>
            )}
          </SelectContent>
        </Select>
      </div>
    </div>
  );
}
