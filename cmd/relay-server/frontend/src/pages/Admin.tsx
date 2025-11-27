import { useAdmin } from "@/hooks/useAdmin";
import { SsgoiTransition } from "@ssgoi/react";

export function Admin() {
  const { leases, bannedLeases, loading, error, handleBanStatus } = useAdmin();

  if (loading) return <div className="p-8 text-foreground">Loading...</div>;
  if (error) return <div className="p-8 text-red-500">Error: {error}</div>;

  return (
    <SsgoiTransition id="admin">
      <div className="min-h-screen bg-background p-8 text-foreground">
        <h1 className="text-3xl font-bold mb-8">Admin Dashboard</h1>

        <div className="mb-12">
          <h2 className="text-2xl font-semibold mb-4">Active Leases</h2>
          <div className="grid gap-4">
            {leases.map((entry, i) => {
              const id = entry.Lease.identity.id;
              const isBanned = bannedLeases.includes(id);
              return (
                <div key={i} className="bg-card p-4 rounded-lg flex justify-between items-center border border-border">
                  <div>
                    <p className="font-bold text-lg">{entry.Lease.name || "(Unnamed)"}</p>
                    <p className="text-sm text-muted-foreground font-mono break-all">{id}</p>
                    <p className="text-xs text-muted-foreground">Expires: {new Date(entry.Expires).toLocaleString()}</p>
                  </div>
                  <button
                    onClick={() => handleBanStatus(id, !isBanned)}
                    className={`px-4 py-2 rounded font-medium transition-colors ml-4 ${
                      isBanned 
                        ? "bg-green-600 hover:bg-green-700 text-white" 
                        : "bg-red-600 hover:bg-red-700 text-white"
                    }`}
                  >
                    {isBanned ? "Unban" : "Ban"}
                  </button> 
                </div>
              );
            })}
            {leases.length === 0 && <p className="text-muted-foreground">No active leases.</p>}
          </div>
        </div>

        <div>
            <h2 className="text-2xl font-semibold mb-4">Banned Leases (All)</h2>
            <div className="grid gap-4">
                {bannedLeases.map((id, i) => (
                    <div key={i} className="bg-card p-4 rounded-lg flex justify-between items-center border border-border">
                        <p className="font-mono text-sm break-all">{id}</p>
                        <button
                            onClick={() => handleBanStatus(id, false)}
                            className="bg-green-600 hover:bg-green-700 text-white px-4 py-2 rounded font-medium transition-colors ml-4"
                        >
                            Unban
                        </button>
                    </div>
                ))}
                {bannedLeases.length === 0 && <p className="text-muted-foreground">No banned leases.</p>}
            </div>
        </div>
      </div>
    </SsgoiTransition>
  );
}

