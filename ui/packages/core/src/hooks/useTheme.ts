import { useCallback, useEffect, useState } from "react";

export type Theme = "light" | "dark";

const STORAGE_KEY = "sockerless:theme";

function readInitialTheme(fallback: Theme): Theme {
  if (typeof window === "undefined") return fallback;
  const stored = window.localStorage.getItem(STORAGE_KEY);
  if (stored === "light" || stored === "dark") return stored;
  // Honour an explicit OS preference in either direction; only when the
  // OS expresses none do we use the caller's fallback. Operator tools pass
  // "dark" (the brutalist design-system default); bleephub passes "light"
  // to match GitHub's own light-first default.
  if (window.matchMedia("(prefers-color-scheme: light)").matches) return "light";
  if (window.matchMedia("(prefers-color-scheme: dark)").matches) return "dark";
  return fallback;
}

function applyTheme(theme: Theme) {
  if (typeof document === "undefined") return;
  const root = document.documentElement;
  if (theme === "dark") root.classList.add("dark");
  else root.classList.remove("dark");
  root.style.colorScheme = theme;
}

/**
 * useTheme reads the current theme + lets callers flip it.
 *
 * Resolution order on first mount: localStorage → prefers-color-scheme
 * media query → `defaultTheme` (caller-supplied; "dark" for operator
 * tools, "light" for bleephub). Once the user picks a theme it persists
 * until they pick the other one.
 */
export function useTheme(
  defaultTheme: Theme = "dark",
): { theme: Theme; setTheme: (t: Theme) => void; toggle: () => void } {
  const [theme, setThemeState] = useState<Theme>(() => readInitialTheme(defaultTheme));

  // Apply on mount + whenever theme changes. The initial apply matters
  // because tokens.css's `.dark` class is the only switch the design
  // system listens to.
  useEffect(() => {
    applyTheme(theme);
  }, [theme]);

  const setTheme = useCallback((next: Theme) => {
    if (typeof window !== "undefined") {
      window.localStorage.setItem(STORAGE_KEY, next);
    }
    setThemeState(next);
  }, []);

  const toggle = useCallback(() => {
    setTheme(theme === "dark" ? "light" : "dark");
  }, [theme, setTheme]);

  return { theme, setTheme, toggle };
}
