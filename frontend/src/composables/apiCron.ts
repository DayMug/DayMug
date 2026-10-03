import { jsonRequestInit, request } from "./apiClient";
import type { CronJob, CronJobInput } from "./apiTypes";

export async function fetchCronJobs(): Promise<CronJob[]> {
  return request<CronJob[]>(`/api/cron-jobs`, undefined, { label: "fetch scheduled tasks" });
}

export async function createCronJob(input: CronJobInput): Promise<CronJob> {
  return request<CronJob>(`/api/cron-jobs`, jsonRequestInit("POST", input), {
    label: "create scheduled task",
    errorBody: "message",
  });
}

export async function updateCronJob(id: string, input: CronJobInput): Promise<CronJob> {
  return request<CronJob>(
    `/api/cron-jobs/${encodeURIComponent(id)}`,
    jsonRequestInit("PUT", input),
    { label: "update scheduled task", errorBody: "message" },
  );
}

export async function deleteCronJob(id: string): Promise<void> {
  await request<void>(
    `/api/cron-jobs/${encodeURIComponent(id)}`,
    { method: "DELETE" },
    { label: "delete scheduled task", expect: "none" },
  );
}
