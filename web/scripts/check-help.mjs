// Fails when a form control on a page listed in a help catalogue area has no catalogue entry or no
// HelpTip. With --all, also fails when a page or component with form controls is in no area.
// Controls: id="x" on Input, SelectTrigger, Switch, Textarea, input and textarea; htmlFor="x";
// data-help="x" (for controls whose id is dynamic). Help affordance: <HelpTip id="x"> or
// help="x" on ListEditor, in the same file.
import { existsSync, readFileSync, readdirSync } from "node:fs";
import { fileURLToPath } from "node:url";

const src = fileURLToPath(new URL("../src/", import.meta.url));
const catalogDir = `${src}help/catalog/`;
const read = (rel) => readFileSync(src + rel, "utf8");

// Every JSX opening tag named `name`, as the text between the name and its closing `>` (braces and
// quotes skipped, so `onChange={(e) => ...}` does not end the tag).
function tags(text, names) {
  const out = [];
  const re = new RegExp(`<(${names.join("|")})(?=[\\s/>])`, "g");
  for (const m of text.matchAll(re)) {
    let depth = 0;
    let quote = "";
    let i = m.index + m[0].length;
    for (; i < text.length; i++) {
      const c = text[i];
      if (quote) {
        if (c === quote) quote = "";
      } else if (c === '"' || c === "'" || c === "`") quote = c;
      else if (c === "{") depth++;
      else if (c === "}") depth--;
      else if (c === ">" && depth === 0) break;
    }
    out.push({ name: m[1], attrs: text.slice(m.index + m[0].length, i) });
  }
  return out;
}

const attr = (attrs, name) =>
  [...attrs.matchAll(new RegExp(`(?:^|\\s)${name}="([^"]+)"`, "g"))].map(
    (m) => m[1],
  );

function controlIds(text) {
  const ids = new Set();
  for (const t of tags(text, [
    "Input",
    "SelectTrigger",
    "Switch",
    "Textarea",
    "input",
    "textarea",
  ]))
    for (const id of attr(t.attrs, "id")) ids.add(id);
  for (const m of text.matchAll(/(?:^|\s)(?:htmlFor|data-help)="([^"]+)"/g))
    ids.add(m[1]);
  return ids;
}

function helpTipIds(text) {
  const ids = new Set();
  for (const t of tags(text, ["HelpTip"]))
    for (const id of attr(t.attrs, "id")) ids.add(id);
  for (const t of tags(text, ["ListEditor"]))
    for (const id of attr(t.attrs, "help")) ids.add(id);
  return ids;
}

const problems = [];
const entries = new Set();
const listed = new Set();
for (const file of readdirSync(catalogDir)
  .filter((f) => f.endsWith(".ts") && f !== "index.ts" && f !== "types.ts")
  .sort()) {
  const text = readFileSync(catalogDir + file, "utf8");
  const pages = text.match(/pages:\s*\[([^\]]*)\]/);
  if (!pages) {
    problems.push(`help/catalog/${file}: no pages: [...] array`);
    continue;
  }
  for (const m of pages[1].matchAll(/"([^"]+)"/g)) listed.add(m[1]);
  const body = text.slice(text.indexOf("entries"));
  for (const m of body.matchAll(
    /(?<=^|[{,])\s*(?:"([^"]+)"|([A-Za-z_$][\w$]*))\s*:\s*\{/gm,
  )) {
    const id = m[1] ?? m[2];
    if (id === "entries") continue;
    if (entries.has(id))
      problems.push(`help/catalog/${file}: duplicate help entry "${id}"`);
    entries.add(id);
  }
}

let controls = 0;
for (const page of [...listed].sort()) {
  if (!existsSync(src + page)) {
    problems.push(`${page}: listed in the help catalogue but does not exist`);
    continue;
  }
  const text = read(page);
  const tips = helpTipIds(text);
  for (const id of controlIds(text)) {
    controls++;
    if (!entries.has(id))
      problems.push(`${page}: control "${id}" has no help entry`);
    else if (!tips.has(id))
      problems.push(`${page}: control "${id}" has no HelpTip`);
  }
  for (const id of tips)
    if (!entries.has(id))
      problems.push(`${page}: HelpTip "${id}" has no help entry`);
}

if (process.argv.includes("--all")) {
  const candidates = [
    ...readdirSync(src + "pages")
      .filter((f) => f.endsWith(".tsx"))
      .map((f) => `pages/${f}`),
    ...readdirSync(src + "components", { recursive: true })
      .filter((f) => f.endsWith(".tsx"))
      .map((f) => `components/${f.split("\\").join("/")}`),
  ];
  for (const file of candidates.sort())
    if (!listed.has(file) && controlIds(read(file)).size > 0)
      problems.push(`${file}: has form controls but is in no help area`);
}

if (problems.length > 0) {
  for (const p of problems) console.error(`check-help: ${p}`);
  process.exit(1);
}
console.log(
  `check-help: ${controls} controls on ${listed.size} pages have help`,
);
