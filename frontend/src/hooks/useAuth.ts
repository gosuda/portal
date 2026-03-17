import { useEffect, useState } from "react";
import { API_PATHS } from "@/lib/apiPaths";
import { APIClientError, apiClient } from "@/lib/apiClient";

interface AuthState {
  isAuthenticated: boolean;
  isLoading: boolean;
  authEnabled: boolean;
}

interface LoginResult {
  success: boolean;
  error?: string;
}

interface AdminAuthStatusPayload {
  authenticated: boolean;
  auth_enabled: boolean;
}

interface AdminLoginPayload {
  success?: boolean;
}

async function fetchAuthState(): Promise<AuthState> {
  try {
    const data = await apiClient.get<AdminAuthStatusPayload>(API_PATHS.admin.authStatus);
    return {
      isAuthenticated: data.authenticated,
      isLoading: false,
      authEnabled: data.auth_enabled,
    };
  } catch {
    return {
      isAuthenticated: false,
      isLoading: false,
      authEnabled: true,
    };
  }
}

export function useAuth() {
  const [authState, setAuthState] = useState<AuthState>({
    isAuthenticated: false,
    isLoading: true,
    authEnabled: true,
  });

  const checkAuth = async () => {
    setAuthState(await fetchAuthState());
  };

  useEffect(() => {
    void (async () => {
      setAuthState(await fetchAuthState());
    })();
  }, []);

  const login = async (key: string): Promise<LoginResult> => {
    try {
      const data = await apiClient.post<AdminLoginPayload>(API_PATHS.admin.login, { key });
      if (data?.success) {
        setAuthState((prev) => ({ ...prev, isAuthenticated: true }));
        return { success: true };
      }
      return { success: false, error: "Secret key is invalid." };
    } catch (err: unknown) {
      if (err instanceof APIClientError) {
        return {
          success: false,
          error: err.message || "Secret key is invalid.",
        };
      }

      return {
        success: false,
        error: err instanceof Error ? err.message : "Could not sign in.",
      };
    }
  };

  const logout = async () => {
    try {
      await apiClient.post<unknown>(API_PATHS.admin.logout);
    } catch {
      // Ignore errors
    }
    setAuthState((prev) => ({ ...prev, isAuthenticated: false }));
  };

  return {
    ...authState,
    login,
    logout,
    checkAuth,
  };
}
