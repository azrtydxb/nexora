import { useState } from "react";

export type Theme = "light" | "dark";

const key = "nexora-theme";

function current(): Theme {
  return document.documentElement.classList.contains("dark") ? "dark" : "light";
}

/** The active theme and a toggle that stores the choice (index.html applies it before paint). */
export function useTheme(): [Theme, () => void] {
  const [theme, setTheme] = useState<Theme>(current);
  const toggle = () => {
    const next: Theme = theme === "dark" ? "light" : "dark";
    document.documentElement.classList.toggle("dark", next === "dark");
    try {
      localStorage.setItem(key, next);
    } catch {
      // Storage may be blocked; the theme still applies for this page load.
    }
    setTheme(next);
  };
  return [theme, toggle];
}
