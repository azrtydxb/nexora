import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
  type QueryClient,
} from "@tanstack/react-query";

import { api, ApiError, unwrap, type Schemas } from "@/api/client";

type Zone = Schemas["Zone"];

/** Records of one zone shown per page; "Load more" fetches the next. */
export const recordPageSize = 200;

// Record, import and DNSSEC changes bump the zone revision and serial: waiting for the refetch keeps
// the revision later forms send current.
const refreshZone = (qc: QueryClient, zoneId: string) =>
  Promise.all([
    qc.invalidateQueries({ queryKey: ["zones", zoneId] }),
    qc.invalidateQueries({ queryKey: ["zones"], exact: true }),
  ]);

export function useZones() {
  return useQuery({
    queryKey: ["zones"],
    queryFn: async () => unwrap(await api.GET("/zones")),
    refetchInterval: 15_000,
  });
}

export function useZone(zoneId: string) {
  return useQuery({
    queryKey: ["zones", zoneId],
    queryFn: async () =>
      unwrap(
        await api.GET("/zones/{zoneId}", { params: { path: { zoneId } } }),
      ),
  });
}

export function useCreateZone() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: Schemas["ZoneCreate"]) =>
      unwrap(await api.POST("/zones", { body })),
    onSuccess: (z) => {
      qc.setQueryData(["zones", z.id], z);
      return qc.invalidateQueries({ queryKey: ["zones"], exact: true });
    },
  });
}

export function useUpdateZone(zoneId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: Schemas["ZoneUpdate"]) =>
      unwrap(
        await api.PATCH("/zones/{zoneId}", {
          params: { path: { zoneId } },
          body,
        }),
      ),
    onSuccess: (z) => {
      qc.setQueryData(["zones", zoneId], z);
      return qc.invalidateQueries({ queryKey: ["zones"], exact: true });
    },
  });
}

export function useDeleteZone() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (zone: Zone) =>
      unwrap(
        await api.DELETE("/zones/{zoneId}", {
          params: {
            path: { zoneId: zone.id },
            query: { revision: zone.revision },
          },
        }),
      ),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["zones"], exact: true }),
  });
}

export function useRefreshZone(zoneId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async () =>
      unwrap(
        await api.POST("/zones/{zoneId}/refresh", {
          params: { path: { zoneId } },
        }),
      ),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["zones", zoneId] }),
  });
}

/** Pages through a zone's records ordered by owner and type; name is an exact absolute owner. */
export function useRecords(
  zoneId: string,
  filter: { name?: string; type?: string },
) {
  return useInfiniteQuery({
    queryKey: ["zones", zoneId, "records", filter],
    queryFn: async ({ pageParam }) =>
      unwrap(
        await api.GET("/zones/{zoneId}/records", {
          params: {
            path: { zoneId },
            query: {
              name: filter.name || undefined,
              type: filter.type || undefined,
              cursor: pageParam,
              limit: recordPageSize,
            },
          },
        }),
      ),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.next_cursor ?? undefined,
  });
}

/** The current records at one owner and type, fetched on demand (the conflict dialog). */
export async function fetchRecordSet(
  zoneId: string,
  name: string,
  type: string,
): Promise<Schemas["Record"][]> {
  const page = unwrap(
    await api.GET("/zones/{zoneId}/records", {
      params: { path: { zoneId }, query: { name, type } },
    }),
  );
  return page.items;
}

export function useSaveRecord(zoneId: string) {
  const qc = useQueryClient();
  return useMutation<
    Schemas["Record"],
    ApiError,
    { id?: string; revision?: number; input: Schemas["RecordInput"] }
  >({
    mutationFn: async ({ id, revision, input }) =>
      id === undefined
        ? unwrap(
            await api.POST("/zones/{zoneId}/records", {
              params: { path: { zoneId } },
              body: input,
            }),
          )
        : unwrap(
            await api.PUT("/zones/{zoneId}/records/{recordId}", {
              params: { path: { zoneId, recordId: id } },
              body: { ...input, revision: revision ?? 0 },
            }),
          ),
    onSuccess: () => refreshZone(qc, zoneId),
  });
}

