import type { HelpArea } from "./types";

export const filteringHelp: HelpArea = {
  pages: [
    "pages/FilteringPage.tsx",
    "pages/FilterCategoriesPage.tsx",
    "pages/PoliciesPage.tsx",
    "pages/RewritesPage.tsx",
    "pages/RpzPage.tsx",
  ],
  entries: {
    // Blocklist / allowlist
    "list-name": {
      text: "A name for this list, shown in the table and as the reason for queries it blocks.",
      range: "1–64 characters",
    },
    "list-kind": {
      text: "A block list blocks every domain it contains and all of their subdomains. An allow list adds its domains to the allowlist instead.",
      default: "Block",
      effect: "Rebuilds the filter index on every resolver.",
      topic: "filtering",
      anchor: "blocklists",
    },
    "list-url": {
      text: "The address of a blocklist in hosts, domain-per-line or Adblock format. Nexora downloads it, keeps the last good copy when a download fails, and refreshes it on the interval below.",
      effect:
        "Adding, removing or changing a list rebuilds the filter index on every resolver.",
      topic: "filtering",
      anchor: "blocklists",
    },
    "list-engine-group": {
      text: "The engine group whose resolvers use this list. All engine groups applies it everywhere.",
      default: "All engine groups",
    },
    "list-interval": {
      text: "How often Nexora downloads the list again. A list not refreshed within two intervals is marked stale, and blocking continues with the last good copy.",
      default: "86400 seconds (1 day)",
      range: "300 seconds or more",
      topic: "filtering",
      anchor: "blocklists",
    },
    "list-enabled": {
      text: "Off removes the list from the filtering of clients in no policy group. A policy group that selects the list still uses it.",
      default: "On",
      effect: "Rebuilds the filter index on every resolver.",
    },
    "allowlist-input": {
      text: "A domain that is never blocked, with all of its subdomains, for clients in no policy group. Allowlist entries beat every blocklist and category block; a policy group has its own allowlist.",
      range: "A domain name of at most 253 characters",
      effect: "Saving publishes a new configuration.",
      topic: "filtering",
      anchor: "allowlist",
    },

    // Filter categories
    "categories-search": {
      text: "Shows only the categories whose name, or one of whose sources, matches. Matching categories open while you search. It changes nothing.",
    },
    "category-toggle": {
      text: "Turns every enabled source of this category on or off for clients that are in no policy group. Sources marked not free for commercial use ask for confirmation.",
      default: "Off",
      effect: "Rebuilds the filter index on every resolver.",
      topic: "filtering",
      anchor: "categories-and-licenses",
    },
    "source-toggle": {
      text: "Turns one source of this category on or off. A source blocks only while its category is on, or for a policy group that selects the category. Sources not free for commercial use ask for confirmation.",
      default: "Set per source by the built-in catalog",
      effect: "Rebuilds the filter index on every resolver.",
      topic: "filtering",
      anchor: "categories-and-licenses",
    },

    // Policies
    "safe-search-engine": {
      text: "Answers this search engine's domains with its safe search address, which hides explicit results.",
      default: "Off",
      effect: "Saving publishes a new configuration.",
      topic: "filtering",
      anchor: "safe-search",
    },
    "safe-search-youtube": {
      text: "Moderate and Strict answer YouTube's domains with its restricted mode addresses. Strict hides more videos.",
      default: "Off",
      effect: "Saving publishes a new configuration.",
      topic: "filtering",
      anchor: "safe-search",
    },
    "group-name": {
      text: "A name for the policy group, shown in the table, the rewrites scope and the query log.",
      range:
        "1–63 characters: letters, digits, spaces, _ . and -, starting with a letter or digit",
    },
    "group-description": {
      text: "An optional note for operators. It has no effect on filtering.",
      range: "Up to 500 characters",
    },
    "policygroup-engine-group": {
      text: "The engine group whose resolvers apply this policy group. All engine groups applies it everywhere.",
      default: "All engine groups",
      effect:
        "The group's filter lists must apply to every engine group or to this one.",
      topic: "filtering",
      anchor: "policies",
    },
    "group-cidrs": {
      text: "The client networks this group applies to, one prefix per line, without host bits. A client in several groups gets the group with the most specific prefix.",
      range: "1–1024 prefixes",
      effect:
        "Matching clients get only this group's lists, allowlist, safe search and rewrites, instead of the global ones.",
      topic: "filtering",
      anchor: "policies",
    },
    "group-filter-lists": {
      text: "The custom block lists this group's clients are filtered with. Allow lists and category sources are not listed here.",
      range: "Up to 256 lists",
      effect: "Rebuilds the filter index on every resolver.",
      topic: "filtering",
      anchor: "policies",
    },
    "group-categories": {
      text: "Filter categories whose enabled sources block for this group, whether or not the category is on globally. Sources not free for commercial use ask for confirmation.",
      range: "Up to 64 categories",
      effect: "Rebuilds the filter index on every resolver.",
      topic: "filtering",
      anchor: "categories-and-licenses",
    },
    "group-allowlist": {
      text: "Domains never blocked for this group's clients, one per line; subdomains are allowed too. The global allowlist does not apply to them.",
      range: "Up to 10,000 domains",
      topic: "filtering",
      anchor: "allowlist",
    },

    // Rewrites
    "rewrite-scope-filter": {
      text: "Shows all rewrites, only the global ones, or those of one policy group. It changes only this view.",
      default: "All",
    },
    "rewrite-name": {
      text: "The name answered locally: an exact name such as host.example, or *.example for every name below example. An exact rule beats a wildcard, and the longest wildcard wins.",
      range: "Up to 255 characters",
      topic: "filtering",
      anchor: "rewrites",
    },
    "rewrite-type": {
      text: "The record type of the local answer: an IPv4 address (A), an IPv6 address (AAAA) or another name (CNAME). A CNAME rewrite cannot share its name with A or AAAA rewrites.",
      default: "A",
      topic: "filtering",
      anchor: "rewrites",
    },
    "rewrite-value": {
      text: "The answer: an IPv4 address for A, an IPv6 address for AAAA, or a target name for CNAME.",
      range: "Up to 255 characters",
    },
    "rewrite-ttl": {
      text: "How long clients may cache the local answer, in seconds.",
      default: "300 seconds",
      range: "0–86400 seconds",
    },
    "rewrite-scope": {
      text: "Global rewrites answer clients in no policy group. A policy group's rewrites answer only that group's clients.",
      default: "Global, or the scope selected on the page",
      topic: "filtering",
      anchor: "rewrites",
    },
    "rewrite-engine-group": {
      text: "The engine group whose resolvers serve this global rewrite. A rewrite in a policy group follows the group's engine group.",
      default: "All engine groups",
    },

    // RPZ
    "rpz-name": {
      text: "The name of the response policy zone, as its primary or zone file names it. It cannot be changed after the zone is created.",
      range: "Up to 255 characters",
      topic: "filtering",
      anchor: "rpz",
    },
    "rpz-source": {
      text: "File: you upload the zone in master-file format. Zone transfer: resolvers transfer the zone from a primary server and refresh it on its SOA timers. It cannot be changed later.",
      default: "File",
      topic: "filtering",
      anchor: "rpz",
    },
    "rpz-engine-group": {
      text: "The engine group whose resolvers apply this zone. It is fixed when the zone is created.",
      default: "All engine groups",
    },
    "rpz-primary": {
      text: "The primary server to transfer the zone from, as address:port.",
      topic: "filtering",
      anchor: "rpz",
    },
    "rpz-tsig-algorithm": {
      text: "Signs the zone transfer with a TSIG key shared with the primary. None transfers unsigned.",
      default: "None",
    },
    "rpz-tsig-key-name": {
      text: "The TSIG key name, exactly as the primary knows it.",
    },
    "rpz-tsig-secret": {
      text: "The shared TSIG secret in base64. It is stored encrypted, never shown again, and needs key storage to be configured. Leave it empty when editing to keep the stored secret.",
      range: "16–64 bytes, base64-encoded",
    },
    "rpz-override": {
      text: "Given applies each rule's own action. Disabled only logs matches. NXDOMAIN, NODATA, PASSTHRU, DROP and TCP-only apply that action to every match in this zone.",
      default: "Given",
      topic: "filtering",
      anchor: "rpz",
    },
    "rpz-min-refresh": {
      text: "The shortest refresh and retry interval for a transferred zone: the zone's SOA timers are raised to at least this value.",
      default: "60 seconds",
      range: "1–86400 seconds",
    },
    "rpz-file": {
      text: "A master-file RPZ zone. It replaces the current file; include directives are rejected.",
      effect: "Uploading publishes a new configuration.",
      topic: "filtering",
      anchor: "rpz",
    },
  },
};
