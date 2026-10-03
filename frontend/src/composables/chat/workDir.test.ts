import { beforeEach, describe, expect, it, vi } from "vitest";

const mockUpdateConversationWorkDir = vi.fn();
vi.mock("../useApi", () => ({
  updateConversationWorkDir: (...args: unknown[]) => mockUpdateConversationWorkDir(...args),
}));

import {
  conversationWorkDir,
  currentConversationId,
  isWorkDirLocked,
} from "@/stores/activeConversationStore";
import { chatMessages } from "@/stores/chatMessageStore";

import { changeWorkDir, WORK_DIR_HINT_PREFIX, workDirHint } from "./workDir";

function seedConversation(id: string, workDir: string) {
  currentConversationId.value = id;
  isWorkDirLocked.value = false;
  conversationWorkDir.value = workDir;
  chatMessages.value = [
    { role: "activity", activityType: "info", content: workDirHint(workDir, false) },
  ];
}

beforeEach(() => {
  vi.clearAllMocks();
  chatMessages.value = [];
});

describe("workDirHint", () => {
  it("keeps the call-to-action while the work dir is still switchable", () => {
    const hint = workDirHint("/srv/work", false);

    expect(hint.startsWith(`${WORK_DIR_HINT_PREFIX}/srv/work.`)).toBe(true);
    expect(hint).toContain("workspace panel");
  });

  it("shrinks to the bare path once the conversation has locked it in", () => {
    expect(workDirHint("/srv/work", true)).toBe(`${WORK_DIR_HINT_PREFIX}/srv/work.`);
  });
});

describe("changeWorkDir", () => {
  it("applies the server's path when the conversation hasn't changed", async () => {
    seedConversation("c1", "/old");
    mockUpdateConversationWorkDir.mockResolvedValueOnce({ work_dir: "/new" });

    await changeWorkDir("/new");

    expect(mockUpdateConversationWorkDir).toHaveBeenCalledWith("c1", "/new");
    expect(conversationWorkDir.value).toBe("/new");
    expect(chatMessages.value[0].content).toContain("/new");
  });

  it("discards a response that lands after the user switched conversations", async () => {
    seedConversation("c1", "/old");
    let resolveUpdate: (v: unknown) => void = () => {};
    mockUpdateConversationWorkDir.mockImplementationOnce(
      () => new Promise((resolve) => (resolveUpdate = resolve)),
    );

    const pending = changeWorkDir("/c1-dir");
    // The user opens another conversation; the store now describes c2.
    currentConversationId.value = "c2";
    conversationWorkDir.value = "/c2-dir";
    chatMessages.value = [
      { role: "activity", activityType: "info", content: workDirHint("/c2-dir", false) },
    ];
    resolveUpdate({ work_dir: "/c1-dir" });
    await pending;

    expect(conversationWorkDir.value).toBe("/c2-dir");
    expect(chatMessages.value[0].content).toContain("/c2-dir");
  });
});
