import { ref, type Ref } from "vue";
import { UploadConflictError, type UploadOptions } from "@/composables/useFileApi";
import { checkFolderFileLimit } from "@/lib/uploadHelpers";
import { i18n } from "@/i18n";
import { errorMessage } from "@/lib/errorMessage";

export type UploadConflictChoice = "cancel" | "overwrite" | "rename";

export interface UploadProgressState {
  loaded: number;
  total: number;
  speed: number;
  fileCount: number;
  doneCount: number;
}

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

// useWorkspaceUpload owns the workspace panel's upload pipeline: progress /
// speed tracking for the bottom progress bar, the per-file AbortController
// behind the Cancel button, and the 409-conflict prompt loop. Instance-scoped
// (call inside setup()) — each mounted panel gets its own upload state.
export function useWorkspaceUpload({
  userId,
  currentPath,
  error,
  uploadFiles,
  loadDir,
}: UseWorkspaceUploadOptions) {
  // Visible upload progress state. While an upload is in flight, a thin
  // green bar appears at the bottom of the panel showing total/loaded
  // bytes and the rolling-average upload speed in the top-right corner.
  // `null` means no upload is currently active.
  const uploadProgress = ref<UploadProgressState | null>(null);

  // Holds the AbortController for the in-flight per-file upload so the
  // Cancel button can interrupt the current request. We upload files one
  // at a time; aborting mid-file means the server's multipart parser fails
  // before SaveUploadedFile is called, so the partial file never lands on
  // disk. Already-completed files in the same batch stay.
  const uploadAbortController = ref<AbortController | null>(null);
  const uploadCancelled = ref(false);

  // Every mutable piece of upload state above is shared by the whole panel, so
  // only one batch may own it at a time. `uploadRun` identifies that owner: a
  // batch that has been orphaned (agent switch) must not clear the progress
  // bar or abort controller a newer batch is using.
  let uploadRun = 0;
  const uploading = ref(false);

  // Upload-conflict prompt: the upload loop pauses while this ref is set
  // and resumes once the user picks an action via the dialog (which fires
  // the resolve callback). One ref serves the whole batch; if the user
  // picks rename/overwrite mid-batch we remember the choice so subsequent
  // conflicts in the same `performUpload` invocation skip the dialog.
  const conflictUpload = ref<{
    fileName: string;
    targetFolder: string;
    resolve: (choice: UploadConflictChoice) => void;
  } | null>(null);

  function askUploadConflict(
    fileName: string,
    targetFolder: string,
  ): Promise<UploadConflictChoice> {
    return new Promise((resolve) => {
      conflictUpload.value = { fileName, targetFolder, resolve };
    });
  }

  function settleUploadConflict(choice: UploadConflictChoice) {
    const c = conflictUpload.value;
    if (!c) return;
    conflictUpload.value = null;
    c.resolve(choice);
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
  // queued files never start. Total/loaded counters span the whole batch.
  async function performUpload(targetPath: string, files: File[]): Promise<void> {
    if (files.length === 0) return;
    // Re-entering while a batch is live used to overwrite uploadAbortController
    // (so Cancel only reached the newest batch) and let whichever batch
    // finished first wipe the shared progress state out from under the other.
    if (uploading.value) {
      error.value = i18n.global.t("workspace.uploadBusy");
      return;
    }
    const limitMsg = checkFolderFileLimit(files);
    if (limitMsg) {
      error.value = limitMsg;
      return;
    }
    const run = ++uploadRun;
    // Pin the batch to the agent it started against: `userId` is a live ref
    // and reading it per file would send the tail of the batch into whatever
    // workspace the panel switched to mid-upload.
    const batchUserId = userId.value;
    uploading.value = true;
    const total = files.reduce((sum, f) => sum + (f.size || 0), 0);
    const startTime = Date.now();
    let completedBytes = 0;
    let doneCount = 0;
    uploadProgress.value = {
      loaded: 0,
      total,
      speed: 0,
      fileCount: files.length,
      doneCount: 0,
    };
    uploadCancelled.value = false;
    const controller = new AbortController();
    uploadAbortController.value = controller;
    // Once the user picks rename/overwrite for one conflict, apply the same
    // choice to every subsequent conflict in the same batch — folder uploads
    // routinely have dozens of files and we don't want to drop a modal on
    // each one.
    let batchOnConflict: "overwrite" | "rename" | null = null;
    try {
      outer: for (const file of files) {
        if (controller.signal.aborted) break;
        if (run !== uploadRun || userId.value !== batchUserId) break;
        let attemptOnConflict: "overwrite" | "rename" | null = batchOnConflict;
        // Retry loop: on a 409 we ask the user, then re-issue the same
        // request with their chosen on_conflict mode. Cancel breaks the
        // whole batch.
        while (true) {
          try {
            await uploadFiles(batchUserId, targetPath, [file], {
              signal: controller.signal,
              onConflict: attemptOnConflict ?? undefined,
              onProgress: ({ loaded }) => {
                if (run !== uploadRun) return;
                const elapsedSec = Math.max((Date.now() - startTime) / 1000, 0.001);
                const overall = completedBytes + loaded;
                uploadProgress.value = {
                  loaded: overall,
                  total,
                  speed: overall / elapsedSec,
                  fileCount: files.length,
                  doneCount,
                };
              },
            });
            break;
          } catch (e) {
            if (e instanceof DOMException && e.name === "AbortError") {
              break outer;
            }
            if (e instanceof UploadConflictError) {
              const displayName = e.conflicts[0] ?? file.webkitRelativePath ?? file.name;
              const choice = await askUploadConflict(displayName, targetPath);
              if (choice === "cancel") {
                break outer;
              }
              attemptOnConflict = choice;
              batchOnConflict = choice;
              continue;
            }
            throw e;
          }
        }
        completedBytes += file.size || 0;
        doneCount += 1;
        if (run !== uploadRun) break;
        uploadProgress.value = {
          loaded: completedBytes,
          total,
          speed: uploadProgress.value?.speed ?? 0,
          fileCount: files.length,
          doneCount,
        };
      }
      // Always reload — successful uploads should appear, and on cancel the
      // user wants to see what made it through. An orphaned batch skips it:
      // the panel has already moved on to a different workspace.
      if (run === uploadRun) await loadDir(currentPath.value);
    } catch (e) {
      if (run !== uploadRun) return;
      error.value = errorMessage(e);
      // Still refresh so any files that landed before the failure show up.
      try {
        await loadDir(currentPath.value);
      } catch {
        // listing errors are surfaced by the composable already
      }
    } finally {
      if (run === uploadRun) {
        uploadProgress.value = null;
        uploadAbortController.value = null;
        uploadCancelled.value = false;
        uploading.value = false;
      }
    }
  }

  function cancelUpload() {
    if (!uploadAbortController.value) return;
    uploadCancelled.value = true;
    uploadAbortController.value.abort();
  }

  // Called when the panel is rebound to a different agent. The in-flight batch
  // is aborted and orphaned (its remaining files would otherwise land in the
  // new agent's workspace), and the shared state is released immediately so a
  // fresh upload can start without waiting for the old batch to unwind.
  function abortActiveUpload() {
    uploadRun++;
    // A batch parked on the conflict dialog would never resume otherwise.
    settleUploadConflict("cancel");
    uploadAbortController.value?.abort();
    uploadAbortController.value = null;
    uploadProgress.value = null;
    uploadCancelled.value = false;
    uploading.value = false;
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
    uploadProgress,
    uploadCancelled,
    conflictUpload,
    settleUploadConflict,
    performUpload,
    cancelUpload,
    abortActiveUpload,
    handleUploadInput,
  };
}
