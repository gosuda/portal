import { Moon, Sun } from "lucide-react";
import clsx from "clsx";
import { Button } from "@/components/ui/button";
import { useTheme } from "@/components/ThemeProvider";

interface ThemeToggleButtonProps {
  className?: string;
}

export function ThemeToggleButton({ className }: ThemeToggleButtonProps) {
  const { theme, toggleTheme } = useTheme();
  const nextTheme = theme === "dark" ? "light" : "dark";

  return (
    <Button
      type="button"
      variant="outline"
      size="icon"
      onClick={toggleTheme}
      className={clsx(
        "h-11 w-11 cursor-pointer rounded-full border-border bg-card/95 text-foreground shadow-none hover:bg-secondary",
        className
      )}
      aria-label={`Switch to ${nextTheme} theme`}
      title={theme === "dark" ? "Light theme" : "Dark theme"}
    >
      {theme === "dark" ? (
        <Sun className="h-5 w-5" />
      ) : (
        <Moon className="h-5 w-5" />
      )}
    </Button>
  );
}
