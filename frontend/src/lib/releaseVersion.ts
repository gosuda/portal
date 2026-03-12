export const RELEASE_VERSION_META_NAME = "portal-release-version";

export function getReleaseVersion(doc?: Document): string {
  const targetDoc =
    doc ?? (typeof document !== "undefined" ? document : undefined);
  if (!targetDoc) {
    return "";
  }

  return (
    targetDoc
      .querySelector<HTMLMetaElement>(
        `meta[name="${RELEASE_VERSION_META_NAME}"]`
      )
      ?.content.trim() || ""
  );
}
