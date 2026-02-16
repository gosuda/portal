import { useCallback, useEffect, useState } from "react";

const STORAGE_KEY = "admin_login_attempts";
const MAX_ATTEMPTS = 3;
const LOCK_DURATION_MS = 60 * 1000; // 1 minute

interface LoginAttempts {
  count: number;
  lockedUntil: number | null;
}

interface AuthState {
  isAuthenticated: boolean;
  isLoading: boolean;
  authEnabled: boolean;
}

interface LoginResult {
  success: boolean;
  error?: string;
  locked?: boolean;
  remaining_seconds?: number;
}

function getStoredAttempts(): LoginAttempts {
  try {
    const stored = localStorage.getItem(STORAGE_KEY);
    if (stored) {
      return JSON.parse(stored);
    }
  } catch {
    // Ignore parse errors
  }
  return { count: 0, lockedUntil: null };
}

function setStoredAttempts(attempts: LoginAttempts): void {
  localStorage.setItem(STORAGE_KEY, JSON.stringify(attempts));
}

function clearStoredAttempts(): void {
  localStorage.removeItem(STORAGE_KEY);
}

export function useAuth() {
  const [authState, setAuthState] = useState<AuthState>({
    isAuthenticated: false,
    isLoading: true,
    authEnabled: true,
  });

  const [clientLock, setClientLock] = useState<{
    isLocked: boolean;
    remainingSeconds: number;
  }>({
    isLocked: false,
    remainingSeconds: 0,
  });

  // Check client-side lock status
  const checkClientLock = useCallback(() => {
    const attempts = getStoredAttempts();
    if (attempts.lockedUntil) {
      const remaining = attempts.lockedUntil - Date.now();
      if (remaining > 0) {
        setClientLock({
          isLocked: true,
          remainingSeconds: Math.ceil(remaining / 1000),
        });
        return true;
      } else {
        // Lock expired, clear it
        clearStoredAttempts();
        setClientLock({ isLocked: false, remainingSeconds: 0 });
      }
    }
    return false;
  }, []);

  // Update countdown timer
  useEffect(() => {
    if (!clientLock.isLocked) return;

    const interval = setInterval(() => {
      const attempts = getStoredAttempts();
      if (attempts.lockedUntil) {
        const remaining = attempts.lockedUntil - Date.now();
        if (remaining > 0) {
          setClientLock({
            isLocked: true,
            remainingSeconds: Math.ceil(remaining / 1000),
          });
        } else {
          clearStoredAttempts();
          setClientLock({ isLocked: false, remainingSeconds: 0 });
        }
      }
    }, 1000);

    return () => clearInterval(interval);
  }, [clientLock.isLocked]);

  // Check authentication status on mount
  const checkAuth = useCallback(async () => {
    try {
      const res = await fetch("/admin/auth/status");
      if (res.ok) {
        const data = await res.json();
        setAuthState({
          isAuthenticated: data.authenticated,
          isLoading: false,
          authEnabled: data.auth_enabled,
        });
      } else {
        setAuthState({
          isAuthenticated: false,
          isLoading: false,
          authEnabled: true,
        });
      }
    } catch {
      setAuthState({
        isAuthenticated: false,
        isLoading: false,
        authEnabled: true,
      });
    }
  }, []);

  useEffect(() => {
    const timeoutId = window.setTimeout(() => {
      void checkAuth();
      checkClientLock();
    }, 0);

    return () => {
      window.clearTimeout(timeoutId);
    };
  }, [checkAuth, checkClientLock]);

  // Login function
  const login = useCallback(
    async (key: string): Promise<LoginResult> => {
      // Check client-side lock first
      if (checkClientLock()) {
        const attempts = getStoredAttempts();
        const remaining = attempts.lockedUntil
          ? Math.ceil((attempts.lockedUntil - Date.now()) / 1000)
          : 60;
        return {
          success: false,
          error: "Too many failed attempts. Please try again later.",
          locked: true,
          remaining_seconds: remaining,
        };
      }

      try {
        const res = await fetch("/admin/login", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ key }),
        });

        const data = await res.json();

        if (data.success) {
          // Clear failed attempts on success
          clearStoredAttempts();
          setClientLock({ isLocked: false, remainingSeconds: 0 });
          setAuthState((prev) => ({ ...prev, isAuthenticated: true }));
          return { success: true };
        }

        // Record failed attempt client-side
        const attempts = getStoredAttempts();
        attempts.count++;

        if (attempts.count >= MAX_ATTEMPTS) {
          attempts.lockedUntil = Date.now() + LOCK_DURATION_MS;
          setClientLock({ isLocked: true, remainingSeconds: 60 });
        }

        setStoredAttempts(attempts);

        return {
          success: false,
          error: data.error || "Invalid key",
          locked: data.locked || attempts.count >= MAX_ATTEMPTS,
          remaining_seconds: data.remaining_seconds || 60,
        };
      } catch (err) {
        return {
          success: false,
          error: err instanceof Error ? err.message : "Login failed",
        };
      }
    },
    [checkClientLock]
  );

  // Logout function
  const logout = useCallback(async () => {
    try {
      await fetch("/admin/logout", { method: "POST" });
    } catch {
      // Ignore errors
    }
    setAuthState((prev) => ({ ...prev, isAuthenticated: false }));
  }, []);

  return {
    ...authState,
    ...clientLock,
    login,
    logout,
    checkAuth,
  };
}
