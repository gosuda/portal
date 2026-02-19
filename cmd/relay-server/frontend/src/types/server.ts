// Base server interface that both ClientServer and AdminServer extend
export interface BaseServer {
  id: number;
  name: string;
  description: string;
  tags: string[];
  thumbnail: string;
  owner: string;
  online: boolean;
  dns: string;
  link: string;
  lastUpdated?: string;
  firstSeen?: string;
}

// Client-side server (identical to BaseServer, kept as named alias for clarity)
export type ClientServer = BaseServer;

// Extended BaseServer with admin-specific fields
export interface AdminServer extends BaseServer {
  peerId: string;
  isBanned: boolean;
  bps: number; // bytes-per-second limit (0 = unlimited)
  isApproved: boolean; // whether lease is approved (for manual mode)
  isDenied: boolean; // whether lease is denied (for manual mode)
  ip: string; // client IP address (for IP-based ban)
  isIPBanned: boolean; // whether the IP is banned
}

// Approval mode type
export type ApprovalMode = "auto" | "manual";

// Admin-specific filter for ban status
export type BanFilter = "all" | "banned" | "active";

// Navigation state passed to server detail pages
export interface ServerNavigationState {
  id: number;
  name: string;
  description: string;
  tags: string[];
  thumbnail: string;
  owner: string;
  online: boolean;
  serverUrl: string;
}

// Type guard to distinguish AdminServer from ClientServer
export function isAdminServer(
  server: ClientServer | AdminServer
): server is AdminServer {
  return "peerId" in server;
}
