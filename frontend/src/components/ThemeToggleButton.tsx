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
      variant="ghost"
      size="icon"
      onClick={toggleTheme}
      className={clsx(
        "h-11 w-11 cursor-pointer rounded-full text-foreground shadow-none hover:bg-transparent hover:-translate-y-0.5 hover:text-primary",
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
