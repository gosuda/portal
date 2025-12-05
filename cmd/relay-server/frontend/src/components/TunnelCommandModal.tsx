import { useState, useMemo } from "react";
import { Copy, Check, Terminal } from "lucide-react";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogTrigger,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

interface TunnelCommandModalProps {
  trigger?: React.ReactNode;
}

export function TunnelCommandModal({ trigger }: TunnelCommandModalProps) {
  const defaultPort = "3000";
  const defaultName = "myapp";

  const [port, setPort] = useState(defaultPort);
  const [name, setName] = useState(defaultName);
  const [copied, setCopied] = useState(false);

  // Get current host URL dynamically
  const hostUrl = useMemo(() => {
    if (typeof window !== "undefined") {
      return window.location.origin;
    }
    return "http://localhost:4017";
  }, []);

  // Generate the tunnel command
  const command = useMemo(() => {
    return `curl -fsSL ${hostUrl}/tunnel | PORT=${
      port === "" ? defaultPort : port
    } NAME=${name === "" ? defaultName : name} sh`;
  }, [hostUrl, port, name]);

  const handleCopy = async () => {
    try {
      await navigator.clipboard.writeText(command);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch (err) {
      console.error("Failed to copy:", err);
    }
  };

  return (
    <Dialog>
      <DialogTrigger asChild>
        {trigger || (
          <Button>
            <span className="truncate">Add Your Server</span>
          </Button>
        )}
      </DialogTrigger>
      <DialogContent className="sm:max-w-[500px]">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <Terminal className="w-5 h-5" />
            Tunnel Setup Command
          </DialogTitle>
          <DialogDescription>
            Configure your tunnel settings and copy the command to start
            exposing your local server.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4 py-4 [&>div]:flex [&>div]:flex-col">
          {/* Port Input */}
          <div className="space-y-2">
            <label
              htmlFor="port"
              className="text-sm font-medium text-foreground"
            >
              Local Port
            </label>
            <div className="flex items-center rounded-md bg-border">
              <span className="px-3 text-sm text-text-muted">PORT=</span>
              <Input
                id="port"
                type="number"
                value={port}
                onChange={(e) => setPort(e.target.value)}
                placeholder={defaultPort}
                className="rounded-l-none"
              />
            </div>
            <p className="text-xs text-text-muted">
              The port your local service is running on
            </p>
          </div>

          {/* Name Input */}
          <div className="space-y-2">
            <label
              htmlFor="name"
              className="text-sm font-medium text-foreground"
            >
              Service Name
            </label>
            <div className="flex items-center rounded-md bg-border">
              <span className="px-3 text-sm text-text-muted">NAME=</span>
              <Input
                id="name"
                type="text"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder={defaultName}
                className="rounded-l-none"
              />
            </div>
            <p className="text-xs text-text-muted">
              A unique identifier for your tunnel
            </p>
          </div>

          {/* Generated Command */}
          <div className="space-y-2">
            <label className="text-sm font-medium text-foreground">
              Generated Command
            </label>
            <div className="relative">
              <pre className="p-3 pr-12 rounded-md bg-border text-sm text-foreground overflow-x-auto whitespace-pre-wrap break-all font-mono">
                {command}
              </pre>
              <button
                onClick={handleCopy}
                className="cursor-pointer absolute right-2 top-1/2 -translate-y-1/2 p-2 rounded-md hover:bg-background/50 transition-colors"
                aria-label="Copy command"
              >
                {copied ? (
                  <Check className="w-4 h-4 text-green-500" />
                ) : (
                  <Copy className="w-4 h-4 text-text-muted" />
                )}
              </button>
            </div>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}
