import { useState, useEffect, type FormEvent } from "react";
import { useNavigate } from "react-router-dom";
import { KeyRound, ShieldCheck } from "lucide-react";
import { useAuth } from "@/hooks/useAuth";

export function AdminLogin() {
  const navigate = useNavigate();
  const {
    isAuthenticated,
    isLoading,
    authEnabled,
    isLocked,
    remainingSeconds,
    login,
  } = useAuth();

  const [key, setKey] = useState("");
  const [error, setError] = useState("");
  const [submitting, setSubmitting] = useState(false);

  // Redirect if already authenticated
  useEffect(() => {
    if (!isLoading && isAuthenticated) {
      navigate("/admin", { replace: true });
    }
  }, [isAuthenticated, isLoading, navigate]);

  // Show auth not enabled message
  useEffect(() => {
    if (!isLoading && !authEnabled) {
      setError(
        "Admin authentication is not configured. Set ADMIN_SECRET_KEY in your environment."
      );
    }
  }, [isLoading, authEnabled]);

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    if (!key.trim() || isLocked || submitting) return;

    setSubmitting(true);
    setError("");

    const result = await login(key);

    setSubmitting(false);

    if (result.success) {
      navigate("/admin", { replace: true });
    } else {
      setError(result.error || "Login failed");
    }
  };

  if (isLoading) {
    return (
      <div className="min-h-screen bg-background flex items-center justify-center">
        <div className="text-muted-foreground">Loading...</div>
      </div>
    );
  }

  return (
    <div className="relative flex h-auto min-h-screen w-full flex-col">
      <div className="flex h-full grow flex-col">
        <div className="flex flex-1 justify-center py-5">
          <div className="flex flex-col w-full max-w-6xl flex-1 px-4 md:px-8">
            {/* Header */}
            <header className="flex items-center justify-between whitespace-nowrap px-4 sm:px-6 py-3">
              <div className="flex items-center gap-4 text-foreground">
                <div className="text-primary size-6">
                  <svg
                    xmlns="http://www.w3.org/2000/svg"
                    width="24"
                    height="24"
                    viewBox="0 0 906.26 1457.543"
                  >
                    <path
                      fill="currentColor"
                      d="M254.854 137.158c-34.46 84.407-88.363 149.39-110.934 245.675 90.926-187.569 308.397-483.654 554.729-348.685 135.487 74.216 194.878 270.78 206.058 467.566 21.924 385.996-190.977 853.604-467.585 943.057-174.879 56.543-307.375-86.447-364.527-198.115-176.498-344.82 2.041-910.077 182.259-1109.498zm198.13 7.918C202.61 280.257 4.622 968.542 207.322 1270.414c51.713 77.029 194.535 160.648 285.294 71.318-209.061 31.529-288.389-176.143-301.145-340.765 31.411 147.743 139.396 326.12 309.075 253.588 251.957-107.723 376.778-648.46 269.433-966.817 22.394 134.616 15.572 317.711-47.551 412.087 86.655-230.615 7.903-704.478-269.444-554.749z"
                    ></path>
                  </svg>
                </div>
                <h2 className="text-foreground text-lg font-bold leading-tight tracking-[0.3em]">
                  PORTAL ADMIN
                </h2>
              </div>
            </header>

            {/* Main Content */}
            <main className="flex flex-1 flex-col items-center justify-center py-16">
              <div className="flex w-full max-w-md flex-col items-center gap-8 rounded-xl bg-card p-8 shadow-lg">
                {/* Icon and Title */}
                <div className="flex flex-col items-center gap-2 text-center">
                  <ShieldCheck className="w-10 h-10 text-primary" />
                  <h1 className="text-2xl font-bold text-foreground">
                    Admin Access
                  </h1>
                  <p className="text-muted-foreground">
                    Enter your secret key to manage servers.
                  </p>
                </div>

                {/* Form */}
                <form
                  onSubmit={handleSubmit}
                  className="flex w-full flex-col gap-6"
                >
                  <div className="flex flex-col gap-2">
                    <label
                      className="text-sm font-medium text-muted-foreground"
                      htmlFor="admin-key"
                    >
                      ADMIN_SECRET_KEY
                    </label>
                    <div className="relative">
                      <KeyRound className="absolute left-3 top-1/2 -translate-y-1/2 w-5 h-5 text-muted-foreground" />
                      <input
                        id="admin-key"
                        type="password"
                        placeholder="Enter your secret key"
                        value={key}
                        onChange={(e) => setKey(e.target.value)}
                        disabled={isLocked || submitting || !authEnabled}
                        autoFocus
                        className="h-12 w-full rounded-lg border-none bg-secondary pl-10 pr-4 text-foreground placeholder:text-muted-foreground focus:outline-none focus:ring-2 focus:ring-primary/50"
                      />
                    </div>
                  </div>

                  {/* Error Message */}
                  {error && (
                    <div className="text-destructive text-sm text-center bg-destructive/10 p-3 rounded-md">
                      {error}
                    </div>
                  )}

                  {/* Lock Message */}
                  {isLocked && (
                    <div className="text-amber-500 text-sm text-center bg-amber-500/10 p-3 rounded-md">
                      Too many failed attempts. Please wait {remainingSeconds}{" "}
                      seconds.
                    </div>
                  )}

                  {/* Submit Button */}
                  <button
                    type="submit"
                    disabled={
                      !key.trim() || isLocked || submitting || !authEnabled
                    }
                    className="flex h-12 w-full cursor-pointer items-center justify-center overflow-hidden rounded-lg bg-primary text-base font-bold text-white transition-colors hover:bg-primary/90 disabled:opacity-50 disabled:cursor-not-allowed"
                  >
                    <span className="truncate">
                      {submitting
                        ? "Authenticating..."
                        : isLocked
                        ? `Wait ${remainingSeconds}s`
                        : "Login"}
                    </span>
                  </button>
                </form>

                {/* Back Link */}
                <a
                  href="/"
                  className="text-sm text-muted-foreground hover:text-foreground transition-colors"
                >
                  Back to Home
                </a>
              </div>
            </main>
          </div>
        </div>
      </div>
    </div>
  );
}
