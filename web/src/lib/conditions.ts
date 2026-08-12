// Rendering a condition AST as something a person can read. A guard decides
// when a relation applies, so it has to be legible wherever it is shown:
// "Type = 7" is reviewable, a JSON tree is not.

import type { ConditionNode } from "@/api/client";

const OPERATORS: Record<string, string> = {
  eq: "=",
  ne: "<>",
  gt: ">",
  gte: ">=",
  lt: "<",
  lte: "<=",
};

/** Resolves a column node id to a name. Falling back to the id is fine. */
export type NameLookup = (nodeId: string) => string;

export function describeCondition(
  condition: ConditionNode | null | undefined,
  nameOf: NameLookup,
): string {
  if (!condition) return "";
  const of = (child: ConditionNode | null | undefined) => describeCondition(child, nameOf);
  switch (condition.kind) {
    case "column":
    case "column_copy":
      return nameOf(condition.nodeId ?? "");
    case "literal":
      return formatLiteral(condition.literal?.value ?? condition.value);
    case "parameter":
      return `:${condition.parameter ?? ""}`;
    case "compare":
      return `${of(condition.left)} ${OPERATORS[condition.operator ?? ""] ?? condition.operator} ${of(condition.right)}`;
    case "and":
    case "or":
      return (condition.children ?? [])
        .map((child) => `(${of(child)})`)
        .join(` ${condition.kind.toUpperCase()} `);
    case "not":
      return `NOT (${of(condition.operand)})`;
    case "in":
    case "not_in":
      return `${of(condition.left)} ${condition.kind === "in" ? "IN" : "NOT IN"} (${(condition.values ?? []).map(of).join(", ")})`;
    case "is_null":
      return `${of(condition.left)} IS NULL`;
    case "is_not_null":
      return `${of(condition.left)} IS NOT NULL`;
    case "case":
      return (condition.cases ?? [])
        .map((branch) => `WHEN ${of(branch.when)} THEN ${of(branch.then)}`)
        .concat(condition.else ? [`ELSE ${of(condition.else)}`] : [])
        .join(" ");
    default:
      return condition.kind;
  }
}

/** Every column node id a condition mentions, for resolving names up front. */
export function conditionNodeIds(
  condition: ConditionNode | null | undefined,
  into: Set<string> = new Set(),
): Set<string> {
  if (!condition) return into;
  if (condition.nodeId) into.add(condition.nodeId);
  for (const child of [condition.left, condition.right, condition.operand, condition.else]) {
    conditionNodeIds(child, into);
  }
  for (const child of [...(condition.children ?? []), ...(condition.values ?? [])]) {
    conditionNodeIds(child, into);
  }
  for (const branch of condition.cases ?? []) {
    conditionNodeIds(branch.when, into);
    conditionNodeIds(branch.then, into);
  }
  return into;
}

function formatLiteral(value: unknown): string {
  if (value === null || value === undefined) return "NULL";
  if (typeof value === "string") return `'${value}'`;
  return String(value);
}
