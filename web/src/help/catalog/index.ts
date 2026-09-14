import { adminHelp } from "./admin";
import { filteringHelp } from "./filtering";
import { fleetHelp } from "./fleet";
import { resolverHelp } from "./resolver";
import type { HelpArea, HelpEntry } from "./types";
import { zonesHelp } from "./zones";

export type { HelpArea, HelpEntry, HelpTopic } from "./types";

export const areas: Record<string, HelpArea> = {
  resolver: resolverHelp,
  filtering: filteringHelp,
  zones: zonesHelp,
  fleet: fleetHelp,
  admin: adminHelp,
};

function merge(): Record<string, HelpEntry> {
  const all: Record<string, HelpEntry> = {};
  for (const [area, { entries }] of Object.entries(areas)) {
    for (const [id, entry] of Object.entries(entries)) {
      if (id in all)
        throw new Error(`help catalogue: duplicate id "${id}" (area ${area})`);
      all[id] = entry;
    }
  }
  return all;
}

/** Every help entry by field id; a duplicate id across areas throws at module load. */
export const help: Record<string, HelpEntry> = merge();

export function helpFor(id: string): HelpEntry | undefined {
  return Object.hasOwn(help, id) ? help[id] : undefined;
}
