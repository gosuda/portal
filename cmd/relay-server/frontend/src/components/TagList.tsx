import { X } from "lucide-react";
import { cn } from "@/lib/utils";

interface TagListProps {
  tags: string[];
  onRemoveTag: (tag: string) => void;
  className?: string;
}

export function TagList({ tags, onRemoveTag, className }: TagListProps) {
  if (tags.length === 0) return null;

  return (
    <div className={cn("flex flex-wrap gap-2 px-4 sm:px-6 mt-2", className)}>
      {tags.map((tag) => (
        <span
          key={tag}
          className="inline-flex items-center gap-1 rounded-full bg-[#233348] px-3 py-1 text-xs font-medium text-white"
        >
          {tag}
          <button
            onClick={() => onRemoveTag(tag)}
            className="ml-1 rounded-full hover:bg-white/20 p-0.5 transition-colors"
            aria-label={`Remove ${tag} tag`}
          >
            <X className="h-3 w-3" />
          </button>
        </span>
      ))}
    </div>
  );
}
