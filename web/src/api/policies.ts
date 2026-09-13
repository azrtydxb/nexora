import {
  useMutation,
  useQuery,
  useQueryClient,
  type QueryClient,
} from "@tanstack/react-query";

import { api, unwrap, type Schemas } from "@/api/client";

type PolicyGroupInput = Schemas["PolicyGroupInput"];
type RewriteInput = Schemas["RewriteInput"];

// Groups, safe search and rewrites feed one snapshot and a group delete removes its rewrites,
// so every mutation refreshes all three.
async function invalidatePolicy(qc: QueryClient) {
  await Promise.all([
    qc.invalidateQueries({ queryKey: ["policy-groups"] }),
    qc.invalidateQueries({ queryKey: ["safe-search"] }),
    qc.invalidateQueries({ queryKey: ["rewrites"] }),
  ]);
}

export function usePolicyGroups() {
  return useQuery({
    queryKey: ["policy-groups"],
    queryFn: async () => unwrap(await api.GET("/policy-groups")),
  });
}

export function usePolicyGroup(id: string) {
  return useQuery({
    queryKey: ["policy-groups", id],
    queryFn: async () =>
      unwrap(
        await api.GET("/policy-groups/{id}", { params: { path: { id } } }),
      ),
    enabled: false, // fetched on demand (the edit dialog's Reload)
  });
}

export function useCreatePolicyGroup() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: PolicyGroupInput) =>
      unwrap(await api.POST("/policy-groups", { body })),
    onSuccess: () => invalidatePolicy(qc),
  });
}

export function useUpdatePolicyGroup() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({
      id,
      body,
    }: {
      id: string;
      body: Schemas["PolicyGroupUpdate"];
    }) =>
      unwrap(
        await api.PUT("/policy-groups/{id}", {
          params: { path: { id } },
          body,
        }),
      ),
    onSuccess: () => invalidatePolicy(qc),
  });
}

export function useDeletePolicyGroup() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, revision }: { id: string; revision: number }) =>
      unwrap(
        await api.DELETE("/policy-groups/{id}", {
          params: { path: { id }, query: { revision } },
        }),
      ),
    onSuccess: () => invalidatePolicy(qc),
  });
}

export function useGlobalSafeSearch() {
  return useQuery({
    queryKey: ["safe-search"],
    queryFn: async () => unwrap(await api.GET("/safe-search")),
  });
}

export function useUpdateGlobalSafeSearch() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: Schemas["GlobalSafeSearch"]) =>
      unwrap(await api.PUT("/safe-search", { body })),
    onSuccess: () => invalidatePolicy(qc),
  });
}

/** scope is `all`, `global` or a policy group id. */
export function useRewrites(scope: string) {
  return useQuery({
    queryKey: ["rewrites", scope],
    queryFn: async () =>
      unwrap(await api.GET("/rewrites", { params: { query: { scope } } })),
  });
}

export function useCreateRewrite() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: RewriteInput) =>
      unwrap(await api.POST("/rewrites", { body })),
    onSuccess: () => invalidatePolicy(qc),
  });
}

export function useUpdateRewrite() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({
      id,
      body,
    }: {
      id: string;
      body: Schemas["RewriteUpdate"];
    }) =>
      unwrap(
        await api.PUT("/rewrites/{id}", { params: { path: { id } }, body }),
      ),
    onSuccess: () => invalidatePolicy(qc),
  });
}

export function useDeleteRewrite() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, revision }: { id: string; revision: number }) =>
      unwrap(
        await api.DELETE("/rewrites/{id}", {
          params: { path: { id }, query: { revision } },
        }),
      ),
    onSuccess: () => invalidatePolicy(qc),
  });
}

export function useDnsTlsStatus() {
  return useQuery({
    queryKey: ["dns-tls"],
    queryFn: async () => unwrap(await api.GET("/settings/dns-tls")),
  });
}
