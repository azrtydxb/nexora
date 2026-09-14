import type { HelpTopic } from "../catalog/types";
import resolution from "./resolution.md?raw";
import dnssec from "./dnssec.md?raw";
import filtering from "./filtering.md?raw";
import zones from "./zones.md?raw";
import fleet from "./fleet.md?raw";
import accessControl from "./access-control.md?raw";
import users from "./users.md?raw";
import observability from "./observability.md?raw";

export const topics: { key: HelpTopic; title: string; body: string }[] = [
  { key: "resolution", title: "Forwarding & recursion", body: resolution },
  { key: "dnssec", title: "DNSSEC", body: dnssec },
  { key: "filtering", title: "Filtering", body: filtering },
  { key: "zones", title: "Authoritative zones", body: zones },
  { key: "fleet", title: "Fleet", body: fleet },
  { key: "access-control", title: "Access control", body: accessControl },
  { key: "users", title: "Users and API tokens", body: users },
  {
    key: "observability",
    title: "Query log and observability",
    body: observability,
  },
];
