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
