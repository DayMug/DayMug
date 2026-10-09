// Global singleton store: the workspace upload pipeline. An upload belongs to
// the page, not to the panel that started it — switching conversation or agent
// rebinds the panel, and on mobile leaving the Files tab unmounts it, yet the
// queued files must keep going to the workspace they were dropped on. Every
// mounted WorkspacePanel renders this one pipeline's progress; the most
// recently mounted one ("host") also shows its conflict dialog and refreshes
// its listing when the run ends. See src/stores/index.ts for the conventions
// every store in this directory follows.
import { computed, ref, shallowRef, type Ref } from "vue";

export type UploadConflictChoice = "cancel" | "skip" | "overwrite" | "rename";

export interface UploadProgressState {
  loaded: number;
  total: number;
  speed: number;
  fileCount: number;
  doneCount: number;
}

export interface UploadConflictPrompt {
  fileName: string;
  targetFolder: string;
  // Files still queued behind this one; "apply to all" is only offered when
  // there is something left to apply it to.
  remaining: number;
  resolve: (answer: { choice: UploadConflictChoice; applyAll: boolean }) => void;
}

export type UploadResultStatus =
  | "pending"
  | "uploading"
  | "done"
  | "skipped"
  | "failed"
  | "cancelled";

// One row of the upload list. Rows outlive the run: the panel keeps showing
// what succeeded and what didn't until the user dismisses the list.
export interface UploadResult {
  id: number;
  name: string;
  status: UploadResultStatus;
  // How a name conflict was resolved, when the file needed one.
  resolved?: "overwrite" | "rename";
  error?: string;
}

export interface UploadQueueItem {
  userId: string;
  targetPath: string;
  file: File;
  resultId: number;
}

// The panel-side hooks a run reports back through.
export interface UploadHost {
  currentPath: Ref<string>;
  error: Ref<string>;
  loadDir: (path: string) => Promise<void>;
}

export const uploading = ref(false);
export const uploadProgress = ref<UploadProgressState | null>(null);
export const uploadCancelled = ref(false);
export const uploadAbortController = ref<AbortController | null>(null);
export const conflictUpload = ref<UploadConflictPrompt | null>(null);
export const uploadResults = ref<UploadResult[]>([]);
export const uploadResultSeq = { next: 0 };

export function setUploadResult(id: number, patch: Partial<Omit<UploadResult, "id">>) {
  const i = uploadResults.value.findIndex((r) => r.id === id);
  if (i >= 0) uploadResults.value[i] = { ...uploadResults.value[i], ...patch };
}

// Rows that never got their turn once the run stops early.
export function cancelUnfinishedResults() {
  for (const r of uploadResults.value) {
    if (r.status === "pending" || r.status === "uploading") r.status = "cancelled";
  }
}

export function dismissUploadResults() {
  if (!uploading.value) uploadResults.value = [];
}

// Mounted panels in mount order; the last one is the host. Shallow so the
// hosts keep their refs instead of being unwrapped.
export const uploadHosts = shallowRef<UploadHost[]>([]);
export const uploadHost = computed(() => uploadHosts.value[uploadHosts.value.length - 1] ?? null);

// Identifies the live run: an abandoned run must not clear the state of the
// run that replaced it.
export const uploadRun = { id: 0 };
// Files waiting behind the in-flight one. Belongs to the live run.
export const uploadQueue = { items: [] as UploadQueueItem[] };

export function settleUploadConflict(choice: UploadConflictChoice, applyAll = false) {
  const c = conflictUpload.value;
  if (!c) return;
  conflictUpload.value = null;
  c.resolve({ choice, applyAll });
}

export function resetWorkspaceUploadStore() {
  uploadRun.id++;
  settleUploadConflict("cancel");
  uploadAbortController.value?.abort();
  uploading.value = false;
  uploadProgress.value = null;
  uploadCancelled.value = false;
  uploadAbortController.value = null;
  uploadQueue.items = [];
  uploadHosts.value = [];
  uploadResults.value = [];
}
