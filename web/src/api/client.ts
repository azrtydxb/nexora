import createClient from "openapi-fetch";
import type { components, paths } from "./schema";

export type Schemas = components["schemas"];

export const api = createClient<paths>({
  baseUrl: "/api/v1",
  credentials: "same-origin",
  headers: { "Content-Type": "application/json" },
});

/** One problem in submitted zone data. */
export type LineError = { line: number; message: string };

/** A non-2xx API response. `code`, `message` and `details` come from the API's Error body. */
export class ApiError extends Error {
  status: number;
  code: string;
  details?: LineError[];

  constructor(
    status: number,
    code: string,
    message: string,
    details?: LineError[],
  ) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.details = details;
  }
}

/** Returns the response data or throws an ApiError for an error response. */
export function unwrap<T>(r: {
  data?: T;
  error?: { code: string; message: string } | unknown;
  response: Response;
}): T {
  if (!r.response.ok) {
    const e = r.error as
      { code?: string; message?: string; details?: LineError[] } | undefined;
    throw new ApiError(
      r.response.status,
      e?.code ?? "http_error",
      e?.message ?? `${r.response.status} ${r.response.statusText}`,
      e?.details,
    );
  }
  return r.data as T;
}
