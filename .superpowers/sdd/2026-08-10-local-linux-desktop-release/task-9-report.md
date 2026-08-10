# Task 9 report — receipt and architecture evidence remediation

## Implemented

- Versioned target receipts at schema version 2.  Each distributable entry now
  contains its target, format, canonical path, SHA-256, byte size, and the
  identity observed while inspecting that exact package.
- The release inventory gate recomputes each package's digest and size from
  disk, rejects unexpected receipt entries, and compares every entry's target,
  path, format, and identity against the release target.
- Package inspection proves the Electron executable and bundled server binary
  architecture from ELF/Mach-O headers.  Linux requires the selected target's
  exact ELF architecture; macOS requires both universal Mach-O slices.
- Linux server GOARCH and DEB control Architecture are checked against the
  selected package target.

## Regression coverage

- Artifact mutation after receipt generation fails on both digest and size.
- A receipt with current AppImage evidence and stale DEB identity fails.
- Arm64 identities paired with amd64 server GOARCH, wrong ELF architectures,
  incomplete universal Mach-O slices, and wrong DEB control architecture fail.

## Verification

- `npm test --workspace desktop` — 125 files / 1604 tests passed.
- `npm run test:security` — 8 files / 84 tests passed.
- `npm run check` — typecheck, lint, formatting, and API drift passed.
