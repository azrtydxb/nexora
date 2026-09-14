import type { Schemas } from "@/api/client";
import { useCurrentUser } from "@/auth/AuthProvider";

export type Preferences = Schemas["UserPreferences"];

export const defaultPreferences: Preferences = {
  theme: "system",
  time_zone: "",
  clock_24h: false,
  querylog_live: true,
};

/** The signed-in user's preferences, or the defaults before the profile has loaded. */
export function usePreferences(): Preferences {
  const { user } = useCurrentUser();
  return { ...defaultPreferences, ...(user?.preferences ?? {}) };
}

/** A timestamp in the user's time zone and clock; an unknown zone falls back to the browser's. */
export function formatTimestamp(
  d: Date,
  p: Preferences,
  withDate = false,
): string {
  const opts: Intl.DateTimeFormatOptions = {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: !p.clock_24h,
    ...(withDate ? { year: "numeric", month: "2-digit", day: "2-digit" } : {}),
  };
  try {
    return new Intl.DateTimeFormat(undefined, {
      ...opts,
      timeZone: p.time_zone || undefined,
    }).format(d);
  } catch {
    return new Intl.DateTimeFormat(undefined, opts).format(d);
  }
}
