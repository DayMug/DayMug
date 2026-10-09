// Filesystem locations are shown verbatim so the value displayed in settings
// matches the path used by the agent and server logs.
export function displayPath(absPath: string): string {
  return absPath || "/";
}
