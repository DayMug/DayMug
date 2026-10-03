import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../useBrowserNotifications", () => ({ notifyBrowserTask: vi.fn() }));
import { resetChatStores } from "@/stores";
import { isAnsweringUserQuestion, pendingUserQuestion } from "@/stores/chatStreamStore";
import { currentConversationId } from "@/stores/activeConversationStore";
import { notifyBrowserTask } from "../useBrowserNotifications";
import { createMessageDispatcher } from "./messageDispatcher";

const notify = vi.mocked(notifyBrowserTask);

describe("messageDispatcher user questions", () => {
  beforeEach(() => {
    resetChatStores();
    notify.mockClear();
  });

  it("keeps the current question until the matching resolution arrives", () => {
    currentConversationId.value = "c1";
    const dispatch = createMessageDispatcher();
    dispatch({
      type: "user_question",
      content: JSON.stringify({
        request_id: "ask-1",
        questions: [{ id: "q1", header: "Choice", question: "Continue?" }],
      }),
    });
    expect(pendingUserQuestion.value?.request_id).toBe("ask-1");
    expect(notify).toHaveBeenCalledWith({
      kind: "waiting",
      conversationId: "c1",
      eventId: "ask-1",
    });

    isAnsweringUserQuestion.value = true;
    dispatch({ type: "user_question_resolved", request_id: "ask-other" });
    expect(pendingUserQuestion.value?.request_id).toBe("ask-1");

    dispatch({ type: "user_question_resolved", request_id: "ask-1" });
    expect(pendingUserQuestion.value).toBeNull();
    expect(isAnsweringUserQuestion.value).toBe(false);
  });

  it.each([
    ["not JSON", "{oops"],
    ["no request id", JSON.stringify({ questions: [{ id: "q1" }] })],
    ["no questions", JSON.stringify({ request_id: "ask-1", questions: [] })],
    ["non-object question", JSON.stringify({ request_id: "ask-1", questions: ["Continue?"] })],
  ])("drops and reports a malformed request (%s)", (_name, content) => {
    const errorSpy = vi.spyOn(console, "error").mockImplementation(() => {});
    const dispatch = createMessageDispatcher();
    dispatch({ type: "user_question", content });

    expect(pendingUserQuestion.value).toBeNull();
    expect(notify).not.toHaveBeenCalled();
    expect(errorSpy).toHaveBeenCalledOnce();
    errorSpy.mockRestore();
  });
});
