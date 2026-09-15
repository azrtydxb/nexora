import { useQuery, type UseQueryResult } from "@tanstack/react-query";

import { api, unwrap, type Schemas } from "@/api/client";

export type DashboardRange = "15m" | "1h" | "6h" | "24h" | "7d";

export const dashboardRanges: DashboardRange[] = [
  "15m",
  "1h",
  "6h",
  "24h",
  "7d",
];

/** Ranges up to an hour move every step, so they poll faster than the longer ones. */
function interval(range: DashboardRange): number {
  return range === "15m" || range === "1h" ? 10_000 : 60_000;
}

export function useDashboardSeries(
  range: DashboardRange,
  { live = true } = {},
): UseQueryResult<Schemas["DashboardSeries"]> {
  return useQuery({
    queryKey: ["dashboard", "series", range],
    queryFn: async () =>
      unwrap(
        await api.GET("/dashboard/series", {
          params: { query: { range } },
        }),
      ),
    refetchInterval: live ? interval(range) : false,
  });
}

export function useDashboardTop(
  range: DashboardRange,
  { live = true } = {},
): UseQueryResult<Schemas["DashboardTop"]> {
  return useQuery({
    queryKey: ["dashboard", "top", range],
    queryFn: async () =>
      unwrap(
        await api.GET("/dashboard/top", {
          params: { query: { range } },
        }),
      ),
    refetchInterval: live ? interval(range) : false,
  });
}

export function useDashboardHealth({ live = true } = {}): UseQueryResult<
  Schemas["DashboardHealth"]
> {
  return useQuery({
    queryKey: ["dashboard", "health"],
    queryFn: async () => unwrap(await api.GET("/dashboard/health")),
    refetchInterval: live ? 15_000 : false,
  });
}
