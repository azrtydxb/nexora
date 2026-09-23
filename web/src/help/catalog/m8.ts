import type { HelpArea } from "./types";

export const m8Help: HelpArea = {
  pages: [
    "pages/ZoneZonemdCard.tsx",
    "components/ZonemdControls.tsx",
    "pages/CatalogZonesPage.tsx",
    "pages/OdohSection.tsx",
    "pages/EngineGroupMdnsSection.tsx",
  ],
  entries: {
    "zonemd-generate": {
      text: "Publish a SHA-384 SIMPLE ZONEMD digest on each primary zone rebuild, including signed zones.",
      default: "Off",
      topic: "zones",
      anchor: "zonemd",
    },
    "zonemd-verify": {
      text: "Off skips digest checking. If present rejects a bad digest but accepts its absence. Required rejects both bad and missing digests. Failure keeps the last good zone; digest checking does not authenticate its source.",
      default: "If present",
      topic: "zones",
      anchor: "zonemd",
    },
    "zone-catalog-select": {
      text: "Assign this zone to a producer catalog. No catalog removes its membership. Catalog-managed consumer members cannot be edited manually.",
      default: "No catalog",
      topic: "zones",
      anchor: "catalog-zones",
    },
    "catalog-name": {
      text: "The fully qualified DNS name of the catalog zone, distinct from existing zones.",
      range: "At most 255 characters",
      topic: "zones",
      anchor: "catalog-zones",
    },
    "catalog-role": {
      text: "Producer publishes assigned zones. Consumer transfers a remote catalog and creates secondary member zones. An empty remote catalog deletes all members it created.",
      default: "Producer",
      topic: "zones",
      anchor: "catalog-zones",
    },
    "catalog-group": {
      text: "Scope the catalog to one engine group, or choose all groups for a global catalog. Consumer members inherit the catalog scope.",
      topic: "zones",
      anchor: "catalog-zones",
    },
    "catalog-allow": {
      text: "Comma-separated CIDRs allowed to transfer the producer catalog. At least one is required; permit only trusted secondary servers.",
      topic: "zones",
      anchor: "catalog-zones",
    },
    "catalog-primary": {
      text: "Remote primary IP address and port for a consumer, for example 192.0.2.53:53 or [2001:db8::53]:53.",
      topic: "zones",
      anchor: "catalog-zones",
    },
    "catalog-tsig": {
      text: "Optional existing TSIG key for authenticated transfers. Producers require it from consumers; consumers use it when transferring from their primary.",
      default: "None",
      topic: "zones",
      anchor: "catalog-zones",
    },
    "odoh-target-enabled": {
      text: "Accept encrypted Oblivious DoH queries through the DoH listener. Recursion ACLs must allow the proxy addresses reaching this target.",
      default: "Off",
      topic: "resolution",
      anchor: "oblivious-doh",
    },
    "odoh-proxy-enabled": {
      text: "Forward opaque encrypted queries only to configured target hosts. At least one target is required when enabled.",
      default: "Off",
      topic: "resolution",
      anchor: "oblivious-doh",
    },
    "odoh-target-host": {
      text: "Allowed destination hostname or IP address, optionally with a port. Enter a host, not an https URL. Targets see the engine address rather than the original client.",
      range: "At most 64 targets",
      topic: "resolution",
      anchor: "oblivious-doh",
    },
    "odoh-target-ca": {
      text: "Optional PEM CA certificates for this target. Leave blank to use system trust. This is a public certificate, never a private key.",
      range: "At most 65536 characters",
      topic: "resolution",
      anchor: "oblivious-doh",
    },
    "odoh-timeout": {
      text: "Maximum time for an outbound proxy request.",
      default: "2000 ms",
      range: "100–10000 ms",
      topic: "resolution",
      anchor: "oblivious-doh",
    },
    "odoh-rotation": {
      text: "How often management creates new target keys. New keys are published after a five-minute delay; older keys retain their validity window.",
      default: "24 hours",
      range: "1–720 hours",
      topic: "resolution",
      anchor: "oblivious-doh",
    },
    "odoh-rotate": {
      text: "Create a new target key immediately. Admin only; requires configured key storage. Publication starts five minutes later.",
      topic: "resolution",
      anchor: "oblivious-doh",
    },
    "odoh-keys": {
      text: "Metadata only: creation, publication start and expiration times. A created key is not advertised before its Listed from time. Key material is never displayed.",
      topic: "resolution",
      anchor: "oblivious-doh",
    },
    "mdns-enabled": {
      text: "Resolve eligible .local names using multicast on the selected LAN interfaces. Requires hostNetwork or an interface on the LAN segment. Turning off the gateway does not turn off reflection.",
      default: "Off",
      topic: "fleet",
      anchor: "mdns-gateway-and-reflection",
    },
    "mdns-interfaces": {
      text: "Comma-separated gateway interface names present on each engine host. At least one is required while the gateway is enabled.",
      range: "At most 16 distinct names, 1–15 characters each",
      topic: "fleet",
      anchor: "mdns-gateway-and-reflection",
    },
    "mdns-timeout": {
      text: "How long to collect multicast replies for one gateway lookup.",
      default: "500 ms",
      range: "100–5000 ms",
      topic: "fleet",
      anchor: "mdns-gateway-and-reflection",
    },
    "mdns-reflect": {
      text: "Reflect multicast across selected LAN interfaces. This is independent of gateway resolution and can expose service discovery across VLANs.",
      default: "Off",
      topic: "fleet",
      anchor: "mdns-gateway-and-reflection",
    },
    "mdns-reflect-interfaces": {
      text: "Comma-separated interface names to reflect between. Reflection requires at least two distinct interfaces.",
      range: "At most 16 distinct names, 1–15 characters each",
      topic: "fleet",
      anchor: "mdns-gateway-and-reflection",
    },
    "rpz-zonemd-verify": {
      text: "Transfer sources only. If present checks any digest supplied; Required also rejects feeds without a digest; Off skips checking. Failures retain the last good policy zone, subject to its normal expiry.",
      default: "If present",
      topic: "filtering",
      anchor: "rpz-zonemd",
    },
    "rpz-zonemd-status": {
      text: "Each engine reports Off, Absent, Verified or Failed with its error. No report is not proof of successful verification. A digest alone does not authenticate the publisher.",
      topic: "filtering",
      anchor: "rpz-zonemd",
    },
  },
};
