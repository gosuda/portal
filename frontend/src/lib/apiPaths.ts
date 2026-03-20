export const API_PATHS = {
  admin: {
    prefix: "/admin",
    snapshot: "/admin/snapshot",
    login: "/admin/login",
    logout: "/admin/logout",
    authStatus: "/admin/auth/status",
    leases: "/admin/leases",
    stats: "/admin/stats",
    approvalMode: "/admin/settings/approval-mode",
    udpSettings: "/admin/settings/udp",
  },
  sdk: {
    prefix: "/sdk",
    register: "/sdk/register",
    unregister: "/sdk/unregister",
    renew: "/sdk/renew",
    domain: "/sdk/domain",
    connect: "/sdk/connect",
  },
  healthz: "/healthz",
  install: {
    shell: "/install.sh",
    powershell: "/install.ps1",
  },
  appPrefix: "/app/",
} as const;

export const ROUTE_PATHS = {
  home: "/",
  serverDetail: "/server/:id",
  admin: "/admin",
  adminLogin: "/admin/login",
} as const;

export function encodeLeaseID(leaseID: string): string {
  return btoa(leaseID).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

export function adminLeasePath(
  encodedLeaseID: string,
  action: "ban" | "bps" | "approve" | "deny"
): string {
  return `${API_PATHS.admin.leases}/${encodeURIComponent(encodedLeaseID)}/${action}`;
}

export function adminIPBanPath(ip: string): string {
  return `${API_PATHS.admin.prefix}/ips/${encodeURIComponent(ip.trim())}/ban`;
}
