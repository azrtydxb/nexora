import type { Schemas } from "@/api/client";

export const parseInterfaces = (value: string): string[] =>
  value.trim() === "" ? [] : value.split(",").map((s) => s.trim());

export function validateMdns(value: Schemas["MdnsSettings"]): string | null {
  if (value.enabled && !value.interfaces.length)
    return "Name at least one interface";
  if (value.reflect && value.reflect_interfaces.length < 2)
    return "Name at least two reflection interfaces";
  for (const names of [value.interfaces, value.reflect_interfaces]) {
    if (names.length > 16) return "Use at most 16 interfaces per list";
    if (names.some((s) => !/^[A-Za-z0-9_.:@-]{1,15}$/.test(s)))
      return "Interface names must be 1–15 letters, digits or _.:@-";
    if (new Set(names).size !== names.length)
      return "Interface names must be distinct in each list";
  }
  if (
    !Number.isInteger(value.timeout_ms) ||
    value.timeout_ms < 100 ||
    value.timeout_ms > 5000
  )
    return "Timeout must be a whole number from 100 to 5000 ms";
  return null;
}

export function validateOdoh(
  value: Schemas["OdohSettingsUpdate"],
): string | null {
  if (value.proxy_enabled && !value.proxy_targets.length)
    return "Add at least one proxy target";
  if (value.proxy_targets.length > 64) return "Use at most 64 proxy targets";
  if (
    value.proxy_targets.some(
      (t) => !t.host.trim() || t.host.length > 261 || /[\s/?#@]/.test(t.host),
    )
  )
    return "Enter a target host, optionally with a port, without a URL scheme or path";
  if (
    new Set(value.proxy_targets.map((t) => t.host.toLowerCase())).size !==
    value.proxy_targets.length
  )
    return "Proxy target hosts must be distinct";
  if (value.proxy_targets.some((t) => t.ca_pem.length > 65536))
    return "CA PEM must be at most 65536 characters";
  if (
    !Number.isInteger(value.proxy_timeout_ms) ||
    value.proxy_timeout_ms < 100 ||
    value.proxy_timeout_ms > 10000
  )
    return "Proxy timeout must be a whole number from 100 to 10000 ms";
  if (
    !Number.isInteger(value.key_rotation_hours) ||
    value.key_rotation_hours < 1 ||
    value.key_rotation_hours > 720
  )
    return "Key rotation must be a whole number from 1 to 720 hours";
  return null;
}
