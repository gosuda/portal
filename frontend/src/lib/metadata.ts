import type { Metadata } from "@/hooks/useSSRData";

const EMPTY_METADATA: Metadata = {
  description: "",
  tags: [],
  thumbnail: "",
  owner: "",
  hide: false,
};

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

export function parseLeaseMetadata(metadataJSON: string): Metadata {
  if (!metadataJSON) {
    return EMPTY_METADATA;
  }

  try {
    const parsed = JSON.parse(metadataJSON);
    if (!isRecord(parsed)) {
      return EMPTY_METADATA;
    }

    const rawTags = parsed.tags;
    const tags = Array.isArray(rawTags)
      ? rawTags
          .map((tag) => (typeof tag === "string" ? tag.trim() : ""))
          .filter(Boolean)
      : [];

    return {
      description: typeof parsed.description === "string" ? parsed.description : "",
      tags,
      thumbnail: typeof parsed.thumbnail === "string" ? parsed.thumbnail : "",
      owner: typeof parsed.owner === "string" ? parsed.owner : "",
      hide: typeof parsed.hide === "boolean" ? parsed.hide : false,
    };
  } catch {
    return EMPTY_METADATA;
  }
}
