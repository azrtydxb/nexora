// Presentation-format help for the record types the management plane accepts (RecordInput.type),
// and a light client-side check that catches the common mistakes before the server's full parse.
// The server stays the authority: a value passing here can still be refused with a 422.

export const recordTypes = [
  "A",
  "AAAA",
  "CAA",
  "CNAME",
  "DNAME",
  "DS",
  "HTTPS",
  "LOC",
  "MX",
  "NAPTR",
  "NS",
  "PTR",
  "SRV",
  "SSHFP",
  "SVCB",
  "TLSA",
  "TXT",
] as const;

export type RecordType = (typeof recordTypes)[number];

type Hint = { placeholder: string; help: string };

export const rdataHints: Record<RecordType, Hint> = {
  A: { placeholder: "192.0.2.10", help: "An IPv4 address." },
  AAAA: { placeholder: "2001:db8::10", help: "An IPv6 address." },
  CAA: {
    placeholder: '0 issue "letsencrypt.org"',
    help: "Flags, tag (issue, issuewild, iodef) and a quoted value.",
  },
  CNAME: {
    placeholder: "www.example.com.",
    help: "The canonical name, absolute (ending in a dot).",
  },
  DNAME: {
    placeholder: "example.net.",
    help: "The target subtree, absolute (ending in a dot).",
  },
  DS: {
    placeholder: "12345 13 2 1F2E…",
    help: "Key tag, algorithm, digest type and hex digest of the child's key.",
  },
  HTTPS: {
    placeholder: '1 . alpn="h2,h3"',
    help: "Priority, target (. for the owner) and SvcParams.",
  },
  LOC: {
    placeholder: "52 22 23.000 N 4 53 32.000 E -2.00m 0.00m 10000m 10m",
    help: "Latitude, longitude, altitude and optional size and precision (RFC 1876).",
  },
  MX: {
    placeholder: "10 mail.example.com.",
    help: "Preference and mail server, absolute (ending in a dot).",
  },
  NAPTR: {
    placeholder: '100 10 "S" "SIP+D2U" "" _sip._udp.example.com.',
    help: "Order, preference, flags, service, regexp and replacement.",
  },
  NS: {
    placeholder: "ns1.example.com.",
    help: "The name server, absolute (ending in a dot).",
  },
  PTR: {
    placeholder: "host.example.com.",
    help: "The name the address points to, absolute (ending in a dot).",
  },
  SRV: {
    placeholder: "10 5 5060 sip.example.com.",
    help: "Priority, weight, port and target, absolute (ending in a dot).",
  },
  SSHFP: {
    placeholder: "4 2 123456789ABCDEF…",
    help: "Algorithm, fingerprint type and hex fingerprint.",
  },
  SVCB: {
    placeholder: "1 svc.example.com. port=8443",
    help: "Priority, target and SvcParams.",
  },
  TLSA: {
    placeholder: "3 1 1 0123456789ABCDEF…",
    help: "Usage, selector, matching type and hex data.",
  },
  TXT: {
    placeholder: '"v=spf1 mx -all"',
    help: "One or more quoted strings of up to 255 characters each.",
  },
};

const ipv4 =
  /^(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}$/;
const hostname = /^(\.|([a-z0-9_*]([a-z0-9_-]{0,61}[a-z0-9_])?\.)+)$/i;
const uint = (max: number) => (s: string) =>
  /^\d+$/.test(s) && Number(s) <= max;
const hex = /^[0-9a-f]+$/i;

function isIPv6(s: string): boolean {
  if (!/^[0-9a-f:.]+$/i.test(s) || !s.includes(":")) return false;
  try {
    return new URL(`http://[${s}]/`).hostname !== "";
  } catch {
    return false;
  }
}

const absolute = (what: string, s: string): string | null =>
  !hostname.test(s)
    ? `${what} is not a valid domain name.`
    : s.endsWith(".")
      ? null
      : `${what} must be absolute: end it with a dot (${s}.).`;

