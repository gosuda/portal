import { Button } from "@/components/ui/button";
import type { TagMode } from "@/types/filters";

type TagFilterProps = {
  availableTags: string[];
  selectedTags: string[];
  mode: TagMode;
  onModeChange: (mode: TagMode) => void;
  onToggleTag: (tag: string) => void;
  onClear: () => void;
};

export function TagFilter({
  availableTags,
  selectedTags,
  mode,
  onModeChange,
  onToggleTag,
  onClear,
}: TagFilterProps) {
  const isSelected = (tag: string) => selectedTags.includes(tag);

  return (
    <div className="flex flex-col gap-3 rounded-xl border border-border/70 bg-background px-4 py-3">
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-sm font-semibold text-foreground">Tags</span>
        <div className="flex rounded-md bg-border text-xs font-semibold text-foreground/80 overflow-hidden">
          <button
            type="button"
            className={`px-3 py-1 transition-colors ${
              mode === "OR" ? "bg-primary text-black" : "hover:bg-border/80"
            }`}
            aria-pressed={mode === "OR"}
            onClick={() => onModeChange("OR")}
          >
            Match Any
          </button>
          <button
            type="button"
            className={`px-3 py-1 transition-colors ${
              mode === "AND" ? "bg-primary text-black" : "hover:bg-border/80"
            }`}
            aria-pressed={mode === "AND"}
            onClick={() => onModeChange("AND")}
          >
            Match All
          </button>
        </div>
        {selectedTags.length > 0 && (
          <Button
            variant="ghost"
            size="sm"
            className="text-xs"
            onClick={onClear}
          >
            Clear
          </Button>
        )}
      </div>

      <div className="flex flex-wrap gap-2">
        {availableTags.map((tag) => (
          <Button
            key={tag}
            variant={isSelected(tag) ? "default" : "secondary"}
            size="sm"
            className="h-8 rounded-full px-3 text-xs"
            onClick={() => onToggleTag(tag)}
            aria-pressed={isSelected(tag)}
          >
            {tag}
          </Button>
        ))}
        {availableTags.length === 0 && (
          <span className="text-xs text-text-muted">No tags available</span>
        )}
      </div>
    </div>
  );
}
