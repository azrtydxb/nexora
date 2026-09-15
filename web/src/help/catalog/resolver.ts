import type { HelpArea } from "./types";

export const resolverHelp: HelpArea = {
  pages: [
    "pages/UpstreamsPage.tsx",
    "pages/ResolutionSection.tsx",
    "pages/ForwardZonesSection.tsx",
    "pages/SettingsPage.tsx",
    "pages/DnssecPage.tsx",
    "pages/AccessControlPage.tsx",
  ],
  entries: {
    // Forwarding & recursion: resolution mode
    "resolution-mode": {
      text: "Forward sends names that no hosted zone answers to the upstream forwarders below. Recursive resolves them from the root servers, which needs direct outbound UDP and TCP port 53.",
      default: "Forward",
      effect:
        "Saving publishes a new configuration and clears the cache. In forward mode, add at least one upstream first.",
      topic: "resolution",
      anchor: "resolution-mode",
    },
    "resolution-qname-min": {
      text: "In recursive mode, sends each server only as much of the name as it needs for its referral, instead of the full query name.",
      default: "On",
      effect: "Improves privacy towards the root and TLD servers.",
      topic: "resolution",
      anchor: "qname-minimisation",
    },
    "resolution-aggressive-nsec": {
      text: "In recursive mode, answers names that validated NSEC records already prove do not exist straight from the cache, without asking the authoritative servers again (RFC 8198).",
      default: "Off",
      effect:
        "Fewer queries to authoritative servers for names that do not exist. Needs DNSSEC validation.",
      topic: "resolution",
      anchor: "resolution-mode",
    },
    "resolution-max-queries": {
      text: "The most queries recursion may send to authoritative servers while answering one client query. A query that needs more gets SERVFAIL.",
      default: "100",
      range: "1–1000",
      topic: "resolution",
      anchor: "resolution-mode",
    },
    "resolution-max-depth": {
      text: "The most delegations recursion follows from the root for one name. Deeper chains get SERVFAIL.",
      default: "32",
      range: "1–64",
      topic: "resolution",
      anchor: "resolution-mode",
    },
    "resolution-authority-port": {
      text: "The port recursion queries on authoritative servers. Keep 53 outside test labs.",
      default: "53",
      range: "1–65535",
      topic: "resolution",
      anchor: "resolution-mode",
    },
    "resolution-root-hints": {
      text: "The root servers recursion starts from: a name and its IPv4 and IPv6 addresses per row. Leave empty for the built-in IANA root servers.",
      default: "Empty (built-in IANA root servers)",
      topic: "resolution",
      anchor: "resolution-mode",
    },

    // Forwarding & recursion: forward zones
    "forward-zone-domain": {
      text: "Queries for this domain and all its subdomains go to the servers below, in forward and in recursive mode.",
      topic: "resolution",
      anchor: "forward-zones",
    },
    "forward-zone-servers": {
      text: "The servers that answer this domain, as ip:port, separated by commas and tried in order.",
      range: "1–16 servers",
      topic: "resolution",
      anchor: "forward-zones",
    },
    "forward-zone-validate": {
      text: "Checks DNSSEC signatures on answers from these servers, so bogus answers become SERVFAIL. Turn it on when the servers return signed data that chains to a trust anchor.",
      default: "Off",
      effect: "Needs DNSSEC validation turned on.",
      topic: "dnssec",
      anchor: "validation",
    },
    "forwardzone-engine-group": {
      text: "Which engine group uses this forward zone. All engine groups applies it everywhere.",
      default: "All engine groups",
      topic: "fleet",
      anchor: "engine-groups",
    },
    "forwardzones-col-dnssec": {
      text: "Validated means answers from this forward zone's servers are DNSSEC-checked; Off means they are passed on as received.",
      topic: "dnssec",
      anchor: "validation",
    },

    // Forwarding & recursion: upstream forwarders
    "upstream-name": {
      text: "A name for this upstream, shown in lists, the query log and metrics. Names are unique.",
      range: "1–64 characters",
      topic: "resolution",
      anchor: "upstreams",
    },
    "upstream-protocol": {
      text: "How queries reach this upstream: plain UDP (retried over TCP when the answer is truncated), TCP, DNS over TLS or DNS over HTTPS.",
      default: "udp",
      effect:
        "DoT and DoH encrypt queries on the way to the upstream and add a little latency per new connection.",
      topic: "resolution",
      anchor: "upstreams",
    },
    "upstream-doh-url": {
      text: "The upstream's DNS over HTTPS address. It must start with https://.",
      topic: "resolution",
      anchor: "upstreams",
    },
    "upstream-address": {
      text: "The upstream's IP address and port, for example 192.0.2.10:53, or port 853 for DNS over TLS.",
      topic: "resolution",
      anchor: "upstreams",
    },
    "upstream-tls-name": {
      text: "The name in the upstream's TLS certificate, checked on every DNS over TLS connection.",
      topic: "resolution",
      anchor: "upstreams",
    },
    "upstream-timeout": {
      text: "How long one attempt at this upstream may take before the next upstream is tried. A client query never waits longer than 2 seconds in total.",
      default: "250 ms",
      range: "50–5000 ms",
      effect:
        "Three failures in a row mark the upstream down for 5 seconds, then one probe query is let through.",
      topic: "resolution",
      anchor: "upstreams",
    },
    "upstream-engine-group": {
      text: "Which engine group forwards to this upstream. All engine groups applies it everywhere; a group in override mode uses only its own upstreams.",
      default: "All engine groups",
      topic: "fleet",
      anchor: "engine-groups",
    },
    "upstream-enabled": {
      text: "Disabled upstreams stay in the list but receive no queries.",
      default: "On",
      topic: "resolution",
      anchor: "upstreams",
    },
    "upstreams-col-position": {
      text: "The upstream's place in the list. The Ordered strategy uses the first healthy upstream in this order.",
      topic: "resolution",
      anchor: "strategy",
    },

    // Settings: upstream selection
    "settings-strategy": {
      text: "How Nexora picks among the enabled upstreams in forward mode. Ordered tries them in list order, Fastest tries the lowest measured round-trip time first, Parallel sends each uncached query to several upstreams at once and uses the first valid answer.",
      default: "Ordered",
      effect:
        "Parallel multiplies upstream load and shares every query with each raced provider. Changing the strategy publishes a new configuration and clears the cache.",
      topic: "resolution",
      anchor: "strategy",
    },
    "settings-parallel-max": {
      text: "How many upstreams one uncached query is raced across, lowest round-trip time first.",
      default: "0 (every healthy upstream, at most 8)",
      range: "0–8",
      topic: "resolution",
      anchor: "parallel",
    },

    // Settings: cache
    "settings-cache_max_bytes": {
      text: "The memory each resolver may use for cached answers, in bytes. When it is full, older and less used answers make room for new ones.",
      default: "268435456 (256 MiB)",
      range: "At least 1048576 (1 MiB)",
      effect:
        "A larger cache answers more queries without upstream traffic; keep the resolver's memory limit above it.",
    },
    "settings-cache_min_ttl": {
      text: "Answers are cached for at least this many seconds, even when their TTL is shorter.",
      default: "0 (use the answer's TTL)",
      range: "0 or more seconds",
      effect:
        "Raising it lowers upstream traffic but keeps changed records stale for longer.",
    },
    "settings-cache_max_ttl": {
      text: "Answers are cached for at most this many seconds, even when their TTL is longer.",
      default: "86400 (1 day)",
      range: "At least the minimum TTL",
    },
    "settings-cache_negative_max_ttl": {
      text: "The longest time, in seconds, that NXDOMAIN and empty answers stay cached.",
      default: "3600 (1 hour)",
      range: "0 or more seconds",
      effect:
        "Lower values make newly created names visible sooner after a lookup failed.",
    },
    "settings-cache_stale_window": {
      text: "How many seconds after expiry a cached answer may still be served, and only when fresh resolution fails.",
      default: "86400 (1 day)",
      range: "0 (never serve stale) or more seconds",
      effect: "Keeps names resolving through short upstream outages.",
    },

    // Settings: blocking
    "settings-block-mode": {
      text: "The answer a client gets for a blocked name: a null address (0.0.0.0 or ::), NXDOMAIN (the name does not exist) or REFUSED.",
      default: "Null address",
      effect: "Applies to every blocklist, filter category and policy block.",
      topic: "filtering",
      anchor: "blocklists",
    },
    "settings-block_ttl": {
      text: "The TTL, in seconds, on blocked answers, which tells clients how long to remember them.",
      default: "60",
      range: "0 or more seconds",
      effect:
        "Lower values make an unblocked name reachable sooner on clients that cache answers.",
      topic: "filtering",
      anchor: "blocklists",
    },

    // Settings: telemetry
    "settings-otlp-endpoint": {
      text: "The OpenTelemetry collector that receives metrics and traces, for example http://otel-collector:4317. An engine group's own endpoint replaces it.",
      default: "Empty (the installation's default endpoint, if any)",
      effect:
        "An unreachable collector loses telemetry but never slows DNS answers.",
      topic: "observability",
      anchor: "traces",
    },
    "settings-trace_sample_one_in": {
      text: "Records a trace for one query in this many. SERVFAIL answers and slow queries are always traced.",
      default: "0 (no sampling)",
      range: "0 or more",
      effect: "Keep it at 0 or a large number on busy resolvers.",
      topic: "observability",
      anchor: "traces",
    },
    "settings-trace_slow_threshold_us": {
      text: "Queries that take longer than this many microseconds are always traced.",
      default: "100000 (100 ms)",
      range: "0 or more microseconds",
      topic: "observability",
      anchor: "traces",
    },

    // DNSSEC
    "dnssec-validation": {
      text: "Checks signatures on answers found by recursion and by forward zones with validation on. Answers whose signatures do not verify are bogus and become SERVFAIL.",
      default: "On",
      effect: "Turning it off passes every answer on unchecked.",
      topic: "dnssec",
      anchor: "validation",
    },
    "dnssec-validate-forwarded": {
      text: "Also validates answers from the upstream forwarders in forward mode, up to the root trust anchor. Works only while Validate answers is on.",
      default: "On",
      effect:
        "Upstreams that strip signatures make signed domains fail with SERVFAIL.",
      topic: "dnssec",
      anchor: "validation",
    },
    "dnssec-rfc5011": {
      text: "Follows root key rollovers automatically (RFC 5011): a new root key is trusted after a 30-day hold-down, and a revoked key is dropped after 30 days.",
      default: "On",
      effect:
        "When off, only the configured trust anchors are trusted, so a root key rollover needs a new trust anchor.",
      topic: "dnssec",
      anchor: "trust-anchors",
    },
    "trust-anchor-zone": {
      text: "The zone whose key this DS record identifies, for example example. for a private signed hierarchy.",
      topic: "dnssec",
      anchor: "trust-anchors",
    },
    "trust-anchor-ds": {
      text: "The zone's DS record in presentation format: key tag, algorithm, digest type and hex digest.",
      range: "Up to 200 characters",
      topic: "dnssec",
      anchor: "trust-anchors",
    },
    "trustanchors-col-source": {
      text: "IANA anchors are the built-in root keys, kept up to date automatically. Operator anchors were added here.",
      topic: "dnssec",
      anchor: "trust-anchors",
    },
    "nta-domain": {
      text: "Answers for this domain and its subdomains are served without validation until the anchor expires.",
      topic: "dnssec",
      anchor: "negative-trust-anchors",
    },
    "nta-reason": {
      text: "Why validation is skipped, for example the provider's expired signatures. Shown in the list for whoever reviews it later.",
      range: "Up to 500 characters",
      topic: "dnssec",
      anchor: "negative-trust-anchors",
    },
    "nta-lifetime": {
      text: "When the negative trust anchor expires and the domain is validated again.",
      default: "1 day",
      range: "1 hour to 30 days",
      topic: "dnssec",
      anchor: "negative-trust-anchors",
    },
    "dnssec-col-secure": {
      text: "Answers whose signatures verified up to a trust anchor, since the resolver started.",
      topic: "dnssec",
      anchor: "validation",
    },
    "dnssec-col-insecure": {
      text: "Answers from zones that are provably unsigned, served without checks.",
      topic: "dnssec",
      anchor: "validation",
    },
    "dnssec-col-bogus": {
      text: "Answers whose signatures did not verify; clients got SERVFAIL. A rising count points to a broken zone or an upstream that strips signatures.",
      topic: "dnssec",
      anchor: "validation",
    },
    "dnssec-col-indeterminate": {
      text: "Answers that could not be checked, for example because no trust anchor covers them.",
      topic: "dnssec",
      anchor: "validation",
    },
    "dnssec-col-keys": {
      text: "The trust anchor keys each resolver holds. Valid and configured keys are trusted; add pend waits out the 30-day hold-down; missing and revoked keys are not used. Hover a key for its last refresh error.",
      topic: "dnssec",
      anchor: "trust-anchors",
    },

    // Access control
    "acl-cidr-input": {
      text: "Networks that may use this resolver: cache, forwarding, recursion, rewrites and filtering. Engine groups can add networks.",
      default: "Loopback and private ranges",
      effect:
        "Clients outside the list get REFUSED for every name that is not hosted.",
      topic: "access-control",
      anchor: "recursion-access",
    },
    "authacl-input": {
      text: "Networks that may query the zones Nexora hosts. A zone's own allow-query list replaces this for that zone.",
      default: "0.0.0.0/0 and ::/0 (everyone)",
      effect:
        "Clients outside the list get REFUSED for hosted names. Recursion is controlled separately.",
      topic: "access-control",
      anchor: "authoritative-access",
    },
    "access-col-queries": {
      text: "Who may query the zone: its own allow-query list, or the authoritative query access above when it has none.",
      topic: "access-control",
      anchor: "zone-allow-query",
    },
    "access-col-transfers": {
      text: "Networks, and the TSIG key if set, that may transfer the zone with AXFR or IXFR. Refused when the zone allows no network.",
      topic: "zones",
      anchor: "transfers",
    },
    "access-col-updates": {
      text: "The TSIG keys that may send dynamic updates, and the source networks they must come from when a list is set. Secondary zones and zones without keys refuse updates.",
      topic: "zones",
      anchor: "dynamic-updates",
    },
  },
};
