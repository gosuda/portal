import { useState } from "react";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import clsx from "clsx";

// BPS slider steps: 0 (Unlimited), 10, 100, 1K, 10K, 100K, 1M, 10M
const bpsSteps = [0, 10, 100, 1000, 10000, 100000, 1000000, 10000000];

function bpsToSliderIndex(value: number): number {
  if (value === 0) return 0;
  const idx = bpsSteps.findIndex((step) => step >= value);
  return idx === -1 ? bpsSteps.length - 1 : idx;
}

function formatSliderLabel(value: number): string {
  if (value === 0) return "Unlimited";
  if (value >= 1000000) return `${value / 1000000} MB/s`;
  if (value >= 1000) return `${value / 1000} KB/s`;
  return `${value} B/s`;
}

function formatStepLabel(value: number): string {
  if (value === 0) return "∞";
  if (value >= 1000000) return `${value / 1000000}M`;
  if (value >= 1000) return `${value / 1000}K`;
  return value.toString();
}

export function formatBPS(value: number): string {
  if (value === 0) return "Unlimited";
  if (value >= 1_000_000_000)
    return `${(value / 1_000_000_000).toFixed(1)} GB/s`;
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)} MB/s`;
  if (value >= 1_000) return `${(value / 1_000).toFixed(1)} KB/s`;
  return `${value} B/s`;
}

interface BPSSettingsModalProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  bps: number;
  leaseId: string;
  onBPSChange: (leaseId: string, bps: number) => void;
}

export function BPSSettingsModal({
  open,
  onOpenChange,
  bps,
  leaseId,
  onBPSChange,
}: BPSSettingsModalProps) {
  const [bpsInput, setBpsInput] = useState(bps.toString());
  const [sliderIndex, setSliderIndex] = useState(bpsToSliderIndex(bps));

  const handleSliderChange = (idx: number) => {
    setSliderIndex(idx);
    setBpsInput(bpsSteps[idx].toString());
  };

  const syncSliderFromInput = (value: number) => {
    const idx = bpsToSliderIndex(value);
    setSliderIndex(idx);
  };

  const handleSave = () => {
    const newBps = parseInt(bpsInput, 10) || 0;
    onBPSChange(leaseId, newBps);
    onOpenChange(false);
  };

  const handleOpenChange = (isOpen: boolean) => {
    if (isOpen) {
      // Reset to current BPS when opening
      setSliderIndex(bpsToSliderIndex(bps));
      setBpsInput(bps.toString());
    }
    onOpenChange(isOpen);
  };

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent className="max-w-sm rounded-xl">
        <DialogHeader>
          <DialogTitle>BPS Settings</DialogTitle>
          <DialogDescription>
            Set bytes-per-second limit (0 = unlimited)
          </DialogDescription>
        </DialogHeader>
        {/* Current value display */}
        <div className="text-center text-xl font-bold text-primary">
          {formatSliderLabel(parseInt(bpsInput, 10) || 0)}
        </div>
        {/* Slider */}
        <input
          type="range"
          min="0"
          max={bpsSteps.length - 1}
          value={sliderIndex}
          onChange={(e) => {
            const idx = parseInt(e.target.value, 10);
            handleSliderChange(idx);
          }}
          className="w-full h-2 bg-secondary rounded-md appearance-none cursor-pointer"
        />
        {/* Step labels */}
        <div className="flex justify-between text-xs text-text-muted">
          {bpsSteps.map((step, idx) => (
            <span
              key={idx}
              className={clsx(
                "cursor-pointer hover:text-foreground transition-colors",
                sliderIndex === idx && "text-primary font-medium"
              )}
              onClick={() => handleSliderChange(idx)}
            >
              {formatStepLabel(step)}
            </span>
          ))}
        </div>
        {/* Manual input */}
        <div>
          <label className="text-xs text-text-muted mb-1 block">
            Custom value (B/s)
          </label>
          <input
            type="number"
            value={bpsInput}
            onChange={(e) => {
              setBpsInput(e.target.value);
              syncSliderFromInput(parseInt(e.target.value, 10) || 0);
            }}
            className="w-full px-3 py-2 border border-foreground/20 rounded bg-background text-foreground"
            placeholder="Enter BPS limit"
            min="0"
          />
        </div>
        <DialogFooter className="gap-2 sm:gap-0">
          <Button
            className="cursor-pointer"
            variant="secondary"
            onClick={() => onOpenChange(false)}
          >
            Cancel
          </Button>
          <Button className="cursor-pointer" onClick={handleSave}>
            Save
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
