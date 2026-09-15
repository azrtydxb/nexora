import type { Schemas } from "@/api/client";
import { Badge } from "@/components/ui/badge";
import { formatTimestamp, usePreferences } from "@/lib/preferences";

/** The cached AI threat verdict for a query-log name: nothing unless the name is a threat. */
export function ThreatBadge({
  threat,
}: {
  threat: Schemas["QueryLogRecord"]["threat"];
}) {
  const prefs = usePreferences();
  if (!threat?.is_threat) return null;
  const categories =
    threat.categories.length > 0
      ? threat.categories.join(", ")
      : "unclassified";
  return (
    <Badge
      variant="destructive"
      data-testid="querylog-threat"
      title={`Checked ${formatTimestamp(new Date(threat.checked_at), prefs)}`}
    >
      Threat: {categories} {Math.round(threat.confidence * 100)}%
    </Badge>
  );
}
