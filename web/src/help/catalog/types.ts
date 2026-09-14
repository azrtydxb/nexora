export type HelpTopic =
  | "resolution"
  | "dnssec"
  | "filtering"
  | "zones"
  | "fleet"
  | "access-control"
  | "users"
  | "observability";

export type HelpEntry = {
  text: string;
  default?: string;
  range?: string;
  effect?: string;
  topic?: HelpTopic;
  anchor?: string;
};

/** One catalogue area: `pages` are the files (relative to web/src) whose controls check-help.mjs checks. */
export type HelpArea = { pages: string[]; entries: Record<string, HelpEntry> };
