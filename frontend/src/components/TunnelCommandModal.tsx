import type { ReactNode } from "react";
import { Terminal } from "lucide-react";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { TunnelCommandForm } from "@/components/TunnelCommandForm";

interface TunnelCommandModalProps {
  trigger?: ReactNode;
}

export function TunnelCommandModal({ trigger }: TunnelCommandModalProps) {
  return (
    <Dialog>
      <DialogTrigger asChild>
        {trigger || (
          <Button className="cursor-pointer">
            <span className="truncate">Add Your Server</span>
          </Button>
        )}
      </DialogTrigger>
      <DialogContent className="sm:max-w-[560px] max-h-[85vh] overflow-y-auto rounded-[1.5rem] border border-border bg-card p-0">
        <DialogHeader className="border-b border-border px-5 py-4 text-left">
          <DialogTitle className="flex items-center gap-2 text-xl font-bold">
            <Terminal className="h-5 w-5" />
            Tunnel Setup Command
          </DialogTitle>
        </DialogHeader>

        <div className="px-5 pb-5 pt-4">
          <TunnelCommandForm />
        </div>
      </DialogContent>
    </Dialog>
  );
}
