import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { TunnelCommandForm } from "@/components/TunnelCommandForm";

export function TunnelCommandModal() {
  return (
    <Dialog>
      <DialogTrigger asChild>
        <Button
          type="button"
          className="h-10 cursor-pointer rounded-full bg-primary/12 px-4 text-sm font-semibold text-primary shadow-none transition-colors hover:bg-primary/20"
        >
          Add Your Server
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-140 max-h-[85vh] overflow-y-auto rounded-3xl border border-border bg-card p-0">
        <DialogHeader className="border-b border-border px-5 py-4 text-left">
          <DialogTitle className="text-xl font-bold">Add Your Server</DialogTitle>
          <DialogDescription className="pt-1 leading-6">
            Start your local app, for example on
            <span className="mx-1 font-mono text-foreground">
              localhost:3000
            </span>
            , then copy and run the generated command.
          </DialogDescription>
        </DialogHeader>

        <div className="px-5 pb-5 pt-4">
          <TunnelCommandForm />
        </div>
      </DialogContent>
    </Dialog>
  );
}
