// Tiny helper for narrowing the untyped OptimisticOp in rollback functions.
// Returns undefined when op is nullish, otherwise casts to T.

/** Narrow an OptimisticOp to T, returning undefined for nullish values. */
export function asOp<T>(op: unknown): T | undefined {
  return (op === undefined || op === null) ? undefined : op as T;
}
