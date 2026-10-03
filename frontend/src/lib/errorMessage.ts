// errorMessage turns whatever a `catch` caught into display text. An Error
// contributes its message; anything else (a rejected string, a thrown object)
// becomes `fallback` when the caller has localized copy for that case, or its
// String() form otherwise.
export function errorMessage(e: unknown, fallback?: string): string {
  if (e instanceof Error) return e.message;
  return fallback ?? String(e);
}
