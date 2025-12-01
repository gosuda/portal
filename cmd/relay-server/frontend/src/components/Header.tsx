import { useEffect, useState } from "react";
import { Moon, Sun } from "lucide-react";

export function Header() {
  const [theme, setTheme] = useState<"light" | "dark">("dark");

  useEffect(() => {
    // Check localStorage for saved theme
    const savedTheme = localStorage.getItem("theme") as "light" | "dark" | null;
    if (savedTheme) {
      setTheme(savedTheme);
      document.documentElement.classList.remove("light", "dark");
      document.documentElement.classList.add(savedTheme);
      document.body.classList.remove("light", "dark");
      document.body.classList.add(savedTheme);
    } else {
      // Default to dark mode
      document.documentElement.classList.add("dark");
      document.body.classList.add("dark");
    }
  }, []);

  const toggleTheme = () => {
    const newTheme = theme === "dark" ? "light" : "dark";
    setTheme(newTheme);
    localStorage.setItem("theme", newTheme);
    document.documentElement.classList.remove("light", "dark");
    document.documentElement.classList.add(newTheme);
    document.body.classList.remove("light", "dark");
    document.body.classList.add(newTheme);
  };

  return (
    <header className="flex items-center justify-between whitespace-nowrap border-b border-solid border-b-border px-4 sm:px-6 py-3">
      <div className="flex items-center gap-4 text-foreground">
        <div className="text-primary size-6">
          <svg
            xmlns="http://www.w3.org/2000/svg"
            width="24"
            height="24"
            viewBox="0 0 906.26 1457.543"
          >
            <path
              fill="#17C0E9"
              d="M254.854 137.158c-34.46 84.407-88.363 149.39-110.934 245.675 90.926-187.569 308.397-483.654 554.729-348.685 135.487 74.216 194.878 270.78 206.058 467.566 21.924 385.996-190.977 853.604-467.585 943.057-174.879 56.543-307.375-86.447-364.527-198.115-176.498-344.82 2.041-910.077 182.259-1109.498zm198.13 7.918C202.61 280.257 4.622 968.542 207.322 1270.414c51.713 77.029 194.535 160.648 285.294 71.318-209.061 31.529-288.389-176.143-301.145-340.765 31.411 147.743 139.396 326.12 309.075 253.588 251.957-107.723 376.778-648.46 269.433-966.817 22.394 134.616 15.572 317.711-47.551 412.087 86.655-230.615 7.903-704.478-269.444-554.749z"
            ></path>
          </svg>
        </div>
        <h2 className="text-foreground text-lg font-bold leading-tight tracking-[-0.015em]">
          PORTAL
        </h2>
      </div>
      <div className="flex items-center gap-3">
        <a
          href="https://github.com/gosuda/portal"
          target="_blank"
          rel="noopener noreferrer"
          className="text-foreground hover:text-primary transition-colors"
          aria-label="View source on GitHub"
        >
          <svg
            height="32"
            width="32"
            viewBox="0 0 24 24"
            fill="currentColor"
            className="opacity-80 hover:opacity-100"
          >
            <path d="M12 1C5.923 1 1 5.923 1 12c0 4.867 3.149 8.979 7.521 10.436.55.096.756-.233.756-.522 0-.262-.013-1.128-.013-2.049-2.764.509-3.479-.674-3.699-1.292-.124-.317-.66-1.293-1.127-1.554-.385-.207-.936-.715-.014-.729.866-.014 1.485.797 1.691 1.128.99 1.663 2.571 1.196 3.204.907.096-.715.385-1.196.701-1.471-2.448-.275-5.005-1.224-5.005-5.432 0-1.196.426-2.186 1.128-2.956-.111-.275-.496-1.402.11-2.915 0 0 .921-.288 3.024 1.128a10.193 10.193 0 0 1 2.75-.371c.936 0 1.871.123 2.75.371 2.104-1.43 3.025-1.128 3.025-1.128.605 1.513.221 2.64.111 2.915.701.77 1.127 1.747 1.127 2.956 0 4.222-2.571 5.157-5.019 5.432.399.344.743 1.004.743 2.035 0 1.471-.014 2.654-.014 3.025 0 .289.206.632.756.522C19.851 20.979 23 16.854 23 12c0-6.077-4.922-11-11-11Z"></path>
          </svg>
        </a>
        <button
          onClick={toggleTheme}
          className="text-foreground hover:text-primary transition-colors p-1 rounded-lg hover:bg-secondary"
          aria-label="Toggle theme"
        >
          {theme === "dark" ? (
            <Sun className="w-6 h-6" />
          ) : (
            <Moon className="w-6 h-6" />
          )}
        </button>
        {/* <Button>
          <span className="truncate">Add Your Server</span>
        </Button> */}
      </div>
    </header>
  );
}
