import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "@/api/client";

// These contracts come from the M8 OpenAPI. Regenerate schema.d.ts after backend integration.
export function useCatalogZones() {
  return useQuery({
    queryKey: ["catalog-zones"],
    queryFn: async () => unwrap(await api.GET("/catalog-zones")),
    refetchInterval: 15_000,
  });
}
export function useCatalogZone(id: string) {
  return useQuery({
    queryKey: ["catalog-zones", id],
    enabled: !!id,
    queryFn: async () =>
      unwrap(
        await api.GET("/catalog-zones/{catalogZoneId}", {
          params: { path: { catalogZoneId: id } },
        }),
      ),
    refetchInterval: 15_000,
  });
}
export function useCreateCatalogZone() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: Schemas["CatalogZoneCreate"]) =>
      unwrap(await api.POST("/catalog-zones", { body })),
    onSuccess: () =>
      Promise.all([
        qc.invalidateQueries({ queryKey: ["catalog-zones"] }),
        qc.invalidateQueries({ queryKey: ["zones"] }),
      ]),
  });
}
export function useDeleteCatalogZone() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (id: string) =>
      unwrap(
        await api.DELETE("/catalog-zones/{catalogZoneId}", {
          params: { path: { catalogZoneId: id } },
        }),
      ),
    onSuccess: (_, id) => {
      qc.removeQueries({ queryKey: ["catalog-zones", id] });
      return Promise.all([
        qc.invalidateQueries({ queryKey: ["catalog-zones"] }),
        qc.invalidateQueries({ queryKey: ["zones"] }),
      ]);
    },
  });
}
export function useOdohSettings() {
  return useQuery({
    queryKey: ["odoh"],
    queryFn: async () => unwrap(await api.GET("/odoh")),
  });
}
export function useUpdateOdohSettings() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: Schemas["OdohSettingsUpdate"]) =>
      unwrap(await api.PUT("/odoh", { body })),
    onSuccess: (saved) => {
      qc.setQueryData(["odoh"], saved);
    },
  });
}
export function useRotateOdohKey() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async () => unwrap(await api.POST("/odoh/rotate-key")),
    onSuccess: (saved) => {
      qc.setQueryData(["odoh"], saved);
    },
  });
}
