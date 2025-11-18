import { Button } from "./ui/button";

interface ServerCardProps {
  name: string;
  description: string;
  tags: string[];
  thumbnail: string;
  owner: string;
  online: boolean;
  dns: string;
  serverUrl: string;
}

export function ServerCard({
  name,
  description,
  tags,
  thumbnail,
  owner,
  online,
  serverUrl,
}: ServerCardProps) {
  const defaultThumbnail =
    "https://cdn.jsdelivr.net/gh/gosuda/portal@main/portal.jpg";

  const goServer = () => {
    window.location.href = serverUrl;
  };
  return (
    <div className="flex flex-col gap-4 rounded-xl bg-card-dark p-4 shadow-sm hover:shadow-lg transition-shadow duration-300">
      <div
        className="w-full bg-center bg-no-repeat aspect-video bg-cover rounded-lg"
        style={{ backgroundImage: `url(${thumbnail || defaultThumbnail})` }}
      />
      <div className="flex flex-1 flex-col justify-between gap-4">
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
          <p className="text-white text-lg font-bold leading-tight">{name}</p>
          {description && (
            <p className="text-text-muted text-sm font-normal leading-normal line-clamp-2">
              {description}
            </p>
          )}
          {tags && tags.length > 0 && (
            <div className="flex flex-wrap gap-1.5 mt-1">
              {tags.map((tag, index) => (
                <span
                  key={index}
                  className="px-2 py-1 bg-border-dark text-primary text-xs font-medium rounded"
                >
                  {tag}
                </span>
              ))}
            </div>
          )}
          {owner && (
            <p className="text-text-muted text-xs font-normal leading-normal">
              by {owner}
            </p>
          )}
        </div>
        <Button variant="secondary" className="w-full" onClick={goServer}>
          <span className="truncate">Connect</span>
        </Button>
      </div>
    </div>
  );
}
