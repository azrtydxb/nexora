import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { api, ApiError, unwrap, type Schemas } from "@/api/client";

type Category = Schemas["FilterCategory"];
type Update = Schemas["FilterCategoryUpdate"];

/** A source whose license needs the operator's acknowledgement before it takes effect. */
export type LicenseNotice = { key: string; name: string; notice: string };

export function useFilterCategories() {
  return useQuery({
    queryKey: ["filter-categories"],
    queryFn: async () => unwrap(await api.GET("/filter-categories")),
  });
}

export function useUpdateFilterCategory() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ key, body }: { key: string; body: Update }) =>
      unwrap(
        await api.PUT("/filter-categories/{key}", {
          params: { path: { key } },
          body,
        }),
      ),
    onSuccess: async (updated) => {
      qc.setQueryData<Category[]>(["filter-categories"], (all) =>
        all?.map((c) => (c.key === updated.key ? updated : c)),
      );
      await Promise.all([
        qc.invalidateQueries({ queryKey: ["filter-categories"] }),
        qc.invalidateQueries({ queryKey: ["filter-lists"] }),
      ]);
    },
  });
}

/**
 * The sources that `body` would newly turn on and whose license is not free for commercial use.
 * The server also counts categories selected by a policy group; licenseNoticesFromError covers that.
 */
export function needsAcknowledgement(
  category: Category,
  body: Update,
): LicenseNotice[] {
  const toggled = new Map((body.sources ?? []).map((s) => [s.key, s.enabled]));
  return category.sources
    .filter((s) => {
      const before = category.enabled && s.enabled;
      const after = body.enabled && (toggled.get(s.key) ?? s.enabled);
      return after && !before && !s.commercial_use;
    })
    .map((s) => ({ key: s.key, name: s.name, notice: s.notice }));
}

/** The notices of a license_acknowledgement_required refusal, or null for any other error. */
export function licenseNoticesFromError(
  err: unknown,
  categories: Category[] | undefined,
): LicenseNotice[] | null {
  if (
    !(err instanceof ApiError) ||
    err.code !== "license_acknowledgement_required"
  ) {
    return null;
  }
  const names = new Map(
    (categories ?? []).flatMap((c) => c.sources.map((s) => [s.key, s.name])),
  );
  // Each detail message is "<source key>: <notice>".
  const notices = (err.details ?? []).map((d) => {
    const i = d.message.indexOf(": ");
    const key = i < 0 ? d.message : d.message.slice(0, i);
    const notice = i < 0 ? "" : d.message.slice(i + 2);
    return { key, name: names.get(key) ?? key, notice };
  });
  return notices.length > 0
    ? notices
    : [{ key: "", name: "License", notice: err.message }];
}
