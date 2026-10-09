import { describe, expect, it } from "vitest";
import { ref } from "vue";

import { useAsyncOperation } from "./useAsyncOperation";

describe("useAsyncOperation", () => {
  it("returns the callback's resolved value", async () => {
    const op = useAsyncOperation();
    const res = await op.run(async () => 42);
    expect(res).toBe(42);
  });

  it("sets busy while the operation runs and clears it after", async () => {
    const op = useAsyncOperation();
    let busyDuring = false;
    await op.run(async () => {
      busyDuring = op.busy.value;
    });
    expect(busyDuring).toBe(true);
    expect(op.busy.value).toBe(false);
  });

  it("captures an Error's message and resolves undefined", async () => {
    const op = useAsyncOperation();
    const res = await op.run(async () => {
      throw new Error("boom");
    });
    expect(res).toBeUndefined();
    expect(op.error.value).toBe("boom");
  });

  it("stringifies non-Error throws", async () => {
    const op = useAsyncOperation();
    await op.run(async () => {
      throw "raw failure";
    });
    expect(op.error.value).toBe("raw failure");
  });

  it("uses errorFallback for non-Error throws only", async () => {
    const op = useAsyncOperation();
    await op.run(
      async () => {
        throw "raw failure";
      },
      { errorFallback: "localized" },
    );
    expect(op.error.value).toBe("localized");
    await op.run(
      async () => {
        throw new Error("boom");
      },
      { errorFallback: "localized" },
    );
    expect(op.error.value).toBe("boom");
  });

  it("clears a previous error when a new run starts", async () => {
    const op = useAsyncOperation();
    await op.run(async () => {
      throw new Error("old");
    });
    await op.run(async () => "ok");
    expect(op.error.value).toBe("");
  });

  it("clears a previous message when a new run starts", async () => {
    const op = useAsyncOperation();
    op.message.value = "saved";
    await op.run(async () => "ok");
    expect(op.message.value).toBe("");
  });

  it("keeps a message set inside the callback after the run finishes", async () => {
    const op = useAsyncOperation();
    await op.run(async () => {
      op.message.value = "saved";
    });
    expect(op.message.value).toBe("saved");
  });

  it("clears busy even when the operation throws", async () => {
    const op = useAsyncOperation();
    await op.run(async () => {
      throw new Error("boom");
    });
    expect(op.busy.value).toBe(false);
  });

  it("toggles a caller-supplied busy ref instead of its own", async () => {
    const op = useAsyncOperation();
    const external = ref(false);
    let externalDuring = false;
    let ownDuring = true;
    await op.run(
      async () => {
        externalDuring = external.value;
        ownDuring = op.busy.value;
      },
      { busy: external },
    );
    expect(externalDuring).toBe(true);
    expect(ownDuring).toBe(false);
    expect(external.value).toBe(false);
  });

  it("skips busy tracking entirely when busy is false", async () => {
    const op = useAsyncOperation();
    let busyDuring = true;
    await op.run(
      async () => {
        busyDuring = op.busy.value;
      },
      { busy: false },
    );
    expect(busyDuring).toBe(false);
  });

  it("reset clears error and message without touching busy", async () => {
    const op = useAsyncOperation();
    op.error.value = "oops";
    op.message.value = "done";
    op.busy.value = true;
    op.reset();
    expect(op.error.value).toBe("");
    expect(op.message.value).toBe("");
    expect(op.busy.value).toBe(true);
  });
});
