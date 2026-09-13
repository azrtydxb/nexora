import createClient from "openapi-fetch";
import type { components, paths } from "./schema";

export type Schemas = components["schemas"];

export const api = createClient<paths>({
  baseUrl: "/api/v1",
  credentials: "same-origin",
  headers: { "Content-Type": "application/json" },
});

/** A non-2xx API response. `code` and `message` come from the API's Error body. */
export class ApiError extends Error {
  status: number;
  code: string;

  constructor(status: number, code: string, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
  }
}

/** Returns the response data or throws an ApiError for an error response. */
export function unwrap<T>(r: {
  data?: T;
  error?: { code: string; message: string } | unknown;
  response: Response;
}): T {
  if (!r.response.ok) {
    const e = r.error as { code?: string; message?: string } | undefined;
    throw new ApiError(
      r.response.status,
      e?.code ?? "http_error",
      e?.message ?? `${r.response.status} ${r.response.statusText}`,
    );
  }
  return r.data as T;
}
