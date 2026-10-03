import { request } from "./apiClient";
import type { ModelRegistry } from "./apiTypes";

// fetchModels returns the per-provider model registry used to populate the
// chat-header model picker. Call loadModelRegistry (useModelRegistry.ts)
// instead: it is the one app-wide cache, and the only one an admin's provider
// edit invalidates.
export async function fetchModels(): Promise<ModelRegistry> {
  return request<ModelRegistry>(`/api/models`, undefined, { label: "fetch models" });
}
