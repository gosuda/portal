import { Link } from "react-router-dom";

interface ServerCardProps {
  serverId: number;
  name: string;
  description: string;
  tags: string[];
  thumbnail: string;
  owner: string;
  online: boolean;
  dns: string;
  serverUrl: string;
  navigationPath: string;
  navigationState: any;
}

export function ServerCard({
  serverId,
  name,
  description,
  tags,
  thumbnail,
  owner,
  online,
  navigationPath,
  navigationState,
}: ServerCardProps) {
  return (
    <Link
      to={navigationPath}
      state={navigationState}
      className="relative hover:scale-105 transition-all duration-300"
    >
      <div
        data-hero-key={`server-bg-${serverId}`}
        className="relative h-[174.5px] bg-center bg-no-repeat bg-cover rounded-xl shadow-lg hover:shadow-xl transition-shadow duration-300 cursor-pointer z-1 border border-foreground/40"
        style={{ ...(thumbnail && { backgroundImage: `url(${thumbnail})` }) }}
      >
        {/* Content overlay - not part of hero transition */}
        <div className="relative h-full w-full bg-background/80 rounded-xl flex flex-col gap-4 p-4 items-start text-start">
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
      <div className="absolute top-2 left-2 h-full w-full bg-secondary/70 rounded-xl z-0" />
    </Link>
  );
}
