import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { StatusFilter } from "@/types/filters";
import clsx from "clsx";

interface StatusSelectProps {
  status: string;
  onStatusChange: (value: StatusFilter) => void;
  hideFiltersOnMobile?: boolean;
  className?: string;
}

export const StatusSelect = ({
  status,
  onStatusChange,
  hideFiltersOnMobile,
  className,
}: StatusSelectProps) => (
  <Select value={status} onValueChange={onStatusChange}>
    <SelectTrigger
      className={clsx(
        "w-[130px] h-10",
        hideFiltersOnMobile && "hidden sm:flex",
        className
      )}
    >
      <SelectValue placeholder="Status" />
    </SelectTrigger>
    <SelectContent>
      <SelectItem value="all">All Status</SelectItem>
      <SelectItem value="online">Online</SelectItem>
      <SelectItem value="offline">Offline</SelectItem>
    </SelectContent>
  </Select>
);
