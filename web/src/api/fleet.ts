import { useEffect, useRef, useState } from "react";
import {
  useMutation,
  useQuery,
  useQueryClient,
  type QueryClient,
} from "@tanstack/react-query";

import { api, unwrap, type Schemas } from "@/api/client";

export type Engine = Schemas["Engine"];
export type EngineGroup = Schemas["EngineGroup"];
export type Rollout = Schemas["Rollout"];
export type RolloutDetail = Schemas["RolloutDetail"];
export type StatsWindow = Schemas["EngineStats"]["window"];

// Lists that move with rollouts and engine health refresh on this interval.
const live = 5_000;

// Engine groups, rollouts and engines feed each other's counts and states, so every fleet
// mutation refreshes both key families.
async function invalidateFleet(qc: QueryClient) {
  await Promise.all([
    qc.invalidateQueries({ queryKey: ["fleet"] }),
    qc.invalidateQueries({ queryKey: ["engines"] }),
  ]);
}

export function useFleetSummary() {
  return useQuery({
    queryKey: ["fleet", "summary"],
    queryFn: async () => unwrap(await api.GET("/fleet/summary")),
    refetchInterval: live,
  });
}

/** The engine groups; live polls them (fleet screens), otherwise they load once (scope selects). */
export function useEngineGroups({ live: poll = false } = {}) {
  return useQuery({
    queryKey: ["fleet", "groups"],
    queryFn: async () => unwrap(await api.GET("/engine-groups")),
    refetchInterval: poll ? live : false,
  });
}

export function useEngineGroup(id: string) {
  return useQuery({
    queryKey: ["fleet", "groups", id],
    queryFn: async () =>
      unwrap(
        await api.GET("/engine-groups/{id}", { params: { path: { id } } }),
      ),
    refetchInterval: live,
  });
}

export function useRollouts({
  engineGroupId,
  limit,
  enabled = true,
}: {
  engineGroupId?: string;
  limit: number;
  enabled?: boolean;
}) {
  return useQuery({
    queryKey: ["fleet", "rollouts", engineGroupId ?? "all", limit],
    queryFn: async () =>
      unwrap(
        await api.GET("/rollouts", {
          params: { query: { engine_group_id: engineGroupId, limit } },
        }),
      ),
    refetchInterval: live,
    enabled,
  });
}

export function useRollout(id: string) {
  return useQuery({
    queryKey: ["fleet", "rollout", id],
    queryFn: async () =>
      unwrap(await api.GET("/rollouts/{id}", { params: { path: { id } } })),
    refetchInterval: live,
  });
}

export function useEngines() {
  return useQuery({
    queryKey: ["engines"],
    queryFn: async () => unwrap(await api.GET("/engines")),
    refetchInterval: live,
  });
}

export function useEngine(id: string) {
  return useQuery({
    queryKey: ["engines", id],
    queryFn: async () =>
      unwrap(await api.GET("/engines/{id}", { params: { path: { id } } })),
    refetchInterval: live,
  });
}

export function useEngineStats(id: string, window: StatsWindow) {
  return useQuery({
    queryKey: ["engines", id, "stats", window],
    queryFn: async () =>
      unwrap(
        await api.GET("/engines/{id}/stats", {
          params: { path: { id }, query: { window } },
        }),
      ),
    refetchInterval: 15_000,
  });
}

/** The engine's metric series for the engine modal's Metrics tab. */
export function useEngineMetrics(id: string, window: StatsWindow) {
  return useQuery({
    queryKey: ["engines", id, "metrics", window],
    queryFn: async () =>
      unwrap(
        await api.GET("/engines/{id}/metrics", {
          params: { path: { id }, query: { window } },
        }),
      ),
    refetchInterval: 15_000,
  });
}

export type EngineLogLine = Schemas["EngineLogs"]["lines"][number];
export type LogLevel = EngineLogLine["level"];

// The Logs tab keeps this many lines; older ones are dropped as new ones arrive.
const logLinesKept = 2_000;
// getEngineLogs answers at most this many lines per request.
const logBatch = 1_000;
const logPollMs = 2_000;

type LogState = {
  key: string;
  lines: EngineLogLine[];
  error: unknown;
  droppedOlder: boolean;
};

/**
 * Tails an engine's log ring buffer: the last 1,000 lines on open, then a poll every 2 s after the
 * sequence cursor. Changing the engine, level or search starts over; pausing keeps the lines and the
 * cursor so resuming continues where it stopped.
 */
