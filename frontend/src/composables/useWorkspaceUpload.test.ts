import { describe, it, expect, vi } from "vitest";
import { ref } from "vue";
import { flushPromises } from "@vue/test-utils";

import { useWorkspaceUpload } from "./useWorkspaceUpload";
import { UploadConflictError, type UploadOptions } from "./useFileApi";

function setup(uploadFiles = vi.fn().mockResolvedValue(undefined)) {
  const userId = ref("u1");
  const currentPath = ref(".");
  const error = ref("");
  const loadDir = vi.fn().mockResolvedValue(undefined);
  const upload = useWorkspaceUpload({ userId, currentPath, error, uploadFiles, loadDir });
  return { upload, uploadFiles, loadDir, error, currentPath, userId };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((res) => {
    resolve = res;
  });
  return { promise, resolve };
}

function neverSettlingUpload() {
  return vi.fn(
    (_userId: string, _path: string, _files: File[], options?: UploadOptions) =>
      new Promise<void>((_resolve, reject) => {
        options?.signal?.addEventListener("abort", () =>
          reject(new DOMException("aborted", "AbortError")),
        );
      }),
  );
}

describe("useWorkspaceUpload", () => {
  it("uploads files one at a time and reloads the listing afterwards", async () => {
    const { upload, uploadFiles, loadDir } = setup();
    const files = [new File(["a"], "a.txt"), new File(["bb"], "b.txt")];

    await upload.performUpload(".", files);

    expect(uploadFiles).toHaveBeenCalledTimes(2);
    expect(uploadFiles).toHaveBeenNthCalledWith(1, "u1", ".", [files[0]], expect.anything());
    expect(uploadFiles).toHaveBeenNthCalledWith(2, "u1", ".", [files[1]], expect.anything());
    expect(loadDir).toHaveBeenCalledWith(".");
    // Progress state is transient — cleared once the batch settles.
    expect(upload.uploadProgress.value).toBeNull();
  });

  it("pauses on a 409 conflict and retries with the user's choice", async () => {
    const uploadFiles = vi
      .fn()
      .mockRejectedValueOnce(new UploadConflictError(["a.txt"]))
      .mockResolvedValue(undefined);
    const { upload } = setup(uploadFiles);

    const done = upload.performUpload(".", [new File(["a"], "a.txt")]);
    await flushPromises();
    expect(upload.conflictUpload.value?.fileName).toBe("a.txt");

    upload.settleUploadConflict("rename");
    await done;

    const retryOpts = uploadFiles.mock.calls[1][3] as UploadOptions;
    expect(retryOpts.onConflict).toBe("rename");
  });

  it("cancelling the conflict dialog stops the rest of the batch", async () => {
    const uploadFiles = vi
      .fn()
      .mockRejectedValueOnce(new UploadConflictError(["a.txt"]))
      .mockResolvedValue(undefined);
    const { upload, loadDir } = setup(uploadFiles);

    const done = upload.performUpload(".", [new File(["a"], "a.txt"), new File(["b"], "b.txt")]);
    await flushPromises();
    upload.settleUploadConflict("cancel");
    await done;

    // Only the first (conflicting) attempt was issued; the queued file never started.
    expect(uploadFiles).toHaveBeenCalledTimes(1);
    // Listing still refreshes so completed files would show up.
    expect(loadDir).toHaveBeenCalledWith(".");
  });

  it("cancelUpload aborts the in-flight request and skips queued files", async () => {
    const uploadFiles = vi.fn(
      (_userId: string, _path: string, _files: File[], options?: UploadOptions) =>
        new Promise<void>((_resolve, reject) => {
          options?.signal?.addEventListener("abort", () =>
            reject(new DOMException("aborted", "AbortError")),
          );
        }),
    );
    const { upload } = setup(uploadFiles);

    const done = upload.performUpload(".", [new File(["a"], "a.txt"), new File(["b"], "b.txt")]);
    await flushPromises();
    upload.cancelUpload();
    expect(upload.uploadCancelled.value).toBe(true);
    await done;

    expect(uploadFiles).toHaveBeenCalledTimes(1);
    expect(upload.uploadProgress.value).toBeNull();
    expect(upload.uploadCancelled.value).toBe(false);
  });

  it("rejects folder uploads exceeding the per-folder file limit", async () => {
    const { upload, uploadFiles, error } = setup();
    // Drag-drop traversal encodes the folder path into the synthetic name.
    const files = Array.from({ length: 11 }, (_, i) => new File(["x"], `dir/f${i}.txt`));

    await upload.performUpload(".", files);

    expect(uploadFiles).not.toHaveBeenCalled();
    expect(error.value).toContain('Folder "dir"');
  });

  it("handleUploadInput uploads picked files and clears the input", async () => {
    const { upload, uploadFiles } = setup();
    const file = new File(["a"], "a.txt");
    const input = { files: [file], value: "C:\\fakepath\\a.txt" };

    await upload.handleUploadInput({ target: input } as unknown as Event);

    expect(uploadFiles).toHaveBeenCalledWith("u1", ".", [file], expect.anything());
    expect(input.value).toBe("");
  });

  it("refuses a second batch while one is still in flight", async () => {
    // Re-entering used to overwrite uploadAbortController (leaving Cancel
    // wired to the newest batch only) and let the first batch to finish wipe
    // the progress state both batches shared.
    const uploadFiles = neverSettlingUpload();
    const { upload, error } = setup(uploadFiles);

    const first = upload.performUpload(".", [new File(["a"], "a.txt")]);
    await flushPromises();
    await upload.performUpload(".", [new File(["b"], "b.txt")]);

    expect(uploadFiles).toHaveBeenCalledTimes(1);
    expect(error.value).toContain("already running");

    upload.cancelUpload();
    await first;
  });

  it("stops uploading the rest of the batch when the panel switches agents", async () => {
    const uploadFiles = neverSettlingUpload();
    const { upload, userId } = setup(uploadFiles);

    const done = upload.performUpload(".", [new File(["a"], "a.txt"), new File(["b"], "b.txt")]);
    await flushPromises();
    userId.value = "u2";
    upload.abortActiveUpload();
    await done;

    expect(uploadFiles).toHaveBeenCalledTimes(1);
    expect(uploadFiles.mock.calls[0][0]).toBe("u1");
  });

  it("an orphaned batch cannot clear the progress of the batch that replaced it", async () => {
    const orphaned = deferred<void>();
    const uploadFiles = vi
      .fn()
      .mockReturnValueOnce(orphaned.promise)
      .mockReturnValueOnce(new Promise<void>(() => {}));
    const { upload } = setup(uploadFiles);

    const first = upload.performUpload(".", [new File(["a"], "a.txt")]);
    await flushPromises();
    upload.abortActiveUpload();

    void upload.performUpload(".", [new File(["b"], "b.txt")]);
    await flushPromises();
    // The first batch's request only settles now — after the replacement took
    // ownership of the shared progress state.
    orphaned.resolve();
    await first;

    expect(upload.uploadProgress.value).not.toBeNull();
  });
});
