import { ChevronLeft, ChevronRight } from "lucide-react";

interface PaginationProps {
  currentPage: number;
  totalPages: number;
  onPageChange: (page: number) => void;
}

export function Pagination({
  currentPage,
  totalPages,
  onPageChange,
}: PaginationProps) {
  return (
    <div className="flex items-center justify-center gap-4 px-4 sm:px-6 py-8">
      <button
        onClick={() => onPageChange(currentPage - 1)}
        disabled={currentPage === 1}
        className="flex h-10 w-10 cursor-pointer items-center justify-center overflow-hidden rounded-full bg-border text-foreground hover:bg-border/80 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
      >
        <ChevronLeft className="w-6 h-6" />
      </button>
      <span className="text-sm text-gray-400">
        Page {currentPage} of {totalPages}
      </span>
      <button
        onClick={() => onPageChange(currentPage + 1)}
        disabled={currentPage === totalPages}
        className="flex h-10 w-10 cursor-pointer items-center justify-center overflow-hidden rounded-full bg-border text-foreground hover:bg-border/80 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
      >
        <ChevronRight className="w-6 h-6" />
      </button>
    </div>
  );
}
