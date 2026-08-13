// One place that knows how dbgraph talks: the response envelope, the CSRF
// header, and what a 401 means. The server rejects unknown JSON fields, so
// every body below is built field by field instead of spread from form state.

export interface DataSource {
  id: string;
  name: string;
  kind: string;
  /** Present only for Admin; the connection string itself is never returned. */
  dsnEnvironment?: string;
  createdAt?: string;
}

export interface TableSummary {
  id: string;
  name: string;
  qualifiedName: string;
}

export interface TableColumn {
  id: string;
  name: string;
  dataType: string;
  nullable: boolean;
  ordinal: number;
}

export interface TableIndex {
  name: string;
  unique: boolean;
  primary: boolean;
  columns: string[];
}

export interface TableDetail {
  id: string;
  name: string;
  qualifiedName: string;
  columns: TableColumn[];
  indexes: TableIndex[];
}

export interface Evidence {
  kind: string;
  repository: string;
  commit: string;
  file: string;
  symbol: string;
  startLine: number;
  endLine: number;
}

/** A condition AST node. Shape is validated server-side; the console only reads it. */
export interface ConditionNode {
  kind: string;
  operator?: string;
  nodeId?: string;
  literal?: { type: string; value: unknown };
  valueType?: string;
  value?: unknown;
  parameter?: string;
  left?: ConditionNode;
  right?: ConditionNode;
  operand?: ConditionNode;
  children?: ConditionNode[];
  values?: ConditionNode[];
  cases?: { when: ConditionNode; then: ConditionNode }[];
  else?: ConditionNode;
}

export interface Revision {
  id: string;
  relationId: string;
  revisionNo: number;
  kind: string;
  sourceNodeId: string;
  targetNodeId: string;
  guard: ConditionNode | null;
  selector: ConditionNode | null;
  transform: ConditionNode;
  confidence: number;
  evidence: Evidence[];
  actor: string;
  reason: string;
  requestId: string;
  createdAt: string;
}

export interface Relation {
  id: string;
  type: string;
  latestRevisionNo: number;
  status: string;
  effective: boolean;
  active: Revision | null;
  proposed: Revision | null;
  createdAt: string;
}

export interface CatalogNode {
  id: string;
  name: string;
  qualifiedName: string;
  kind: string;
  dataType: string;
}

export interface RelationEdge {
  relationId: string;
  sourceTableId: string;
  targetTableId: string;
  sourceColumn: string;
  targetColumn: string;
  conditional: boolean;
  confidence: number;
  guard?: ConditionNode;
}

export interface RelationGraph {
  tables: TableSummary[];
  edges: RelationEdge[];
  truncated: boolean;
}

export interface Job {
  id: string;
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

let signedOut: (() => void) | null = null;

/**
 * Registers what happens when the server says the session is gone. Every call
 * routes through here, so an expired session sends you to the sign-in screen
 * instead of failing whatever you were in the middle of.
 */
export function onSignedOut(handler: () => void): void {
  signedOut = handler;
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
    if (response.status === 401) {
      csrfToken = "";
      signedOut?.();
      throw new UnauthenticatedError(message, response.status, code);
    }
    throw new ApiError(message, response.status, code);
  }
  return envelope.data;
}

export const api = {
  signIn: (token: string) =>
    request<Session>("/login", { method: "POST", body: JSON.stringify({ token }) }),

  signOut: () => request<unknown>("/logout", { method: "POST", body: JSON.stringify({}) }),

  session: () => request<Session>("/api/v1/session"),

  listAllDataSources: () => request<DataSource[]>("/api/v1/data-sources"),

  // An empty dsn leaves the stored connection string exactly as it is.
  updateDataSource: (
    dataSourceId: string,
    input: { name: string; dsnEnvironment: string; dsn: string; reason: string },
  ) =>
    request<DataSource>(`/api/v1/data-sources/${dataSourceId}/update`, {
      method: "POST",
      body: JSON.stringify({
        name: input.name,
        dsnEnvironment: input.dsnEnvironment,
        dsn: input.dsn,
        reason: input.reason,
      }),
    }),

  deleteDataSource: (dataSourceId: string) =>
    request<unknown>(`/api/v1/data-sources/${dataSourceId}/delete`, {
      method: "POST",
      body: JSON.stringify({}),
    }),

  createDataSource: (input: {
    name: string;
    dsnEnvironment: string;
    dsn: string;
    reason: string;
  }) =>
    request<DataSource>("/api/v1/data-sources", {
      method: "POST",
      body: JSON.stringify({
        kind: "MYSQL",
        name: input.name,
        dsnEnvironment: input.dsnEnvironment,
        dsn: input.dsn,
        reason: input.reason,
      }),
    }),

  startScan: (dataSourceId: string, reason: string) =>
    request<Job>(`/api/v1/data-sources/${dataSourceId}/schema-scan-jobs`, {
      method: "POST",
      body: JSON.stringify({ mode: "FULL", reason }),
    }),

  listTables: (dataSourceId: string, filter: string) =>
    request<{ tables: TableSummary[]; truncated: boolean }>(
      `/api/v1/data-sources/${dataSourceId}/tables?q=${encodeURIComponent(filter)}`,
    ),

  tableDetail: (tableId: string) => request<TableDetail>(`/api/v1/tables/${tableId}`),

  listProposals: () =>
    request<{ relations: Relation[]; truncated: boolean }>("/api/v1/relation-proposals"),

  node: (nodeId: string) => request<CatalogNode>(`/api/v1/nodes/${nodeId}`),

  reviewRelation: (
    relationId: string,
    input: { expectedRevisionNo: number; decision: "APPROVE" | "REJECT"; reason: string },
  ) =>
    request<Relation>(`/api/v1/relations/${relationId}/reviews`, {
      method: "POST",
      body: JSON.stringify({
        expectedRevisionNo: input.expectedRevisionNo,
        decision: input.decision,
        reason: input.reason,
      }),
    }),

  relationGraph: (dataSourceId: string) =>
    request<RelationGraph>(`/api/v1/data-sources/${dataSourceId}/relation-graph`),

  job: (jobId: string) => request<Job>(`/api/v1/schema-scan-jobs/${jobId}`),
};
