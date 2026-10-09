import { describe, expect, it } from "vitest";

import { WORK_DIR_HINT_PREFIX, workDirHint } from "./workDir";

describe("workDirHint", () => {
  it("names the fixed working directory without offering to switch it", () => {
    expect(workDirHint("/srv/work")).toBe(`${WORK_DIR_HINT_PREFIX}/srv/work.`);
  });
});
