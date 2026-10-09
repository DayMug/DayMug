import { computed, getCurrentScope, onScopeDispose, type Ref } from "vue";
import type { Router } from "vue-router";
import { UploadConflictError, type UploadOptions } from "@/composables/useFileApi";
import { checkFolderFileLimit } from "@/lib/uploadHelpers";
import { errorMessage } from "@/lib/errorMessage";
import { useConfirm } from "@/composables/useConfirm";
import { i18n } from "@/i18n";
import {
  cancelUnfinishedResults,
  conflictUpload,
  dismissUploadResults,
  setUploadResult,
  settleUploadConflict,
  uploadAbortController,
  uploadCancelled,
  uploadHost,
  uploadHosts,
  uploadProgress,
  uploadQueue,
  uploadResults,
  uploadResultSeq,
  uploadRun,
  uploading,
  type UploadConflictChoice,
  type UploadHost,
} from "@/stores/workspaceUploadStore";

export type {
  UploadConflictChoice,
  UploadProgressState,
  UploadResult,
} from "@/stores/workspaceUploadStore";

export interface UseWorkspaceUploadOptions {
  userId: Ref<string>;
  currentPath: Ref<string>;
  // Listing error surface shared with the rest of the panel — upload
  // failures land in the same banner as listing failures.
  error: Ref<string>;
  uploadFiles: (
    userId: string,
    path: string,
    files: File[],
    options?: UploadOptions,
  ) => Promise<void>;
  loadDir: (path: string) => Promise<void>;
}

// A reload or tab close would kill the run outright, so the browser asks first.
function warnBeforeUnload(e: Event) {
  if (!uploading.value) return;
  e.preventDefault();
  (e as unknown as { returnValue: string }).returnValue = "";
}

function releaseUpload() {
  window.removeEventListener("beforeunload", warnBeforeUnload);
  cancelUnfinishedResults();
  uploadQueue.items = [];
  uploadProgress.value = null;
  uploadAbortController.value = null;
  uploadCancelled.value = false;
  uploading.value = false;
}

function cancelUpload() {
  if (!uploadAbortController.value) return;
  uploadCancelled.value = true;
  uploadAbortController.value.abort();
}

// Called when the user confirms leaving the workspace pages mid-upload. The
// in-flight batch is aborted and abandoned, and the shared state is released
// immediately so a fresh upload can start without waiting for it to unwind.
export function abortActiveUpload() {
  uploadRun.id++;
  // A batch parked on the conflict dialog would never resume otherwise.
  settleUploadConflict("cancel");
  uploadAbortController.value?.abort();
  releaseUpload();
}

const WORKSPACE_ROUTES = new Set<unknown>(["chat", "conversations", "file"]);

// Switching conversation or agent keeps the upload running, but every other
// page has no workspace panel to show its progress or ask about conflicts,
// so leaving for one asks first and cancels the remaining files on confirm.
export function installUploadLeaveGuard(router: Router) {
  router.beforeEach(async (to) => {
    if (!uploading.value || WORKSPACE_ROUTES.has(to.name)) return true;
    const ok = await useConfirm().confirm({
      message: i18n.global.t("workspace.uploadLeaveConfirm"),
      confirmText: i18n.global.t("workspace.uploadLeave"),
      variant: "destructive",
    });
    // The run may have finished while the dialog was open.
    if (ok && uploading.value) abortActiveUpload();
    return ok;
  });
}

