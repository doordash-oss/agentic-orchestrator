import type { SVGProps } from 'react';

/**
 * Minimal inline icons in the Lucide visual style (24px grid, 2px stroke).
 * Kept local to avoid a runtime icon dependency in a sandboxed app.
 */
function Icon({ children, ...props }: SVGProps<SVGSVGElement>) {
  return (
    <svg
      width="16"
      height="16"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      focusable="false"
      {...props}
    >
      {children}
    </svg>
  );
}

export function MaximizeIcon(props: SVGProps<SVGSVGElement>) {
  return (
    <Icon {...props}>
      <path d="M8 3H5a2 2 0 0 0-2 2v3" />
      <path d="M21 8V5a2 2 0 0 0-2-2h-3" />
      <path d="M3 16v3a2 2 0 0 0 2 2h3" />
      <path d="M16 21h3a2 2 0 0 0 2-2v-3" />
    </Icon>
  );
}

/**
 * The pinned Overview row's leading mark, shaped after SF Symbols' `house`.
 * Overview is the one row that is a place rather than a feature, so it carries
 * a glyph where feature rows carry a status dot.
 */
export function HouseIcon(props: SVGProps<SVGSVGElement>) {
  return (
    <Icon {...props}>
      <path d="M3 10.5 12 4l9 6.5" />
      <path d="M5.5 12.5V20h13v-7.5" />
    </Icon>
  );
}

export function MinimizeIcon(props: SVGProps<SVGSVGElement>) {
  return (
    <Icon {...props}>
      <path d="M8 3v3a2 2 0 0 1-2 2H3" />
      <path d="M21 8h-3a2 2 0 0 1-2-2V3" />
      <path d="M3 16h3a2 2 0 0 1 2 2v3" />
      <path d="M16 21v-3a2 2 0 0 1 2-2h3" />
    </Icon>
  );
}

/** Disclosure chevron for split buttons and menus, shaped after SF Symbols' `chevron.down`. */
export function ChevronDownIcon(props: SVGProps<SVGSVGElement>) {
  return (
    <Icon {...props}>
      <path d="M6 9l6 6 6-6" />
    </Icon>
  );
}

/** The toolbar attention trigger, shaped after SF Symbols' `bell`. */
export function BellIcon(props: SVGProps<SVGSVGElement>) {
  return (
    <Icon {...props}>
      <path d="M18 8a6 6 0 0 0-12 0c0 4.5-1.5 6-2 6.5h16c-.5-.5-2-2-2-6.5" />
      <path d="M10.3 18.5a2 2 0 0 0 3.4 0" />
    </Icon>
  );
}

/** The transient toolbar update trigger, shaped after SF Symbols' `arrow.down.circle`. */
export function UpdateIcon(props: SVGProps<SVGSVGElement>) {
  return (
    <Icon {...props}>
      <circle cx="12" cy="12" r="9" />
      <path d="M12 7.5v9" />
      <path d="m8.5 13 3.5 3.5 3.5-3.5" />
    </Icon>
  );
}

export function CloseIcon(props: SVGProps<SVGSVGElement>) {
  return (
    <Icon {...props}>
      <path d="M18 6 6 18" />
      <path d="m6 6 12 12" />
    </Icon>
  );
}

/* --- Settings pane glyphs -------------------------------------------------
 * One per pane in the Settings window's source list, in the same 24px/2px
 * vocabulary as the icons above so the list reads as part of the same set.
 */

/** Workspace roots, shaped after SF Symbols' `folder`. */
export function FolderIcon(props: SVGProps<SVGSVGElement>) {
  return (
    <Icon {...props}>
      <path d="M3 8.5A2 2 0 0 1 5 6.5h3.6a2 2 0 0 1 1.5.7l1 1.3H19a2 2 0 0 1 2 2v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2Z" />
    </Icon>
  );
}

/** Servers, shaped after SF Symbols' `server.rack`. */
export function ServersIcon(props: SVGProps<SVGSVGElement>) {
  return (
    <Icon {...props}>
      <rect x="3.5" y="4.5" width="17" height="6.5" rx="1.5" />
      <rect x="3.5" y="13" width="17" height="6.5" rx="1.5" />
      <path d="M7 7.75h.01M7 16.25h.01" />
    </Icon>
  );
}

/** Providers, shaped after SF Symbols' `cpu`. */
export function ProvidersIcon(props: SVGProps<SVGSVGElement>) {
  return (
    <Icon {...props}>
      <rect x="8" y="8" width="8" height="8" rx="1.5" />
      <path d="M12 4v4M12 16v4M4 12h4M16 12h4" />
    </Icon>
  );
}

/** Appearance, shaped after SF Symbols' `circle.lefthalf.filled`. */
export function AppearanceIcon(props: SVGProps<SVGSVGElement>) {
  return (
    <Icon {...props}>
      <circle cx="12" cy="12" r="8.5" />
      <path d="M12 3.5a8.5 8.5 0 0 0 0 17Z" fill="currentColor" stroke="none" />
    </Icon>
  );
}

/** Diagnostics, shaped after SF Symbols' `waveform.path`. */
export function DiagnosticsIcon(props: SVGProps<SVGSVGElement>) {
  return (
    <Icon {...props}>
      <path d="M3 12h3l2.5-6 3 12L14 9l2 3h5" />
    </Icon>
  );
}

/** Advanced, shaped after SF Symbols' `slider.horizontal.3`. */
export function AdvancedIcon(props: SVGProps<SVGSVGElement>) {
  return (
    <Icon {...props}>
      <path d="M4 7h16M4 12h16M4 17h16" />
      <circle cx="9" cy="7" r="2" />
      <circle cx="15" cy="12" r="2" />
      <circle cx="8" cy="17" r="2" />
    </Icon>
  );
}

/** Workspace defaults, shaped after SF Symbols' `list.bullet.rectangle`. */
export function DefaultsIcon(props: SVGProps<SVGSVGElement>) {
  return (
    <Icon {...props}>
      <rect x="3" y="4.5" width="18" height="15" rx="2" />
      <path d="M7.5 9.5h.01M11 9.5h5.5M7.5 14.5h.01M11 14.5h5.5" />
    </Icon>
  );
}
