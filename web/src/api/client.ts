// One place that knows how dbgraph talks: the response envelope, the CSRF
// header, and what a 401 means.

export interface Project {
  id: string;
  name: string;
  description: string;
  createdAt: string;
  updatedAt: string;
}

export interface DataSource {
  id: string;
  projectId: string;
  name: string;
  kind: string;
  /** Present only for Admin; the connection string itself is never returned. */
  dsnEnvironment?: string;
  createdAt?: string;
}

export interface Job {
  id: string;
  projectId: string;
  type: string;
  status: string;
  errorCode: string;
  createdAt: string;
}

export interface Session {
  actor: string;
  role: string;
  csrfToken: string;
}

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;

  constructor(message: string, status: number, code: string) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

/** Thrown when the session is missing or expired, so callers can route to login. */
export class UnauthenticatedError extends ApiError {}

let csrfToken = "";

export function setCsrfToken(token: string): void {
  csrfToken = token;
}

interface Envelope<T> {
  data: T;
  error: { code: string; message: string } | null;
  success: boolean;
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers);
  if (init.body) headers.set("Content-Type", "application/json");
  if (init.method && init.method !== "GET") headers.set("X-CSRF-Token", csrfToken);

  const response = await fetch(path, { ...init, headers, credentials: "same-origin" });

  let envelope: Envelope<T>;
  try {
    envelope = (await response.json()) as Envelope<T>;
  } catch {
    throw new ApiError(`Request failed with status ${response.status}.`, response.status, "UNPARSEABLE");
  }

  if (!response.ok || !envelope.success) {
    const code = envelope.error?.code ?? "UNKNOWN";
    const message = envelope.error?.message ?? `Request failed with status ${response.status}.`;
    if (response.status === 401) throw new UnauthenticatedError(message, response.status, code);
    throw new ApiError(message, response.status, code);
  }
  return envelope.data;
}

export const api = {
  signIn: (token: string) =>
    request<Session>("/login", { method: "POST", body: JSON.stringify({ token }) }),

  signOut: () => request<unknown>("/logout", { method: "POST", body: JSON.stringify({}) }),

  session: () => request<Session>("/api/v1/session"),

  listProjects: () => request<Project[]>("/api/v1/projects?limit=200"),

  createProject: (input: { name: string; description: string; reason: string }) =>
    request<Project>("/api/v1/projects", { method: "POST", body: JSON.stringify(input) }),

  listDataSources: (projectId: string) =>
    request<DataSource[]>(`/api/v1/projects/${projectId}/data-sources?limit=200`),

  createDataSource: (
    projectId: string,
    input: { name: string; dsnEnvironment: string; dsn: string; reason: string },
  ) =>
    request<DataSource>(`/api/v1/projects/${projectId}/data-sources`, {
      method: "POST",
      body: JSON.stringify({ kind: "MYSQL", ...input }),
    }),

  startScan: (projectId: string, dataSourceId: string, reason: string) =>
    request<Job>(`/api/v1/projects/${projectId}/data-sources/${dataSourceId}/schema-scan-jobs`, {
      method: "POST",
      body: JSON.stringify({ mode: "FULL", reason }),
    }),

  job: (projectId: string, jobId: string) =>
    request<Job>(`/api/v1/projects/${projectId}/schema-scan-jobs/${jobId}`),
};