// useWorkspaceUpload binds a workspace panel to the app-wide upload pipeline
// (src/stores/workspaceUploadStore): progress / speed for the bottom progress
// bar, the AbortController behind the Cancel button, and the 409-conflict
// prompt loop. The run outlives the panel that started it, so switching
// conversation or agent keeps uploading into the workspace each file was
// dropped on. While the calling panel is mounted it is a host: the newest host
// shows the conflict dialog and has its listing refreshed when a run ends.
export function useWorkspaceUpload({
  userId,
  currentPath,
  error,
  uploadFiles,
  loadDir,
}: UseWorkspaceUploadOptions) {
  const host: UploadHost = { currentPath, error, loadDir };
  uploadHosts.value = [...uploadHosts.value, host];
  if (getCurrentScope()) {
    onScopeDispose(() => {
      uploadHosts.value = uploadHosts.value.filter((h) => h !== host);
    });
  }
  const isHost = computed(() => uploadHost.value === host);

  function askUploadConflict(
    fileName: string,
    targetFolder: string,
    remaining: number,
  ): Promise<{ choice: UploadConflictChoice; applyAll: boolean }> {
    return new Promise((resolve) => {
      conflictUpload.value = { fileName, targetFolder, remaining, resolve };
    });
  }

  // Refresh whichever panel is showing when the run ends. Its listing errors
  // are surfaced by that panel's browser composable.
  async function reloadHost() {
    const h = uploadHost.value;
    if (!h) return;
    try {
      await h.loadDir(h.currentPath.value);
    } catch {
      // already surfaced by the panel
    }
  }

  // Single upload pipeline shared by every entry point (file picker, folder
  // picker, drag-drop on background, drag-drop on a folder card). It rejects
  // folder uploads that exceed the per-folder file cap, sums the byte total
  // up front, and pumps progress + rolling-average speed into uploadProgress
  // so the bottom progress bar can render.
  //
  // Files are uploaded one at a time. This makes Cancel cleanly granular:
  // already-completed files stay on disk, the in-flight file's partial body
  // is rejected by the multipart parser before any SaveUploadedFile call, and
  // queued files never start. Total/loaded counters span the whole run.
  //
  // Files added while a run is live join its queue instead of starting a
  // second run: the progress bar, the Cancel button and the conflict choice
  // all keep describing one pipeline. The call then returns immediately.
  async function performUpload(targetPath: string, files: File[]): Promise<void> {
    if (files.length === 0) return;
    const limitMsg = checkFolderFileLimit(files);
    if (limitMsg) {
      error.value = limitMsg;
      return;
    }
    // Each file is pinned to the agent whose panel it was dropped on: the
    // run may outlive several agent switches.
    // A new run replaces the previous run's finished list.
    if (!uploading.value) uploadResults.value = [];
    const added = files.map((file) => {
      const resultId = uploadResultSeq.next++;
      uploadResults.value.push({
        id: resultId,
        name: file.webkitRelativePath || file.name,
        status: "pending",
      });
      return { userId: userId.value, targetPath, file, resultId };
    });
    const addedBytes = files.reduce((sum, f) => sum + (f.size || 0), 0);
    if (uploading.value) {
      uploadQueue.items.push(...added);
      if (uploadProgress.value) {
        uploadProgress.value = {
          ...uploadProgress.value,
          total: uploadProgress.value.total + addedBytes,
          fileCount: uploadProgress.value.fileCount + files.length,
        };
      }
      return;
    }
    const run = ++uploadRun.id;
    uploading.value = true;
    window.addEventListener("beforeunload", warnBeforeUnload);
    uploadQueue.items = added;
    const queue = uploadQueue.items;
    const startTime = Date.now();
    let completedBytes = 0;
    let doneCount = 0;
    uploadProgress.value = {
      loaded: 0,
      total: addedBytes,
      speed: 0,
      fileCount: files.length,
      doneCount: 0,
    };
    uploadCancelled.value = false;
    const controller = new AbortController();
    uploadAbortController.value = controller;
    // Set when the user ticks "apply to all" on a conflict: every later
    // conflict in the same run takes that choice without a dialog.
    let batchOnConflict: "overwrite" | "rename" | "skip" | null = null;
    try {
      outer: while (queue.length > 0) {
        const { userId: fileUserId, targetPath: fileTarget, file, resultId } = queue.shift()!;
        if (controller.signal.aborted) break;
        if (run !== uploadRun.id) break;
        setUploadResult(resultId, { status: "uploading" });
        let attemptOnConflict: "overwrite" | "rename" | null =
          batchOnConflict === "skip" ? null : batchOnConflict;
        // Retry loop: on a 409 we ask the user, then re-issue the same
        // request with their chosen on_conflict mode. Skip drops just this
        // file; cancel breaks the whole run.
        while (true) {
          try {
            await uploadFiles(fileUserId, fileTarget, [file], {
              signal: controller.signal,
              onConflict: attemptOnConflict ?? undefined,
              onProgress: ({ loaded }) => {
                if (run !== uploadRun.id || !uploadProgress.value) return;
                const elapsedSec = Math.max((Date.now() - startTime) / 1000, 0.001);
                const overall = completedBytes + loaded;
                uploadProgress.value = {
                  ...uploadProgress.value,
                  loaded: overall,
                  speed: overall / elapsedSec,
                  doneCount,
                };
              },
            });
            setUploadResult(resultId, {
              status: "done",
              resolved: attemptOnConflict ?? undefined,
            });
            break;
          } catch (e) {
            if (e instanceof DOMException && e.name === "AbortError") {
              break outer;
            }
            if (e instanceof UploadConflictError) {
              let choice: UploadConflictChoice;
              if (batchOnConflict === "skip") {
                choice = "skip";
              } else {
                const displayName = e.conflicts[0] ?? file.webkitRelativePath ?? file.name;
                const answer = await askUploadConflict(displayName, fileTarget, queue.length);
                choice = answer.choice;
                if (answer.applyAll && choice !== "cancel") batchOnConflict = choice;
              }
              if (choice === "cancel") {
                break outer;
              }
              if (choice === "skip") {
                setUploadResult(resultId, { status: "skipped" });
                break;
              }
              attemptOnConflict = choice;
              continue;
            }
            // One bad file (too large, permission denied, …) is reported on
            // its row; the rest of the batch still goes.
            setUploadResult(resultId, { status: "failed", error: errorMessage(e) });
            break;
          }
        }
        // Skipped and failed files count as settled too, so the bar still
        // reaches the end.
        completedBytes += file.size || 0;
        doneCount += 1;
        if (run !== uploadRun.id || !uploadProgress.value) break;
        uploadProgress.value = {
          ...uploadProgress.value,
          loaded: completedBytes,
          doneCount,
        };
      }
      // Always reload — successful uploads should appear, and on cancel the
      // user wants to see what made it through. An abandoned run skips it.
      // The pipeline is released first so files dropped during the refresh
      // start a new run instead of joining a queue nobody drains any more.
      if (run === uploadRun.id) {
        releaseUpload();
        await reloadHost();
      }
    } catch (e) {
      if (run !== uploadRun.id) return;
      const h = uploadHost.value;
      if (h) h.error.value = errorMessage(e);
      // Still refresh so any files that landed before the failure show up.
      await reloadHost();
    } finally {
      if (run === uploadRun.id) releaseUpload();
    }
  }

  // Shared change handler for both hidden <input type="file"> elements
  // (plain multi-file and webkitdirectory). Clears the input afterwards so
  // re-picking the same file fires change again.
  async function handleUploadInput(event: Event) {
    const input = event.target as HTMLInputElement;
    const files = input.files;
    if (!files || files.length === 0) return;
    try {
      await performUpload(currentPath.value, Array.from(files));
    } finally {
      input.value = "";
    }
  }

  return {
    uploading,
    uploadProgress,
    uploadCancelled,
    uploadResults,
    dismissUploadResults,
    // Only the host panel renders the dialog, so two mounted panels never
    // stack two copies of it.
    conflictUpload: computed(() => (isHost.value ? conflictUpload.value : null)),
    settleUploadConflict,
    performUpload,
    cancelUpload,
    abortActiveUpload,
    handleUploadInput,
  };
}
