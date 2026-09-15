import { cn } from "@/lib/utils";

const isObject = (v: unknown): v is Record<string, unknown> =>
  typeof v === "object" && v !== null && !Array.isArray(v);

const show = (v: unknown) =>
  v === undefined ? "—" : JSON.stringify(v, null, 2);

/**
 * Before and after side by side. Two objects compare key by key (top level); anything else compares
 * as one value. A row whose values differ carries data-changed="true".
 */
export function JsonDiff({
  before,
  after,
}: {
  before: unknown;
  after: unknown;
}) {
  const rows: { key: string; before: unknown; after: unknown }[] =
    isObject(before) || isObject(after)
      ? [
          ...new Set([
            ...Object.keys(isObject(before) ? before : {}),
            ...Object.keys(isObject(after) ? after : {}),
          ]),
        ].map((key) => ({
          key,
          before: isObject(before) ? before[key] : undefined,
          after: isObject(after) ? after[key] : undefined,
        }))
      : [{ key: "", before, after }];

  return (
    <div data-testid="json-diff" className="overflow-x-auto rounded-md border">
      <table className="w-full min-w-[32rem] text-xs">
        <thead>
          <tr className="text-muted-foreground border-b text-left">
            <th className="w-1/5 px-3 py-1.5 font-medium">Field</th>
            <th className="w-2/5 px-3 py-1.5 font-medium">Current</th>
            <th className="w-2/5 px-3 py-1.5 font-medium">Proposed</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => {
            const changed = show(r.before) !== show(r.after);
            return (
              <tr
                key={r.key}
                data-changed={changed ? "true" : "false"}
                className={cn(
                  "border-b align-top last:border-0",
                  changed && "bg-warning/10",
                )}
              >
                <td className="px-3 py-1.5 font-mono font-medium">
                  {r.key || "value"}
                </td>
                <td className="px-3 py-1.5">
                  <pre className="font-mono break-all whitespace-pre-wrap">
                    {show(r.before)}
                  </pre>
                </td>
                <td className="px-3 py-1.5">
                  <pre
                    className={cn(
                      "font-mono break-all whitespace-pre-wrap",
                      changed && "font-semibold",
                    )}
                  >
                    {show(r.after)}
                  </pre>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
