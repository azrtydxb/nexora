import { useAiListClassification } from "@/api/ai";
import { ErrorAlert, formatAgo, formatDateTime } from "@/components/common";

const integer = new Intl.NumberFormat();

/**
 * What the classification agent estimates a block list blocks, from a sample of its names: one bar
 * per category. The caller mounts this only when AI is enabled, so the request is never made with
 * AI off.
 */
export function ListClassification({ listId }: { listId: string }) {
  const q = useAiListClassification(listId);
  const c = q.data;
  // debt: getAiFilterListClassification answers "breakdown": null (not []) for a list that was never
  // classified, against its schema; drop the fallback once the handler always sends an array.
  const breakdown = c?.breakdown ?? [];
  const sampled = breakdown.reduce((n, b) => n + b.sampled, 0);
  const buckets = [...breakdown].sort((a, b) => b.sampled - a.sampled);

  return (
    <section
      data-testid={`ai-list-classification-${listId}`}
      aria-labelledby={`ai-list-classification-${listId}-heading`}
      className="border-t px-5 py-4"
    >
      <h4
        id={`ai-list-classification-${listId}-heading`}
        className="mb-2 text-sm font-semibold"
      >
        AI classification
      </h4>
      <ErrorAlert error={q.error} prefix="Could not load the classification" />
      {q.isPending && <p className="text-muted-foreground text-sm">Loading…</p>}
      {c && (c.classified_at === null || sampled === 0) && (
        <p className="text-muted-foreground text-sm">Not classified yet.</p>
      )}
      {c && c.classified_at !== null && sampled > 0 && (
        <>
          <ul className="grid max-w-xl gap-2">
            {buckets.map((b) => {
              const pct = Math.round((b.sampled / sampled) * 100);
              return (
                <li key={b.category} className="grid gap-1 text-sm">
                  <div className="flex items-baseline justify-between gap-3">
                    <span>{b.category}</span>
                    <span className="text-muted-foreground text-xs tabular-nums">
                      {pct}% · ~{integer.format(b.estimated)}{" "}
                      {b.estimated === 1 ? "name" : "names"}
                    </span>
                  </div>
                  <div
                    role="meter"
                    aria-label={b.category}
                    aria-valuemin={0}
                    aria-valuemax={100}
                    aria-valuenow={pct}
                    className="bg-muted h-1.5 overflow-hidden rounded-full"
                  >
                    <div
                      className={
                        b.category === "none"
                          ? "bg-muted-foreground/50 h-full"
                          : "bg-primary h-full"
                      }
                      style={{ width: `${pct}%` }}
                    />
                  </div>
                </li>
              );
            })}
          </ul>
          <p className="text-muted-foreground mt-3 text-xs">
            Estimated from {integer.format(c.sample_size)} sampled names of{" "}
            {integer.format(c.entry_count)} ·{" "}
            <span title={formatDateTime(c.classified_at)}>
              classified {formatAgo(c.classified_at)}
            </span>
          </p>
        </>
      )}
    </section>
  );
}
