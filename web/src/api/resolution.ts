import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { api, unwrap, type Schemas } from "@/api/client";

export function useResolutionSettings() {
  return useQuery({
    queryKey: ["resolution"],
    queryFn: async () => unwrap(await api.GET("/resolution")),
  });
}

export function useUpdateResolutionSettings() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: Schemas["ResolutionSettings"]) =>
      unwrap(await api.PUT("/resolution", { body })),
    onSuccess: (saved) => qc.setQueryData(["resolution"], saved),
  });
}

export function useForwardZones() {
  return useQuery({
    queryKey: ["forward-zones"],
    queryFn: async () => unwrap(await api.GET("/forward-zones")),
  });
}

export function useCreateForwardZone() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: Schemas["ForwardZoneInput"]) =>
      unwrap(await api.POST("/forward-zones", { body })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["forward-zones"] }),
  });
}

export function useUpdateForwardZone() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({
      id,
      body,
    }: {
      id: string;
      body: Schemas["ForwardZoneUpdate"];
    }) =>
      unwrap(
        await api.PUT("/forward-zones/{id}", {
          params: { path: { id } },
          body,
        }),
      ),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["forward-zones"] }),
  });
}

export function useDeleteForwardZone() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, revision }: { id: string; revision: number }) =>
      unwrap(
        await api.DELETE("/forward-zones/{id}", {
          params: { path: { id }, query: { revision } },
        }),
      ),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["forward-zones"] }),
  });
}

export function useDnssecSettings() {
  return useQuery({
    queryKey: ["dnssec", "settings"],
    queryFn: async () => unwrap(await api.GET("/dnssec/settings")),
  });
}

export function useUpdateDnssecSettings() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: Schemas["DnssecSettings"]) =>
      unwrap(await api.PUT("/dnssec/settings", { body })),
    onSuccess: (saved) => qc.setQueryData(["dnssec", "settings"], saved),
  });
}

export function useDnssecStatus() {
  return useQuery({
    queryKey: ["dnssec", "status"],
    queryFn: async () => unwrap(await api.GET("/dnssec/status")),
    refetchInterval: 10_000,
  });
}

export function useTrustAnchors() {
  return useQuery({
    queryKey: ["dnssec", "anchors"],
    queryFn: async () => unwrap(await api.GET("/dnssec/trust-anchors")),
  });
}

export function useCreateTrustAnchor() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: Schemas["TrustAnchorInput"]) =>
      unwrap(await api.POST("/dnssec/trust-anchors", { body })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["dnssec", "anchors"] }),
  });
}

export function useDeleteTrustAnchor() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (id: string) =>
      unwrap(
        await api.DELETE("/dnssec/trust-anchors/{id}", {
          params: { path: { id } },
        }),
      ),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["dnssec", "anchors"] }),
  });
}

export function useNegativeTrustAnchors() {
  return useQuery({
    queryKey: ["dnssec", "ntas"],
    queryFn: async () =>
      unwrap(await api.GET("/dnssec/negative-trust-anchors")),
  });
}

export function useCreateNegativeTrustAnchor() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: Schemas["NegativeTrustAnchorInput"]) =>
      unwrap(await api.POST("/dnssec/negative-trust-anchors", { body })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["dnssec", "ntas"] }),
  });
}

export function useDeleteNegativeTrustAnchor() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (id: string) =>
      unwrap(
        await api.DELETE("/dnssec/negative-trust-anchors/{id}", {
          params: { path: { id } },
        }),
      ),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["dnssec", "ntas"] }),
  });
}

export function useRpzZones() {
  return useQuery({
    queryKey: ["rpz-zones"],
    queryFn: async () => unwrap(await api.GET("/rpz-zones")),
    refetchInterval: 10_000,
  });
}

/** One zone, fetched fresh while the component using it (the edit dialog) is mounted. */
export function useRpzZone(id: string) {
  return useQuery({
    queryKey: ["rpz-zones", id],
    queryFn: async () =>
      unwrap(await api.GET("/rpz-zones/{id}", { params: { path: { id } } })),
    staleTime: 0,
    gcTime: 0,
  });
}

export function useCreateRpzZone() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: Schemas["RpzZoneInput"]) =>
      unwrap(await api.POST("/rpz-zones", { body })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["rpz-zones"] }),
  });
}

export function useUpdateRpzZone() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({
      id,
      body,
    }: {
      id: string;
      body: Schemas["RpzZoneUpdate"];
    }) =>
      unwrap(
        await api.PUT("/rpz-zones/{id}", { params: { path: { id } }, body }),
      ),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["rpz-zones"] }),
  });
}

export function useDeleteRpzZone() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, revision }: { id: string; revision: number }) =>
      unwrap(
        await api.DELETE("/rpz-zones/{id}", {
          params: { path: { id }, query: { revision } },
        }),
      ),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["rpz-zones"] }),
  });
}

export function useUploadRpzZoneFile() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({
      id,
      body,
    }: {
      id: string;
      body: Schemas["RpzZoneFile"];
    }) =>
      unwrap(
        await api.PUT("/rpz-zones/{id}/file", {
          params: { path: { id } },
          body,
        }),
      ),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["rpz-zones"] }),
  });
}

export function useRefreshRpzZone() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (id: string) =>
      unwrap(
        await api.POST("/rpz-zones/{id}/refresh", {
          params: { path: { id } },
        }),
      ),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["rpz-zones"] }),
  });
}

export function useReorderRpzZones() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (ids: string[]) =>
      unwrap(await api.PUT("/rpz-zones/order", { body: { ids } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["rpz-zones"] }),
  });
}
