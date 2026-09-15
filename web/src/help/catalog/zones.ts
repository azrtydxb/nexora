import type { HelpArea, HelpEntry } from "./types";

// Shared by the "New zone" dialog and the zone's Transfers tab.
const primaries: HelpEntry = {
  text: "The primary servers a secondary zone is transferred from, as ip:port. Nexora checks their SOA serial on the zone's SOA refresh timer and whenever one of them sends NOTIFY, and transfers with IXFR when it can, AXFR otherwise.",
  range: "At least one primary",
  effect: "NOTIFY is accepted only from these addresses.",
  topic: "zones",
  anchor: "transfers",
};

export const zonesHelp: HelpArea = {
  pages: [
    "pages/ZonesPage.tsx",
    "pages/ZoneDetailPage.tsx",
    "pages/ZoneRecordsTab.tsx",
    "pages/ZoneRecordEditor.tsx",
    "pages/ZoneTransfersTab.tsx",
    "pages/ZoneDnssecTab.tsx",
    "pages/ZoneImportExportTab.tsx",
    "pages/TsigKeysPage.tsx",
  ],
  entries: {
    "zones-col-serial": {
      text: "The SOA serial. It increases with every change to a primary zone; for a secondary zone it is the serial last transferred from its primaries.",
      topic: "zones",
      anchor: "records",
    },
    "zones-col-transfers": {
      text: "Primary zones: the networks allowed to transfer the zone, or refused. Secondary zones: when the zone was last transferred successfully from its primaries.",
      topic: "zones",
      anchor: "transfers",
    },
    "zone-name": {
      text: "The zone's apex name. A trailing dot is added when you leave it out.",
      range: "At most 255 characters, unique across the fleet",
      effect: "Cannot be changed after the zone is created.",
      topic: "zones",
    },
    "zone-kind": {
      text: "Primary zones hold records edited in Nexora, imported from a zone file or changed by dynamic updates. Secondary zones copy their records from other servers by zone transfer.",
      default: "Primary",
      effect: "Cannot be changed after the zone is created.",
      topic: "zones",
    },
    "zone-engine-group": {
      text: "The engine group whose resolvers serve this zone.",
      default: "All engine groups",
      topic: "fleet",
      anchor: "engine-groups",
    },
    "zone-mname": {
      text: "The SOA MNAME: the name of the zone's primary name server.",
      topic: "zones",
      anchor: "records",
    },
    "zone-ttl": {
      text: "The TTL, in seconds, that new records in the editor start with.",
      default: "3600",
      range: "0–2147483647 seconds",
      topic: "zones",
      anchor: "records",
    },
    "zone-rname": {
      text: "The SOA RNAME: the mailbox of the person responsible for the zone, with the @ written as a dot (hostmaster.example.com. for hostmaster@example.com).",
      topic: "zones",
      anchor: "records",
    },
    "zone-ns": {
      text: "The name servers published as the zone's apex NS records.",
      range: "At least one name, separated by commas",
      topic: "zones",
      anchor: "records",
    },
    "zone-primaries": { ...primaries },
    "zone-primary-key": {
      text: "The TSIG key that signs transfers and SOA checks to the primaries.",
      default: "None (unsigned)",
      effect:
        "The primary must know the same key name, algorithm and secret, or it refuses the transfer.",
      topic: "zones",
      anchor: "tsig",
    },
    "record-filter-name": {
      text: "Shows only the records of one owner name: @ for the apex, a name relative to the zone, or an absolute name ending in a dot. Press Enter to apply.",
      topic: "zones",
      anchor: "records",
    },
    "record-filter-type": {
      text: "Shows only the records of one type.",
      default: "All types",
      topic: "zones",
      anchor: "records",
    },
    "record-name": {
      text: "The record's owner name: @ for the apex, a name relative to the zone, or an absolute name ending in a dot inside the zone.",
      range: "At most 255 characters",
      topic: "zones",
      anchor: "records",
    },
    "record-type": {
      text: "The record type. The Data field's hint and check follow the type you pick.",
      default: "A",
      topic: "zones",
      anchor: "records",
    },
    "record-ttl": {
      text: "How long, in seconds, resolvers may cache this record.",
      default: "The zone's default TTL",
      range: "0–2147483647 seconds",
      topic: "zones",
      anchor: "records",
    },
    "record-data": {
      text: "The record's data in zone-file presentation format. The hint under the field shows the format and an example for the selected type, and the value is checked before it is saved.",
      range: "At most 65535 characters",
      effect:
        "Saving increases the zone serial and sends NOTIFY to the notify targets.",
      topic: "zones",
      anchor: "records",
    },
    "zone-allow-query": {
      text: "Networks that may query this zone. Empty uses the global authoritative query access under Access control.",
      default: "Empty (inherit)",
      effect:
        "Clients outside the list get REFUSED for names in this zone; transfers keep their own list.",
      topic: "access-control",
      anchor: "zone-allow-query",
    },
    "zone-transfer-allow": {
      text: "Networks that may transfer this zone with AXFR or IXFR. Empty refuses every transfer; a TSIG key additionally requires signed requests.",
      default: "Empty (transfers refused)",
      topic: "zones",
      anchor: "transfers",
    },
    "zone-transfer-key": {
      text: "The TSIG key every outgoing transfer request must be signed with, in addition to coming from an allowed network.",
      default: "No TSIG key (unsigned transfers from allowed networks)",
      topic: "zones",
      anchor: "tsig",
    },
    "zone-transfer-primaries": { ...primaries },
    "zone-notify": {
      text: "Secondary servers sent a NOTIFY whenever the zone changes, as ip:port, each optionally signed with a TSIG key. They still need to be in the allowed transfer networks to fetch the change.",
      default: "None",
      topic: "zones",
      anchor: "transfers",
    },
    "zone-update-keys": {
      text: "TSIG keys that may change this zone's records with RFC 2136 dynamic updates. Updates must be signed with one of them.",
      default: "None (updates refused)",
      topic: "zones",
      anchor: "dynamic-updates",
    },
    "zone-update-allow": {
      text: "Source networks that may send dynamic updates. Empty allows any source; a TSIG key from the list below is always required.",
      default: "Empty (any source)",
      topic: "zones",
      anchor: "dynamic-updates",
    },
    "zone-dnssec-algorithm": {
      text: "The signing algorithm of the zone's keys: ECDSA P-256 SHA-256 (13) gives small signatures, RSA SHA-256 (8) suits parents or validators that lack algorithm 13.",
      default: "ECDSA P-256 SHA-256 (13)",
      effect: "Fixed while the zone is signed; disable signing to change it.",
      topic: "dnssec",
      anchor: "signing",
    },
    "zone-dnssec-nsec": {
      text: "How the signed zone proves that a name or type does not exist. NSEC lists the zone's names in the clear, so anyone can enumerate them; NSEC3 lists hashes of the names instead, without extra iterations or salt.",
      default: "NSEC3",
      topic: "dnssec",
      anchor: "signing",
    },
    "zone-dnssec-backend": {
      text: "Where the zone's private keys are kept: sealed with the key-encryption key in the database, or inside a PKCS#11 token.",
      default:
        "PKCS#11 when a token is configured, otherwise the key-encryption key",
      effect: "Fixed while the zone is signed.",
      topic: "dnssec",
      anchor: "signing",
    },
    "zone-dnssec-propagation": {
      text: "How long a change takes to reach every server and every cache. Each rollover step waits for it on top of the record TTLs.",
      default: "3600 seconds",
      range: "1–604800 seconds",
      topic: "dnssec",
      anchor: "rollovers",
    },
    "zone-dnssec-parent-ds-ttl": {
      text: "The TTL of the zone's DS record at the parent. Once the parent's DS for a new KSK is confirmed, the old KSK is removed after this TTL plus the propagation delay.",
      default: "86400 seconds",
      range: "1–604800 seconds",
      topic: "dnssec",
      anchor: "rollovers",
    },
    "zone-dnssec-zsk-lifetime": {
      text: "How many days a ZSK signs before it is rolled automatically. 0 rolls ZSKs only when you press Roll ZSK.",
      default: "90 days",
      range: "0–3650 days",
      topic: "dnssec",
      anchor: "rollovers",
    },
    "zone-dnssec-col-state": {
      text: "published: in the DNSKEY set, not signing yet. active: signing. retired: no longer signing, still published until caches have dropped its signatures. removed: gone from the zone.",
      topic: "dnssec",
      anchor: "rollovers",
    },
    "zone-dnssec-col-ds": {
      text: "KSKs only. pending: the parent's DS for this key is not confirmed, and CDS and CDNSKEY advertise it. seen: you confirmed the parent publishes it.",
      topic: "dnssec",
      anchor: "rollovers",
    },
    "zone-import-content": {
      text: "A zone file in BIND master-file format. Importing replaces every record and the SOA fields of the zone in one change; $INCLUDE and $GENERATE are refused.",
      range: "At most 1,000,000 records and 64 MiB",
      effect:
        "Records not in the file are deleted. The file's SOA serial is adopted when it is newer than the zone's.",
      topic: "zones",
      anchor: "zone-files",
    },
    "zone-import-picker": {
      text: "Loads a zone file from your computer into the text box, where you can review it before importing.",
      topic: "zones",
      anchor: "zone-files",
    },
    "tsig-name": {
      text: "The key name, exactly as the peers configure it. A trailing dot is added when you leave it out.",
      range: "A domain name, unique across TSIG keys",
      topic: "zones",
      anchor: "tsig",
    },
    "tsig-algorithm": {
      text: "The HMAC algorithm the key signs with. Peers must use the same one.",
      default: "hmac-sha256",
      topic: "zones",
      anchor: "tsig",
    },
    "tsig-secret": {
      text: "The shared secret in base64. Leave it empty to generate a random secret of the algorithm's output size. It is stored encrypted and shown only once.",
      default: "Empty (generated)",
      range: "16–64 bytes",
      topic: "zones",
      anchor: "tsig",
    },
  },
};
