import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { StatusSelect } from "@/components/select/StatusSelect";
import { BanStatusButtons } from "@/components/button/BanStatusButtons";
import { SortbySelect } from "@/components/select/SortbySelect";
import { TagCombobox } from "@/components/TagCombobox";
import type { StatusFilter, SortOption } from "@/types/filters";
import type { BanFilter } from "@/types/server";

interface MobileFilterModalProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  status: StatusFilter;
  onStatusChange: (value: StatusFilter) => void;
  sortBy: SortOption;
  onSortByChange: (value: SortOption) => void;
  availableTags: string[];
  selectedTags: string[];
  onTagToggle: (tag: string) => void;
  isAdmin?: boolean;
  banFilter?: BanFilter;
  onBanFilterChange?: (value: BanFilter) => void;
}

export function MobileFilterModal({
  open,
  onOpenChange,
  status,
  onStatusChange,
  sortBy,
  onSortByChange,
  availableTags,
  selectedTags,
  onTagToggle,
  isAdmin = false,
  banFilter = "all",
  onBanFilterChange,
}: MobileFilterModalProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
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
  );
}
