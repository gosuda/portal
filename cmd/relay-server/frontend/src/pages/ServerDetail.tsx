import { SsgoiTransition } from "@ssgoi/react";
import { useEffect } from "react";
import { useLocation, useNavigate } from "react-router-dom";

interface ServerDetailState {
  id: number;
  name: string;
  description: string;
  tags: string[];
  thumbnail: string;
  owner: string;
  online: boolean;
  serverUrl: string;
}

export function ServerDetail() {
  const location = useLocation();
  const navigate = useNavigate();
  const server = location.state as ServerDetailState;

  const isPush = () => localStorage.getItem("isPush") === "true";

  useEffect(() => {
    const handlePageShow = () => {
      if (isPush()) {
        localStorage.removeItem("isPush");
        navigate("/");
      }
    };

    window.addEventListener("pageshow", handlePageShow);

    return () => {
      window.removeEventListener("pageshow", handlePageShow);
    };
  }, []);

  useEffect(() => {
    if (typeof localStorage === "undefined") return;

    // Redirect to actual server URL after animation
    const timer = setTimeout(() => {
      if (isPush()) {
        localStorage.removeItem("isPush");
        navigate("/");
      } else {
        localStorage.setItem("isPush", "true");
        window.location.href = server.serverUrl;
      }
    }, 300);

    return () => {
      clearTimeout(timer);
    };
  }, [server, navigate]);

  // If no server data, show nothing (will redirect)
  if (!server) {
    return null;
  }

  const { id, thumbnail, name, online, description, tags, owner } = server;

  const defaultThumbnail =
    "https://cdn.jsdelivr.net/gh/gosuda/portal@main/portal.jpg";

  return (
    <SsgoiTransition id={`/server/${id}`}>
      <div
        data-hero-key={`server-bg-${id}`}
        className="fixed inset-0 bg-center bg-no-repeat bg-cover w-screen h-screen"
        style={{ backgroundImage: `url(${thumbnail || defaultThumbnail})` }}
      >
        {/* Content overlay - not part of hero transition */}
        <div className="absolute top-1/2 left-1/2 -translate-x-1/2 -translate-y-1/2 h-[174.5px] w-full min-[500px]:w-[50%] md:w-[33.33%] bg-background/70 rounded-xl flex flex-col gap-4 p-4 items-start text-start z-1">
          <div className="w-full flex flex-1 flex-col justify-between gap-4">
            <div className="flex flex-col gap-2">
              <div className="flex items-center gap-2">
                <div
                  className={`w-2.5 h-2.5 rounded-full ${
                    online ? "bg-green-status" : "bg-red-500"
                  }`}
                />
                <p
                  className={`text-sm font-medium leading-normal ${
                    online ? "text-green-status" : "text-red-500"
                  }`}
                >
                  {online ? "Online" : "Offline"}
                </p>
              </div>
              <p className="text-foreground text-lg font-bold leading-tight truncate max-w-full">
                {name}
              </p>
              {description && (
                <p className="text-text-muted text-sm font-normal leading-normal truncate max-w-full">
                  {description}
                </p>
              )}
              {tags && tags.length > 0 && (
                <div className="flex flex-wrap gap-1.5 mt-1">
                  {tags.map((tag, index) => (
                    <span
                      key={index}
                      className="px-2 py-1 bg-secondary text-primary text-xs font-medium rounded truncate max-w-[120px]"
                    >
                      {tag}
                    </span>
                  ))}
                </div>
              )}
              {owner && (
                <p className="text-text-muted text-xs font-normal leading-normal truncate max-w-full">
                  by {owner}
                </p>
              )}
            </div>
          </div>
        </div>
      </div>
    </SsgoiTransition>
  );
}
