import { Link } from "react-router";
import { TriangleAlert } from "lucide-react";

import { useAiFindings } from "@/api/ai";
import { Alert, AlertDescription } from "@/components/ui/alert";

/** "N open anomalies" above the query log, linking to the anomalies on the insights page. */
export function AnomalyBanner() {
  const findings = useAiFindings("anomaly", "open");
  const n = findings.data?.length ?? 0;
  if (n === 0) return null;
  return (
    <Alert className="mb-4" data-testid="querylog-ai-anomalies">
      <TriangleAlert className="h-4 w-4" />
      <AlertDescription>
        <Link to="/ai/insights?kind=anomaly" className="underline">
          {n} open {n === 1 ? "anomaly" : "anomalies"}
        </Link>{" "}
        in the query log.
      </AlertDescription>
    </Alert>
  );
}
