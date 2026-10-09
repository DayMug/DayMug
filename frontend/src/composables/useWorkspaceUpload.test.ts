import { describe, it, expect, vi } from "vitest";
import { effectScope, ref } from "vue";
import { flushPromises } from "@vue/test-utils";

import type { Router } from "vue-router";
import { installUploadLeaveGuard, useWorkspaceUpload } from "./useWorkspaceUpload";
import { UploadConflictError, type UploadOptions } from "./useFileApi";

const mockConfirm = vi.hoisted(() => vi.fn(async () => true));
vi.mock("@/composables/useConfirm", () => ({ useConfirm: () => ({ confirm: mockConfirm }) }));

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

  it("skipping a conflict keeps uploading the rest of the batch", async () => {
    const uploadFiles = vi
      .fn()
      .mockRejectedValueOnce(new UploadConflictError(["a.txt"]))
      .mockResolvedValue(undefined);
    const { upload } = setup(uploadFiles);
    const files = [new File(["a"], "a.txt"), new File(["b"], "b.txt")];

    const done = upload.performUpload(".", files);
    await flushPromises();
    expect(upload.conflictUpload.value?.remaining).toBe(1);
    upload.settleUploadConflict("skip");
    await done;

    expect(uploadFiles).toHaveBeenCalledTimes(2);
    expect(uploadFiles.mock.calls[1][2]).toEqual([files[1]]);
    expect((uploadFiles.mock.calls[1][3] as UploadOptions).onConflict).toBeUndefined();
  });

  it("asks about every conflicting file unless the user applies a choice to all", async () => {
    const conflict = (_u: string, _p: string, files: File[], options?: UploadOptions) =>
      options?.onConflict
        ? Promise.resolve()
        : Promise.reject(new UploadConflictError([files[0].name]));
    const uploadFiles = vi.fn(conflict);
    const { upload } = setup(uploadFiles);
    const files = ["a", "b", "c", "d"].map((n) => new File([n], `${n}.txt`));

    const done = upload.performUpload(".", files);
    await flushPromises();
    expect(upload.conflictUpload.value?.fileName).toBe("a.txt");
    upload.settleUploadConflict("overwrite");
    await flushPromises();
    // Not remembered: the next conflict gets its own question.
    expect(upload.conflictUpload.value?.fileName).toBe("b.txt");
    upload.settleUploadConflict("skip");
    await flushPromises();
    expect(upload.conflictUpload.value?.fileName).toBe("c.txt");
    upload.settleUploadConflict("rename", true);
    await done;

    const retried = uploadFiles.mock.calls
      .filter((c) => c[3]?.onConflict)
      .map((c) => [c[2][0].name, c[3]?.onConflict]);
    expect(retried).toEqual([
      ["a.txt", "overwrite"],
      ["c.txt", "rename"],
      ["d.txt", "rename"],
    ]);
  });

  it("skip applied to all skips later conflicts without asking", async () => {
    const uploadFiles = vi.fn((_u: string, _p: string, files: File[]) =>
      files[0].name === "ok.txt"
        ? Promise.resolve()
        : Promise.reject(new UploadConflictError([files[0].name])),
    );
    const { upload } = setup(uploadFiles);
    const files = ["a.txt", "b.txt", "ok.txt"].map((n) => new File(["x"], n));

    const done = upload.performUpload(".", files);
    await flushPromises();
    upload.settleUploadConflict("skip", true);
    await done;

    expect(upload.conflictUpload.value).toBeNull();
    expect(uploadFiles.mock.calls.map((c) => c[2][0].name)).toEqual(["a.txt", "b.txt", "ok.txt"]);
  });

  it("a failing file is reported and the rest of the batch still uploads", async () => {
    const uploadFiles = vi
      .fn()
      .mockRejectedValueOnce(new Error("upload: 500"))
      .mockResolvedValue(undefined);
    const { upload, error } = setup(uploadFiles);

    await upload.performUpload(".", [new File(["a"], "a.txt"), new File(["b"], "b.txt")]);

    expect(uploadFiles).toHaveBeenCalledTimes(2);
    expect(error.value).toBe("");
    expect(upload.uploadResults.value).toMatchObject([
      { name: "a.txt", status: "failed", error: "upload: 500" },
      { name: "b.txt", status: "done" },
    ]);
  });

  it("keeps the results after the run until dismissed; a new run starts a fresh list", async () => {
    const { upload } = setup();
    await upload.performUpload(".", [new File(["a"], "a.txt")]);
    expect(upload.uploadProgress.value).toBeNull();
    expect(upload.uploadResults.value).toHaveLength(1);

    await upload.performUpload(".", [new File(["b"], "b.txt")]);
    expect(upload.uploadResults.value.map((r) => r.name)).toEqual(["b.txt"]);

    upload.dismissUploadResults();
    expect(upload.uploadResults.value).toEqual([]);
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

  it("queues files added while an upload is in flight", async () => {
    // Re-entering must not start a second pipeline (Cancel would only reach
    // the newest one) nor reject the files: they join the running queue.
    const first = deferred<void>();
    const uploadFiles = vi.fn().mockReturnValueOnce(first.promise).mockResolvedValue(undefined);
    const { upload, error, loadDir } = setup(uploadFiles);
    const a = new File(["a"], "a.txt");
    const b = new File(["bb"], "b.txt");

    const run = upload.performUpload(".", [a]);
    await flushPromises();
    await upload.performUpload("sub", [b]);

    expect(uploadFiles).toHaveBeenCalledTimes(1);
    expect(error.value).toBe("");
    expect(upload.uploadProgress.value).toMatchObject({ total: 3, fileCount: 2 });

    first.resolve();
    await run;

    expect(uploadFiles).toHaveBeenCalledTimes(2);
    expect(uploadFiles).toHaveBeenNthCalledWith(2, "u1", "sub", [b], expect.anything());
    expect(loadDir).toHaveBeenCalledTimes(1);
    expect(upload.uploadProgress.value).toBeNull();
  });

  it("cancel drops files queued behind the in-flight one", async () => {
    const uploadFiles = neverSettlingUpload();
    const { upload } = setup(uploadFiles);

    const run = upload.performUpload(".", [new File(["a"], "a.txt")]);
    await flushPromises();
    await upload.performUpload(".", [new File(["b"], "b.txt")]);
    upload.cancelUpload();
    await run;

    expect(uploadFiles).toHaveBeenCalledTimes(1);
  });

  it("keeps uploading to the original agent after the panel switches agents", async () => {
    const first = deferred<void>();
    const uploadFiles = vi.fn().mockReturnValueOnce(first.promise).mockResolvedValue(undefined);
    const { upload, userId } = setup(uploadFiles);
    const a = new File(["a"], "a.txt");
    const b = new File(["b"], "b.txt");
    const c = new File(["c"], "c.txt");

    const done = upload.performUpload(".", [a, b]);
    await flushPromises();
    userId.value = "u2";
    // Dropped on the panel after the switch: goes to the new agent.
    await upload.performUpload(".", [c]);
    first.resolve();
    await done;

    expect(uploadFiles.mock.calls.map((call) => [call[0], call[2][0].name])).toEqual([
      ["u1", "a.txt"],
      ["u1", "b.txt"],
      ["u2", "c.txt"],
    ]);
  });

  it("a run survives the panel that started it and reports to the next one", async () => {
    const first = deferred<void>();
    const uploadFiles = vi
      .fn()
      .mockReturnValueOnce(first.promise)
      .mockRejectedValueOnce(new UploadConflictError(["b.txt"]))
      .mockResolvedValue(undefined);
    const scope = effectScope();
    const old = scope.run(() => setup(uploadFiles))!;
    const done = old.upload.performUpload(".", [
      new File(["a"], "a.txt"),
      new File(["b"], "b.txt"),
    ]);
    await flushPromises();
    // Mobile: leaving the Files tab unmounts the panel mid-upload.
    scope.stop();
    const next = setup(uploadFiles);
    expect(next.upload.uploadProgress.value).toMatchObject({ fileCount: 2 });

    first.resolve();
    await flushPromises();
    expect(next.upload.conflictUpload.value?.fileName).toBe("b.txt");
    next.upload.settleUploadConflict("overwrite");
    await done;

    expect(uploadFiles).toHaveBeenCalledTimes(3);
    expect(next.loadDir).toHaveBeenCalledWith(".");
    expect(old.loadDir).not.toHaveBeenCalled();
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

  describe("leave guard", () => {
    type Guard = (to: { name: string }) => Promise<boolean>;
    function installGuard(): Guard {
      let guard!: Guard;
      installUploadLeaveGuard({ beforeEach: (g: Guard) => (guard = g) } as unknown as Router);
      return guard;
    }

    it("lets every navigation through when nothing is uploading", async () => {
      const guard = installGuard();
      expect(await guard({ name: "settings-account" })).toBe(true);
      expect(mockConfirm).not.toHaveBeenCalled();
    });

    it("keeps uploading through conversation switches without asking", async () => {
      const guard = installGuard();
      const { upload } = setup(neverSettlingUpload());
      void upload.performUpload(".", [new File(["a"], "a.txt")]);
      await flushPromises();

      expect(await guard({ name: "chat" })).toBe(true);
      expect(mockConfirm).not.toHaveBeenCalled();
      expect(upload.uploadProgress.value).not.toBeNull();
    });

    it("asks before leaving the workspace pages and cancels on confirm", async () => {
      const guard = installGuard();
      const { upload } = setup(neverSettlingUpload());
      void upload.performUpload(".", [new File(["a"], "a.txt")]);
      await flushPromises();

      mockConfirm.mockResolvedValueOnce(false);
      expect(await guard({ name: "settings-account" })).toBe(false);
      expect(upload.uploadProgress.value).not.toBeNull();

      mockConfirm.mockResolvedValueOnce(true);
      expect(await guard({ name: "settings-account" })).toBe(true);
      expect(upload.uploadProgress.value).toBeNull();
    });
  });
});
