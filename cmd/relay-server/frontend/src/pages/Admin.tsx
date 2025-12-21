import { useEffect } from "react";
import { useNavigate } from "react-router-dom";
import { SsgoiTransition } from "@ssgoi/react";
import { useAdmin } from "@/hooks/useAdmin";
import { useAuth } from "@/hooks/useAuth";
import { ServerListView } from "@/components/ServerListView";

export function Admin() {
  const navigate = useNavigate();
  const { isAuthenticated, isLoading: authLoading, logout } = useAuth();

  const {
    filteredServers,
    availableTags,
    searchQuery,
    status,
    sortBy,
    selectedTags,
    banFilter,
    approvalMode,
    favorites,
    loading,
    error,
    handleSearchChange,
    handleStatusChange,
    handleSortByChange,
    handleTagToggle,
    handleBanFilterChange,
    handleToggleFavorite,
    handleBanStatus,
    handleBPSChange,
    handleApprovalModeChange,
    handleApproveStatus,
    handleDenyStatus,
    handleIPBanStatus,
    handleBulkApprove,
    handleBulkDeny,
    handleBulkBan,
  } = useAdmin();

  // Redirect to login if not authenticated
  useEffect(() => {
    if (!authLoading && !isAuthenticated) {
      navigate("/admin/login", { replace: true });
    }
  }, [authLoading, isAuthenticated, navigate]);

  const handleLogout = async () => {
    await logout();
    navigate("/admin/login", { replace: true });
  };

  if (authLoading) {
    return <div className="p-8 text-foreground">Checking authentication...</div>;
  }

  if (!isAuthenticated) {
    return null; // Will redirect
  }

  if (loading) return <div className="p-8 text-foreground">Loading...</div>;
  if (error) return <div className="p-8 text-red-500">Error: {error}</div>;

  return (
    <SsgoiTransition id="admin">
      <ServerListView
        title="PORTAL ADMIN"
        searchQuery={searchQuery}
        status={status}
        sortBy={sortBy}
        selectedTags={selectedTags}
        availableTags={availableTags}
        filteredServers={filteredServers}
        favorites={favorites}
        onSearchChange={handleSearchChange}
        onStatusChange={handleStatusChange}
        onSortByChange={handleSortByChange}
        onTagToggle={handleTagToggle}
        onToggleFavorite={handleToggleFavorite}
        // Admin-specific props
        isAdmin={true}
        banFilter={banFilter}
        approvalMode={approvalMode}
        onBanFilterChange={handleBanFilterChange}
        onBanStatusChange={handleBanStatus}
        onBPSChange={handleBPSChange}
        onApprovalModeChange={handleApprovalModeChange}
        onApproveStatusChange={handleApproveStatus}
        onDenyStatusChange={handleDenyStatus}
        onIPBanStatusChange={handleIPBanStatus}
        onBulkApprove={handleBulkApprove}
        onBulkDeny={handleBulkDeny}
        onBulkBan={handleBulkBan}
        onLogout={handleLogout}
      />
    </SsgoiTransition>
  );
}
