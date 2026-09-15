import {
  useMutation,
  useQueries,
  useQuery,
  useQueryClient,
  type UseMutationResult,
  type UseQueryResult,
} from "@tanstack/react-query";

import { api, unwrap, type ApiError, type Schemas } from "@/api/client";

export type AiAgentName = Schemas["AiAgentName"];
export type AiStatus = Schemas["AiStatus"];
export type AiTask = Schemas["AiTask"];
export type AiFinding = Schemas["AiFinding"];
export type AiProposal = Schemas["AiProposal"];

const findingsRefresh = 30_000;
const taskPoll = 2_000;
const riskPoll = 5_000;

const taskActive = (t: AiTask | null | undefined) =>
  t?.status === "queued" || t?.status === "running";

export function useAiStatus(): UseQueryResult<AiStatus> {
  return useQuery({
    queryKey: ["ai", "status"],
    queryFn: async () => unwrap(await api.GET("/ai/status")),
    staleTime: 30_000,
    retry: false,
  });
}

/** True only once the AI status has loaded and reports enabled. */
export function useAiEnabled(): boolean {
  return useAiStatus().data?.enabled === true;
}

export function useAiTask(id: string | null): UseQueryResult<AiTask> {
  return useQuery({
    queryKey: ["ai", "task", id],
    queryFn: async () =>
      unwrap(
        await api.GET("/ai/tasks/{id}", { params: { path: { id: id ?? "" } } }),
      ),
    enabled: id !== null,
    refetchInterval: (q) => (taskActive(q.state.data) ? taskPoll : false),
  });
}

export function useRunAiAgent(): UseMutationResult<
  void,
  ApiError,
  AiAgentName
> {
  const qc = useQueryClient();
  return useMutation<void, ApiError, AiAgentName>({
    mutationFn: async (agent) => {
      unwrap(
        await api.POST("/ai/agents/{agent}/run", {
          params: { path: { agent } },
        }),
      );
    },
    onSettled: () => qc.invalidateQueries({ queryKey: ["ai", "status"] }),
  });
}

export function useAiFindings(
  kind: "anomaly" | "insight",
  status?: string,
): UseQueryResult<AiFinding[]> {
  return useQuery({
    queryKey: ["ai", "findings", kind, status ?? "all"],
    queryFn: async () =>
      unwrap(
        await api.GET("/ai/findings", {
          params: {
            query: { kind, status: status as AiFinding["status"] | undefined },
          },
        }),
      ),
    refetchInterval: findingsRefresh,
  });
}

export function useUpdateAiFinding(): UseMutationResult<
  AiFinding,
  ApiError,
  { id: string; status: "acknowledged" | "dismissed" }
> {
  const qc = useQueryClient();
  return useMutation<
    AiFinding,
    ApiError,
    { id: string; status: "acknowledged" | "dismissed" }
  >({
    mutationFn: async ({ id, status }) =>
      unwrap(
        await api.PATCH("/ai/findings/{id}", {
          params: { path: { id } },
          body: { status },
        }),
      ),
    onSuccess: async () => {
      await Promise.all([
        qc.invalidateQueries({ queryKey: ["ai", "findings"] }),
        qc.invalidateQueries({ queryKey: ["ai", "insights"] }),
      ]);
    },
  });
}

export function useAiInsights(): UseQueryResult<Schemas["AiInsights"]> {
  return useQuery({
    queryKey: ["ai", "insights"],
    queryFn: async () => unwrap(await api.GET("/ai/insights")),
    refetchInterval: findingsRefresh,
  });
}

export function useAiProposals(
  source?: AiProposal["source"],
  status?: AiProposal["status"],
): UseQueryResult<AiProposal[]> {
  return useQuery({
    queryKey: ["ai", "proposals", "list", source ?? "all", status ?? "all"],
    queryFn: async () =>
      unwrap(
        await api.GET("/ai/proposals", {
          params: { query: { source, status } },
        }),
      ),
  });
}

const proposalQuery = (id: string) => ({
  queryKey: ["ai", "proposals", "one", id],
  queryFn: async () =>
    unwrap(await api.GET("/ai/proposals/{id}", { params: { path: { id } } })),
});

export function useAiProposal(id: string | null): UseQueryResult<AiProposal> {
  return useQuery({ ...proposalQuery(id ?? ""), enabled: id !== null });
}

/** Every proposal in ids with its live `current` values (the apply dialog). */
export function useAiProposalsById(
  ids: string[],
): UseQueryResult<AiProposal>[] {
  return useQueries({ queries: ids.map(proposalQuery) });
}

