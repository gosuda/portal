import { useCallback, useEffect, useState } from "react";

export interface LeaseMetadata {
  description: string;
  tags: string[];
  thumbnail: string;
  owner: string;
  hide: boolean;
}

export interface LeaseEntryParsed {
  ConnectionID: number;
  Expires: string;
  LastSeen: string;
  Lease: {
    identity: { id: string; public_key: string };
    expires: number;
    name: string;
    alpn: string[];
    metadata: LeaseMetadata;
  };
}

export function useAdmin() {
  const [leases, setLeases] = useState<LeaseEntryParsed[]>([]);
  const [bannedLeases, setBannedLeases] = useState<string[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");

  const fetchData = useCallback(async () => {
    try {
      const [leasesRes, bannedRes] = await Promise.all([
        fetch("/admin/leases"),
        fetch("/admin/leases/banned"),
      ]);

      if (!leasesRes.ok || !bannedRes.ok) {
        throw new Error("Failed to fetch admin data. Are you on localhost?");
      }

      const leasesData = await leasesRes.json();
      const bannedData: string[] = await bannedRes.json();

      setLeases(leasesData || []);
      // bannedData is base64 encoded byte arrays, decode them
      const decodedBanned = (bannedData || []).map((b64: string) => {
        try {
          return atob(b64);
        } catch {
          return b64;
        }
      });
      setBannedLeases(decodedBanned);
    } catch (err: any) {
      setError(err.message);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchData();
  }, [fetchData]);

  const handleBanStatus = async (leaseId: string, isBan: boolean) => {
    try {
      // URL-safe base64 encode the lease ID
      const safeId = btoa(leaseId).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
      await fetch(`/admin/leases/${safeId}/ban`, {
        method: isBan ? "POST" : "DELETE"
      });
      fetchData();
    } catch (err) {
      console.error(err);
    }
  };

  return {
    leases,
    bannedLeases,
    loading,
    error,
    handleBanStatus,
    refresh: fetchData
  };
}
