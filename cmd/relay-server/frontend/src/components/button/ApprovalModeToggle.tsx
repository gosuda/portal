import type { ApprovalMode } from "@/hooks/useAdmin";

interface ApprovalModeToggleProps {
  approvalMode: ApprovalMode;
  onApprovalModeChange: (mode: ApprovalMode) => void;
}

export const ApprovalModeToggle = ({
  approvalMode,
  onApprovalModeChange,
}: ApprovalModeToggleProps) => (
  <div className="flex rounded-lg overflow-hidden border border-foreground/20">
    <button
      onClick={() => onApprovalModeChange("auto")}
      className={`px-4 h-10 text-sm font-medium transition-colors ${
        approvalMode === "auto"
          ? "bg-primary text-primary-foreground"
          : "bg-secondary text-secondary-foreground hover:bg-secondary/80"
      }`}
    >
      Auto
    </button>
    <button
      onClick={() => onApprovalModeChange("manual")}
      className={`px-4 h-10 text-sm font-medium transition-colors border-l border-foreground/20 ${
        approvalMode === "manual"
          ? "bg-primary text-primary-foreground"
          : "bg-secondary text-secondary-foreground hover:bg-secondary/80"
      }`}
    >
      Manual
    </button>
  </div>
);