export function useEngineLogs(
  id: string,
  opts: { level: LogLevel; q: string; paused: boolean },
): { lines: EngineLogLine[]; error: unknown; droppedOlder: boolean } {
  const { level, q, paused } = opts;
  const key = `${id}\n${level}\n${q}`;
  const [state, setState] = useState<LogState>({
    key,
    lines: [],
    error: null,
    droppedOlder: false,
  });
  // The cursor survives pause/resume; it belongs to one engine, level and search.
  const cursor = useRef<{ key: string; after: number | null }>({
    key,
    after: null,
  });

  useEffect(() => {
    if (paused || id === "") return;
    if (cursor.current.key !== key) cursor.current = { key, after: null };
    let stopped = false;
    let timer: ReturnType<typeof setTimeout> | undefined;

    const read = async (after: number | null) =>
      unwrap(
        await api.GET("/engines/{id}/logs", {
          params: {
            path: { id },
            query: {
              level,
              limit: logBatch,
              ...(q !== "" && { q }),
              ...(after !== null && { after }),
            },
          },
        }),
      );

    async function tick() {
      let after = cursor.current.after;
      let reset = after === null;
      try {
        let r = await read(after);
        // A restarted engine numbers its lines from 1 again: start over.
        if (after !== null && r.last_seq < after) {
          after = null;
          reset = true;
          r = await read(null);
        }
        let gap = after !== null && r.oldest_seq > after + 1;
        // On open the buffer can hold more than one batch: read its newest 1,000 sequence numbers.
        if (after === null && r.lines.length === logBatch) {
          gap = true;
          r = await read(Math.max(0, r.last_seq - logBatch));
        }
        if (stopped) return;
        const lines = r.lines;
        cursor.current = {
          key,
          after:
            lines.length === logBatch
              ? lines[lines.length - 1].seq
              : r.last_seq,
        };
        setState((prev) => {
          const base = prev.key === key && !reset ? prev.lines : [];
          const merged = lines.length > 0 ? base.concat(lines) : base;
          const over = merged.length > logLinesKept;
          return {
            key,
            lines: over ? merged.slice(-logLinesKept) : merged,
            error: null,
            droppedOlder:
              (prev.key === key && !reset && prev.droppedOlder) || over || gap,
          };
        });
      } catch (err) {
        if (stopped) return;
        setState((prev) =>
          prev.key === key
            ? { ...prev, error: err }
            : { key, lines: [], error: err, droppedOlder: false },
        );
      }
      if (!stopped) timer = setTimeout(() => void tick(), logPollMs);
    }

    void tick();
    return () => {
      stopped = true;
      clearTimeout(timer);
    };
  }, [id, level, q, paused, key]);

  return state.key === key
    ? state
    : { lines: [], error: null, droppedOlder: false };
}

export function useCreateEngineGroup() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: Schemas["EngineGroupInput"]) =>
      unwrap(await api.POST("/engine-groups", { body })),
    onSuccess: () => invalidateFleet(qc),
  });
}

export function useUpdateEngineGroup() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({
      id,
      body,
    }: {
      id: string;
      body: Schemas["EngineGroupUpdate"];
    }) =>
      unwrap(
        await api.PUT("/engine-groups/{id}", {
          params: { path: { id } },
          body,
        }),
      ),
    onSuccess: () => invalidateFleet(qc),
  });
}

export function useDeleteEngineGroup() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, revision }: { id: string; revision: number }) =>
      unwrap(
        await api.DELETE("/engine-groups/{id}", {
          params: { path: { id }, query: { revision } },
        }),
      ),
    onSuccess: async (_, { id }) => {
      qc.removeQueries({ queryKey: ["fleet", "groups", id] });
      await invalidateFleet(qc);
    },
  });
}

export function useRollbackEngineGroup() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, toVersion }: { id: string; toVersion: number }) =>
      unwrap(
        await api.POST("/engine-groups/{id}/rollback", {
          params: { path: { id } },
          body: { to_version: toVersion },
        }),
      ),
    onSuccess: () => invalidateFleet(qc),
  });
}

export function useResumeRollouts() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (id: string) =>
      unwrap(
        await api.POST("/engine-groups/{id}/resume-rollouts", {
          params: { path: { id } },
        }),
      ),
    onSuccess: () => invalidateFleet(qc),
  });
}

export function useUpdateEngine() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({
      id,
      body,
    }: {
      id: string;
      body: Schemas["EngineUpdate"];
    }) =>
      unwrap(
        await api.PATCH("/engines/{id}", { params: { path: { id } }, body }),
      ),
    onSuccess: async (saved) => {
      qc.setQueryData(["engines", saved.id], saved);
      await invalidateFleet(qc);
    },
  });
}

export function useRevokeEngine() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (id: string) =>
      unwrap(
        await api.POST("/engines/{id}/revoke", { params: { path: { id } } }),
      ),
    onSuccess: async (saved) => {
      qc.setQueryData(["engines", saved.id], saved);
      await invalidateFleet(qc);
    },
  });
}

export function useRotateEngineCertificate() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (id: string) =>
      unwrap(
        await api.POST("/engines/{id}/rotate-certificate", {
          params: { path: { id } },
        }),
      ),
    onSuccess: async (saved) => {
      qc.setQueryData(["engines", saved.id], saved);
      await invalidateFleet(qc);
    },
  });
}

export function useDeleteEngine() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (id: string) =>
      unwrap(await api.DELETE("/engines/{id}", { params: { path: { id } } })),
    onSuccess: async (_, id) => {
      qc.removeQueries({ queryKey: ["engines", id] });
      await invalidateFleet(qc);
    },
  });
}