export function useDeleteRecord(zoneId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (r: Schemas["Record"]) =>
      unwrap(
        await api.DELETE("/zones/{zoneId}/records/{recordId}", {
          params: {
            path: { zoneId, recordId: r.id },
            query: { revision: r.revision },
          },
        }),
      ),
    onSuccess: () => refreshZone(qc, zoneId),
  });
}

export function useImportZoneFile(zoneId: string) {
  const qc = useQueryClient();
  return useMutation<
    Schemas["ZoneImportResult"],
    ApiError,
    Schemas["ZoneImport"]
  >({
    mutationFn: async (body) =>
      unwrap(
        await api.POST("/zones/{zoneId}/import", {
          params: { path: { zoneId } },
          body,
        }),
      ),
    onSuccess: (res) => {
      qc.setQueryData(["zones", zoneId], res.zone);
      return refreshZone(qc, zoneId);
    },
  });
}

/** Downloads the zone as a BIND master file named "<zone>zone". */
export async function downloadZoneFile(zone: Zone): Promise<void> {
  // openapi-fetch would parse the text/plain body; a plain fetch keeps it as a Blob.
  const res = await fetch(`/api/v1/zones/${zone.id}/export`, {
    credentials: "same-origin",
  });
  if (!res.ok) {
    const e = (await res.json().catch(() => null)) as {
      code?: string;
      message?: string;
    } | null;
    throw new ApiError(
      res.status,
      e?.code ?? "http_error",
      e?.message ?? `${res.status} ${res.statusText}`,
    );
  }
  const url = URL.createObjectURL(await res.blob());
  try {
    const a = document.createElement("a");
    a.href = url;
    a.download = `${zone.name}zone`;
    document.body.appendChild(a);
    a.click();
    a.remove();
  } finally {
    // The click starts the download synchronously; the URL can go once it has.
    window.setTimeout(() => URL.revokeObjectURL(url), 10_000);
  }
}

export function useZoneDnssec(zoneId: string) {
  return useQuery({
    queryKey: ["zones", zoneId, "dnssec"],
    queryFn: async () =>
      unwrap(
        await api.GET("/zones/{zoneId}/dnssec", {
          params: { path: { zoneId } },
        }),
      ),
  });
}

function useDnssecMutation<T>(
  zoneId: string,
  call: (body: T) => Promise<Schemas["ZoneDnssec"]>,
) {
  const qc = useQueryClient();
  return useMutation<Schemas["ZoneDnssec"], ApiError, T>({
    mutationFn: call,
    onSuccess: (v) => {
      qc.setQueryData(["zones", zoneId, "dnssec"], v);
      return refreshZone(qc, zoneId);
    },
  });
}

export function useUpdateZoneDnssec(zoneId: string) {
  return useDnssecMutation(zoneId, async (body: Schemas["ZoneDnssecUpdate"]) =>
    unwrap(
      await api.PUT("/zones/{zoneId}/dnssec", {
        params: { path: { zoneId } },
        body,
      }),
    ),
  );
}

export function useStartKeyRollover(zoneId: string) {
  return useDnssecMutation(zoneId, async (role: "zsk" | "ksk") =>
    unwrap(
      await api.POST("/zones/{zoneId}/dnssec/rollovers", {
        params: { path: { zoneId } },
        body: { role },
      }),
    ),
  );
}

export function useConfirmKskDs(zoneId: string) {
  return useDnssecMutation(zoneId, async (keyId: string) =>
    unwrap(
      await api.POST("/zones/{zoneId}/dnssec/rollovers/ds-published", {
        params: { path: { zoneId } },
        body: { key_id: keyId },
      }),
    ),
  );
}

export function useTsigKeys() {
  return useQuery({
    queryKey: ["tsig-keys"],
    queryFn: async () => unwrap(await api.GET("/tsig-keys")),
  });
}

export function useCreateTsigKey() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: Schemas["TsigKeyCreate"]) =>
      unwrap(await api.POST("/tsig-keys", { body })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["tsig-keys"] }),
  });
}

export function useDeleteTsigKey() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (k: Schemas["TsigKey"]) =>
      unwrap(
        await api.DELETE("/tsig-keys/{keyId}", {
          params: { path: { keyId: k.id }, query: { revision: k.revision } },
        }),
      ),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["tsig-keys"] }),
  });
}
