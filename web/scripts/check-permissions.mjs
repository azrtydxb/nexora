// Fails when web/src/auth/permissions.ts and mgmt/internal/auth/permissions.go disagree.
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

const read = (rel) =>
  readFileSync(fileURLToPath(new URL(rel, import.meta.url)), "utf8");
const goSrc = read("../../mgmt/internal/auth/permissions.go");
const tsSrc = read("../src/auth/permissions.ts");

function section(src, start) {
  const i = src.indexOf(start);
  if (i < 0) throw new Error(`permissions.go: "${start}" not found`);
  return src.slice(i, src.indexOf("\n}", i));
}

const go = new Map();
for (const [, op] of section(goSrc, "var Public = map[string]bool{").matchAll(
  /"(\w+)":\s*true/g,
)) {
  go.set(op, "public");
}
for (const [, op, role] of section(
  goSrc,
  "var Permissions = map[string]Role{",
).matchAll(/"(\w+)":\s*Role(Viewer|Operator|Admin)/g)) {
  go.set(op, role.toLowerCase());
}

const ts = new Map();
const body = tsSrc.slice(tsSrc.indexOf("export const permissions"));
for (const [, op, role] of body
  .slice(0, body.indexOf("\n}"))
  .matchAll(/(\w+):\s*"(viewer|operator|admin|public)"/g)) {
  ts.set(op, role);
}

const problems = [];
for (const [op, role] of go) {
  if (!ts.has(op))
    problems.push(`${op}: missing in permissions.ts (Go: ${role})`);
  else if (ts.get(op) !== role)
    problems.push(`${op}: permissions.ts ${ts.get(op)}, Go ${role}`);
}
for (const [op, role] of ts) {
  if (!go.has(op))
    problems.push(
      `${op}: in permissions.ts (${role}) but not in permissions.go`,
    );
}
if (go.size === 0) problems.push("no operations parsed from permissions.go");
if (problems.length > 0) {
  console.error("permission parity check failed:\n  " + problems.join("\n  "));
  process.exit(1);
}
console.log(`permission parity: ${go.size} operations match`);
