import { useCallback, useEffect, useState } from "react";
import { API_PATHS } from "@/lib/apiPaths";
import { APIClientError, apiClient } from "@/lib/apiClient";

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

interface AdminAuthStatusPayload {
  authenticated: boolean;
  auth_enabled: boolean;
}

interface AdminLoginPayload {
  success?: boolean;
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

  // Check browser-side lock status.
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
        // Lock expired.
        clearStoredAttempts();
        setClientLock({ isLocked: false, remainingSeconds: 0 });
      }
    }
    return false;
  }, []);

  // Keep lock countdown in sync.
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

  // Load current auth state from server.
  const checkAuth = useCallback(async () => {
    try {
      const data = await apiClient.get<AdminAuthStatusPayload>(API_PATHS.admin.authStatus);
      setAuthState({
        isAuthenticated: data.authenticated,
        isLoading: false,
        authEnabled: data.auth_enabled,
      });
    } catch {
      setAuthState({
        isAuthenticated: false,
        isLoading: false,
        authEnabled: true,
      });
    }
  }, []);

  useEffect(() => {
    checkAuth();
    checkClientLock();
  }, [checkAuth, checkClientLock]);

  // Try admin sign-in.
  const login = useCallback(
    async (key: string): Promise<LoginResult> => {
      // Apply local lock before calling server.
      if (checkClientLock()) {
        const attempts = getStoredAttempts();
        const remaining = attempts.lockedUntil
          ? Math.ceil((attempts.lockedUntil - Date.now()) / 1000)
          : 60;
        return {
          success: false,
          error: "Too many failed attempts. Try again in 1 minute.",
          locked: true,
          remaining_seconds: remaining,
        };
      }

      try {
        const data = await apiClient.post<AdminLoginPayload>(API_PATHS.admin.login, { key });

        if (data?.success) {
          // Reset local lock state on success.
          clearStoredAttempts();
          setClientLock({ isLocked: false, remainingSeconds: 0 });
          setAuthState((prev) => ({ ...prev, isAuthenticated: true }));
          return { success: true };
        }

        return { success: false, error: "Secret key is invalid." };
      } catch (err: unknown) {
        if (err instanceof APIClientError) {
          const payload =
            typeof err.details === "object" && err.details !== null
              ? (err.details as AdminLoginPayload)
              : undefined;

          const lockedByServer =
            err.code === "auth_locked" || payload?.locked === true;

          if (lockedByServer) {
            const remainingSeconds = payload?.remaining_seconds ?? 60;
            const attempts = getStoredAttempts();
            attempts.lockedUntil = Date.now() + remainingSeconds * 1000;
            setStoredAttempts(attempts);
            setClientLock({ isLocked: true, remainingSeconds });

            return {
              success: false,
              error: err.message,
              locked: true,
              remaining_seconds: remainingSeconds,
            };
          }

          // Record failed attempt locally for invalid-key style failures.
          const attempts = getStoredAttempts();
          attempts.count++;
          if (attempts.count >= MAX_ATTEMPTS) {
            attempts.lockedUntil = Date.now() + LOCK_DURATION_MS;
            setClientLock({ isLocked: true, remainingSeconds: 60 });
          }
          setStoredAttempts(attempts);

          return {
            success: false,
            error: err.message || "Secret key is invalid.",
            locked: attempts.count >= MAX_ATTEMPTS,
            remaining_seconds: payload?.remaining_seconds || 60,
          };
        }

        return {
          success: false,
          error: err instanceof Error ? err.message : "Could not sign in.",
        };
      }
    },
    [checkClientLock]
  );

  // End current admin session.
  const logout = useCallback(async () => {
    try {
      await apiClient.post<unknown>(API_PATHS.admin.logout);
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
