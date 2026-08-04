// Desktop release-version stamping shared by package-build.mjs (which decides
// whether to override electron-builder's package.json version) and
// prepare-server.mjs (which records the same value as desktop_version in
// build-identity.json). Pure functions only, so tag-parsing stays
// unit-testable without a git checkout.

/**
 * Map `git describe --tags --exact-match` output to the desktop package
 * version. Returns the bare MAJOR.MINOR.PATCH (leading "v" stripped) for a
 * clean release tag, or null for anything else — prerelease suffixes,
 * arbitrary tag names, empty output — so callers fall back to the static
 * development version in package.json. Mirrors the updater's parseSemver and
 * the CLI's parseReleaseVersion: only versions every consumer can order get
 * stamped.
 */
export function desktopVersionFromExactTag(tagOutput) {
  if (typeof tagOutput !== 'string') return null;
  const match = /^v?(\d+\.\d+\.\d+)$/.exec(tagOutput.trim());
  return match === null ? null : match[1];
}
