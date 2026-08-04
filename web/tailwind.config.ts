import type { Config } from "tailwindcss";

// Design tokens lifted verbatim from nucleus/css/styles.css (:root and
// [data-theme="dark"]). Exposed both as Tailwind colour utilities and
// as CSS variables in src/styles/tokens.css so non-Tailwind components
// (CodeMirror, embedded markdown, etc.) can read them too.
const config: Config = {
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  // Component classes built via template literals (`chip chip--${family}`,
  // `check-icon--${tone}`, `histogram__bar--${mod}`) never appear as
  // string literals in source, so Tailwind's content scanner prunes the
  // matching rules from `@layer components`. Safelist their families so
  // the rules survive — keep this list in sync with index.css.
  safelist: [
    "chip",
    "chip--teal",
    "chip--blue",
    "chip--purple",
    "chip--amber",
    "chip--rose",
    "chip--green",
    "chip--slate",
    "check-icon",
    "check-icon--warning",
    "check-icon--error",
    "check-icon--muted",
    "histogram__bar",
    "histogram__bar--peak",
    "histogram__bar--empty",
    "status-pill__dot--success",
    "status-pill__dot--warning",
    "status-pill__dot--error",
    "status-pill__dot--accent",
    "tab-strip__tab--active",
  ],
  darkMode: ["class", '[data-theme="dark"]'],
  theme: {
    extend: {
      colors: {
        // Brand: gopher palette (default accent).
        gopher: {
          cyan: "#00ADD8",
          "cyan-light": "#5DC9E2",
          "cyan-dark": "#007D9C",
          yellow: "#FDDD00",
          pink: "#CE3262",
        },
        // Warm sand neutral scale (light theme chrome).
        sand: {
          50: "#FDFCFA",
          100: "#F5F3EF",
          200: "#E8E4DC",
          300: "#D4CFC4",
          400: "#A8A093",
          500: "#7A7267",
          600: "#5C554B",
          700: "#433D35",
          800: "#2A2520",
          900: "#1A1714",
        },
        // Cool slate neutral scale (dark theme chrome — drops warm tint
        // so the amber/teal status palette reads cleanly on dark).
        slate: {
          50:  "#F8FAFC",
          100: "#F1F5F9",
          200: "#E2E8F0",
          300: "#CBD5E1",
          400: "#94A3B8",
          500: "#64748B",
          600: "#475569",
          700: "#334155",
          800: "#1E293B",
          900: "#0F172A",
        },
        // Semantic tokens — wired via CSS variables so runtime theme
        // changes swap them globally.
        accent: "var(--accent)",
        "accent-hover": "var(--accent-hover)",
        bg: {
          primary: "var(--bg-primary)",
          secondary: "var(--bg-secondary)",
          tertiary: "var(--bg-tertiary)",
        },
        text: {
          primary: "var(--text-primary)",
          secondary: "var(--text-secondary)",
          tertiary: "var(--text-tertiary)",
        },
        border: "var(--border-color)",
        // Flat semantic hues — backed by CSS vars so dark mode swaps them.
        success: "var(--status-success)",
        warning: "var(--status-warning)",
        error: "var(--status-error)",
        // Full status scales (mirror Tailwind defaults, exposed for ad-hoc use).
        amber: {
          50: "#FFFBEB",
          100: "#FEF3C7",
          300: "#FCD34D",
          500: "#F59E0B",
          700: "#B45309",
          900: "#78350F",
        },
        teal: {
          50: "#F0FDFA",
          100: "#CCFBF1",
          300: "#5EEAD4",
          500: "#14B8A6",
          700: "#0F766E",
          900: "#134E4A",
        },
        red: {
          50: "#FEF2F2",
          100: "#FEE2E2",
          500: "#EF4444",
          600: "#DC2626",
          700: "#B91C1C",
        },
        green: {
          50: "#F0FDF4",
          100: "#DCFCE7",
          500: "#22C55E",
          600: "#16A34A",
          700: "#15803D",
        },
        // Semantic component tokens — prefer these in components.
        banner: {
          "warning-bg":     "var(--banner-warning-bg)",
          "warning-border": "var(--banner-warning-border)",
          "warning-icon":   "var(--banner-warning-icon)",
          "warning-title":  "var(--banner-warning-title)",
          "warning-body":   "var(--banner-warning-body)",
          "success-bg":     "var(--banner-success-bg)",
          "success-border": "var(--banner-success-border)",
          "success-icon":   "var(--banner-success-icon)",
          "success-title":  "var(--banner-success-title)",
          "success-body":   "var(--banner-success-body)",
        },
        delta: {
          "negative-bg":   "var(--delta-negative-bg)",
          "negative-text": "var(--delta-negative-text)",
          "positive-bg":   "var(--delta-positive-bg)",
          "positive-text": "var(--delta-positive-text)",
        },
        card: {
          bg:     "var(--card-bg)",
          border: "var(--card-border)",
          label:  "var(--card-label)",
          value:  "var(--card-value)",
        },
        score: {
          good:    "var(--score-good)",
          improve: "var(--score-improve)",
          poor:    "var(--score-poor)",
        },
      },
      fontFamily: {
        sans: [
          "Outfit",
          "-apple-system",
          "BlinkMacSystemFont",
          "Segoe UI",
          "sans-serif",
        ],
        mono: ["JetBrains Mono", "Fira Code", "ui-monospace", "monospace"],
      },
      borderRadius: {
        sm: "4px",
        md: "8px",
        lg: "12px",
        xl: "16px",
      },
      boxShadow: {
        glow: "0 0 20px rgba(0, 173, 216, 0.3)",
      },
      transitionDuration: {
        fast: "150ms",
        base: "250ms",
        slow: "400ms",
      },
    },
  },
  plugins: [],
};

export default config;
