import { useEffect, useSyncExternalStore } from "react";
import { useQueryClient } from "@tanstack/react-query";

import { api } from "@/api/client";
import { useCurrentUser } from "@/auth/AuthProvider";

export type Theme = "light" | "dark";
export type ThemePreference = "system" | "light" | "dark";

const key = "nexora-theme";
const darkQuery = "(prefers-color-scheme: dark)";

function current(): Theme {
  return document.documentElement.classList.contains("dark") ? "dark" : "light";
}

/** Re-renders subscribers whenever the root element's class (the dark flag) changes. */
function subscribe(onChange: () => void) {
  const observer = new MutationObserver(onChange);
  observer.observe(document.documentElement, { attributeFilter: ["class"] });
  return () => observer.disconnect();
}

function store(pref: ThemePreference) {
  try {
    if (pref === "system") localStorage.removeItem(key);
    else localStorage.setItem(key, pref);
  } catch {
    // Storage may be blocked; the theme still applies for this page load.
  }
}

/** Applies a profile theme (`system` follows prefers-color-scheme) and stores it for the pre-paint script in index.html. */
export function applyThemePreference(pref: ThemePreference) {
  const dark =
    pref === "dark" || (pref === "system" && matchMedia(darkQuery).matches);
  document.documentElement.classList.toggle("dark", dark);
  store(pref);
}

/**
 * The active theme and a toggle. The signed-in user's `preferences.theme` applies when the profile
 * loads or changes; the toggle applies at once and saves the preference when the user is loaded
 * (best effort: a failed save keeps the local toggle).
 */
export function useTheme(): [Theme, () => void] {
  const theme = useSyncExternalStore(subscribe, current);
  const { user } = useCurrentUser();
  const qc = useQueryClient();
  const pref = user?.preferences.theme;

  useEffect(() => {
    if (!pref) return;
    applyThemePreference(pref);
    if (pref !== "system") return;
    const mq = matchMedia(darkQuery);
    const onChange = () => applyThemePreference("system");
    mq.addEventListener("change", onChange);
    return () => mq.removeEventListener("change", onChange);
  }, [pref]);

  const toggle = () => {
    const next: Theme = theme === "dark" ? "light" : "dark";
    applyThemePreference(next);
    if (!user) return;
    void api
      .PUT("/auth/me", {
        body: {
          revision: user.revision,
          preferences: { ...user.preferences, theme: next },
        },
      })
      .then((r) => {
        if (r.data) qc.setQueryData(["me"], r.data);
      })
      .catch(() => {
        // Best effort: the local toggle stays when the preference cannot be saved.
      });
  };
  return [theme, toggle];
}
