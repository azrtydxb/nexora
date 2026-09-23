import { test, expect } from "@playwright/test";
import {
  parseInterfaces,
  validateMdns,
  validateOdoh,
} from "../../src/lib/m8Validation";

const mdns = {
  enabled: false,
  interfaces: [] as string[],
  timeout_ms: 500,
  reflect: false,
  reflect_interfaces: [] as string[],
};
const odoh = {
  target_enabled: false,
  proxy_enabled: false,
  proxy_targets: [] as { host: string; ca_pem: string }[],
  proxy_timeout_ms: 2000,
  key_rotation_hours: 24,
  revision: 1,
};

test("mDNS gateway and reflection validate independently", () => {
  expect(validateMdns(mdns)).toBeNull();
  expect(validateMdns({ ...mdns, enabled: true })).toBe(
    "Name at least one interface",
  );
  expect(
    validateMdns({ ...mdns, reflect: true, reflect_interfaces: ["vlan10"] }),
  ).toBe("Name at least two reflection interfaces");
  expect(
    validateMdns({
      ...mdns,
      reflect: true,
      reflect_interfaces: ["vlan10", "vlan20"],
    }),
  ).toBeNull();
  expect(
    validateMdns({ ...mdns, enabled: true, interfaces: ["vlan10"] }),
  ).toBeNull();
});

test("mDNS rejects malformed, repeated and oversized interface lists even while disabled", () => {
  expect(parseInterfaces(" vlan10, vlan20 ")).toEqual(["vlan10", "vlan20"]);
  expect(parseInterfaces(" ")).toEqual([]);
  for (const input of [
    "vlan10,",
    "vlan10,,vlan20",
    "vlan10, vlan10",
    "bad/interface",
    "sixteencharacters",
    Array.from({ length: 17 }, (_, n) => `vlan${n}`).join(","),
  ]) {
    expect(
      validateMdns({ ...mdns, interfaces: parseInterfaces(input) }),
      input,
    ).not.toBeNull();
  }
  for (const timeout_ms of [0, 99, 5001, 100.5, NaN])
    expect(validateMdns({ ...mdns, timeout_ms })).not.toBeNull();
  for (const timeout_ms of [100, 5000])
    expect(validateMdns({ ...mdns, timeout_ms })).toBeNull();
});

test("ODoH stays default off and requires an explicit valid proxy target", () => {
  expect(validateOdoh(odoh)).toBeNull();
  expect(validateOdoh({ ...odoh, target_enabled: true })).toBeNull();
  expect(validateOdoh({ ...odoh, proxy_enabled: true })).toBe(
    "Add at least one proxy target",
  );
  for (const host of [
    "",
    "https://odoh.example",
    "odoh.example/path",
    "user@odoh.example",
    "bad host",
  ])
    expect(
      validateOdoh({ ...odoh, proxy_targets: [{ host, ca_pem: "" }] }),
      host,
    ).not.toBeNull();
  for (const host of ["odoh.example", "odoh.example:8443", "[2001:db8::1]:443"])
    expect(
      validateOdoh({
        ...odoh,
        proxy_enabled: true,
        proxy_targets: [{ host, ca_pem: "" }],
      }),
      host,
    ).toBeNull();
  expect(
    validateOdoh({
      ...odoh,
      proxy_targets: [
        { host: "odoh.example", ca_pem: "" },
        { host: "ODOH.EXAMPLE", ca_pem: "" },
      ],
    }),
  ).not.toBeNull();
});

test("ODoH enforces numeric and target-size contract boundaries", () => {
  for (const proxy_timeout_ms of [99, 10001, 100.5, NaN])
    expect(validateOdoh({ ...odoh, proxy_timeout_ms })).not.toBeNull();
  for (const key_rotation_hours of [0, 721, 1.5, NaN])
    expect(validateOdoh({ ...odoh, key_rotation_hours })).not.toBeNull();
  for (const proxy_timeout_ms of [100, 10000])
    expect(validateOdoh({ ...odoh, proxy_timeout_ms })).toBeNull();
  for (const key_rotation_hours of [1, 720])
    expect(validateOdoh({ ...odoh, key_rotation_hours })).toBeNull();
  expect(
    validateOdoh({
      ...odoh,
      proxy_targets: [{ host: "odoh.example", ca_pem: "x".repeat(65537) }],
    }),
  ).not.toBeNull();
  expect(
    validateOdoh({
      ...odoh,
      proxy_targets: Array.from({ length: 65 }, (_, n) => ({
        host: `odoh${n}.example`,
        ca_pem: "",
      })),
    }),
  ).not.toBeNull();
});
