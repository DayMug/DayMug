import { request } from "./apiClient";
import type { UsageInsightsQuery, UsageInsightsResponse } from "./apiTypes";

export async function adminFetchUsageInsights(
  q?: UsageInsightsQuery,
): Promise<UsageInsightsResponse> {
  const params = new URLSearchParams();
  if (q) {
    for (const [key, value] of Object.entries(q)) {
      if (value !== undefined && value !== "") params.set(key, String(value));
    }
  }
  const suffix = params.size ? `?${params.toString()}` : "";
  return request<UsageInsightsResponse>(`/api/admin/usage/insights${suffix}`, undefined, {
    label: "admin/usage/insights",
    errorBody: "message",
  });
}

export async function fetchUsageInsights(q?: UsageInsightsQuery): Promise<UsageInsightsResponse> {
  const params = new URLSearchParams();
  if (q) {
    for (const [key, value] of Object.entries(q)) {
      if (value !== undefined && value !== "") params.set(key, String(value));
    }
  }
  const suffix = params.size ? `?${params.toString()}` : "";
  return request<UsageInsightsResponse>(`/api/usage/insights${suffix}`, undefined, {
    label: "usage/insights",
    errorBody: "message",
  });
}
