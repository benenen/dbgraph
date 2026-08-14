import { readonly, ref } from "vue";

interface ActiveReviewOperation {
  token: symbol;
  cancelled: boolean;
}

const active = ref(false);
let current: ActiveReviewOperation | null = null;

export const reviewOperationActive = readonly(active);

export function beginReviewOperation(): symbol | null {
  if (current) return null;
  const token = Symbol("review-operation");
  current = { token, cancelled: false };
  active.value = true;
  return token;
}

export function cancelReviewOperation(token: symbol): void {
  if (current?.token !== token) return;
  current = { ...current, cancelled: true };
}

export function reviewOperationCanContinue(token: symbol): boolean {
  return current?.token === token && !current.cancelled;
}

export function finishReviewOperation(token: symbol): void {
  if (current?.token !== token) return;
  current = null;
  active.value = false;
}