/** Returns a message describing why data cannot be RDATA of type, or null when it looks valid. */
export function checkRdata(type: RecordType, raw: string): string | null {
  const data = raw.trim();
  if (data === "") return "Enter the record data.";
  if (/[;\n\r]/.test(data)) return "Use one line without ; comments.";
  const f = data.split(/\s+/);
  switch (type) {
    case "A":
      return ipv4.test(data) ? null : "Enter an IPv4 address like 192.0.2.10.";
    case "AAAA":
      return isIPv6(data) ? null : "Enter an IPv6 address like 2001:db8::10.";
    case "CNAME":
    case "DNAME":
    case "NS":
    case "PTR":
      return f.length !== 1
        ? "Enter a single domain name."
        : absolute("The target", data);
    case "MX":
      if (f.length !== 2 || !uint(65535)(f[0]))
        return "Enter a preference (0–65535) and a mail server.";
      return absolute("The mail server", f[1]);
    case "SRV":
      if (f.length !== 4 || !f.slice(0, 3).every(uint(65535)))
        return "Enter priority, weight and port (0–65535) and a target.";
      return absolute("The target", f[3]);
    case "CAA":
      if (f.length < 3 || !uint(255)(f[0]))
        return 'Enter flags (0–255), a tag and a quoted value, like 0 issue "ca.example".';
      return /^[a-z0-9]+$/i.test(f[1]) ? null : "The tag must be alphanumeric.";
    case "DS":
    case "TLSA":
      if (
        f.length < 4 ||
        !uint(type === "DS" ? 65535 : 255)(f[0]) ||
        !uint(255)(f[1]) ||
        !uint(255)(f[2]) ||
        !hex.test(f.slice(3).join(""))
      )
        return type === "DS"
          ? "Enter key tag, algorithm, digest type and a hex digest."
          : "Enter usage, selector, matching type and hex data.";
      return null;
    case "SSHFP":
      if (
        f.length < 3 ||
        !uint(255)(f[0]) ||
        !uint(255)(f[1]) ||
        !hex.test(f.slice(2).join(""))
      )
        return "Enter algorithm, fingerprint type and a hex fingerprint.";
      return null;
    case "HTTPS":
    case "SVCB":
      if (f.length < 2 || !uint(65535)(f[0]))
        return "Enter a priority (0–65535), a target and optional SvcParams.";
      return absolute("The target", f[1]);
    case "NAPTR":
      return f.length >= 6 && uint(65535)(f[0]) && uint(65535)(f[1])
        ? null
        : "Enter order, preference, flags, service, regexp and replacement.";
    case "TXT":
      return /^"/.test(data) && !/^("([^"\\]|\\.)*"\s*)+$/.test(data)
        ? "Close every quoted string."
        : null;
    case "LOC":
      return f.length >= 8 ? null : "Enter latitude, longitude and altitude.";
  }
}

/** The owner name relative to zone: "@" for the apex, the absolute name when outside it. */
export function relativeName(name: string, zone: string): string {
  const n = name.toLowerCase();
  const z = zone.toLowerCase();
  if (n === z) return "@";
  if (n.endsWith(`.${z}`)) return name.slice(0, name.length - z.length - 1);
  return name;
}

/** The absolute owner name for an input relative to zone ("@" or empty is the apex). */
export function absoluteName(input: string, zone: string): string {
  const v = input.trim();
  if (v === "" || v === "@") return zone;
  if (v.endsWith(".")) return v;
  return `${v}.${zone}`;
}

/** Splits a comma- or whitespace-separated list, dropping empty entries. */
export function splitList(v: string): string[] {
  return v.split(/[\s,]+/).filter(Boolean);
}

/** An absolute domain name for input (a trailing dot is added when missing). */
export function fqdn(v: string): string {
  const s = v.trim();
  return s === "" || s.endsWith(".") ? s : `${s}.`;
}