export function useApplyAiProposals(): UseMutationResult<
  Schemas["AiApplyResponse"],
  ApiError,
  Schemas["AiApplyRequest"]
> {
  const qc = useQueryClient();
  return useMutation<
    Schemas["AiApplyResponse"],
    ApiError,
    Schemas["AiApplyRequest"]
  >({
    mutationFn: async (body) =>
      unwrap(await api.POST("/ai/proposals/apply", { body })),
    // An apply replays configuration operations: every cached screen may be stale.
    onSettled: () => qc.invalidateQueries(),
  });
}

export function useDismissAiProposals(): UseMutationResult<
  Schemas["AiDismissResponse"],
  ApiError,
  Schemas["AiDismissRequest"]
> {
  const qc = useQueryClient();
  return useMutation<
    Schemas["AiDismissResponse"],
    ApiError,
    Schemas["AiDismissRequest"]
  >({
    mutationFn: async (body) =>
      unwrap(await api.POST("/ai/proposals/dismiss", { body })),
    onSettled: () => qc.invalidateQueries({ queryKey: ["ai", "proposals"] }),
  });
}

export function useAiForecasts(
  kind: "upstream" | "capacity",
): UseQueryResult<Schemas["AiForecast"][]> {
  return useQuery({
    queryKey: ["ai", "forecasts", kind],
    queryFn: async () =>
      unwrap(await api.GET("/ai/forecasts", { params: { query: { kind } } })),
  });
}

export function useStartAiQueryLogSearch(): UseMutationResult<
  AiTask,
  ApiError,
  Schemas["AiQueryLogSearchRequest"]
> {
  return useMutation<AiTask, ApiError, Schemas["AiQueryLogSearchRequest"]>({
    mutationFn: async (body) =>
      unwrap(await api.POST("/ai/query-log/search", { body })),
  });
}

export function useStartAiThreatCheck(): UseMutationResult<
  AiTask,
  ApiError,
  Schemas["AiThreatCheckRequest"]
> {
  return useMutation<AiTask, ApiError, Schemas["AiThreatCheckRequest"]>({
    mutationFn: async (body) =>
      unwrap(await api.POST("/ai/threat-check", { body })),
  });
}

export function useCreateAiAssistantSession(): UseMutationResult<
  Schemas["AiAssistantSession"],
  ApiError,
  void
> {
  return useMutation<Schemas["AiAssistantSession"], ApiError, void>({
    mutationFn: async () => unwrap(await api.POST("/ai/assistant/sessions")),
  });
}

export function useAiAssistantSession(
  id: string | null,
): UseQueryResult<Schemas["AiAssistantSession"]> {
  return useQuery({
    queryKey: ["ai", "assistant", id],
    queryFn: async () =>
      unwrap(
        await api.GET("/ai/assistant/sessions/{id}", {
          params: { path: { id: id ?? "" } },
        }),
      ),
    enabled: id !== null,
    refetchInterval: (q) => (taskActive(q.state.data?.task) ? taskPoll : false),
  });
}

export function usePostAiAssistantMessage(
  sessionId: string,
): UseMutationResult<AiTask, ApiError, string> {
  const qc = useQueryClient();
  return useMutation<AiTask, ApiError, string>({
    mutationFn: async (content) =>
      unwrap(
        await api.POST("/ai/assistant/sessions/{id}/messages", {
          params: { path: { id: sessionId } },
          body: { content },
        }),
      ),
    onSuccess: () =>
      qc.invalidateQueries({ queryKey: ["ai", "assistant", sessionId] }),
  });
}

export function useAiRolloutRisk(
  rolloutId: string,
): UseQueryResult<Schemas["AiRolloutRisk"]> {
  return useQuery({
    queryKey: ["ai", "rollout-risk", rolloutId],
    queryFn: async () =>
      unwrap(
        await api.GET("/rollouts/{id}/ai-risk", {
          params: { path: { id: rolloutId } },
        }),
      ),
    refetchInterval: (q) =>
      q.state.data?.status === "pending" ? riskPoll : false,
  });
}

export function useAiListClassification(
  listId: string | null,
): UseQueryResult<Schemas["AiListClassification"]> {
  return useQuery({
    queryKey: ["ai", "list-classification", listId],
    queryFn: async () =>
      unwrap(
        await api.GET("/filter-lists/{id}/ai-classification", {
          params: { path: { id: listId ?? "" } },
        }),
      ),
    enabled: listId !== null,
  });
}
