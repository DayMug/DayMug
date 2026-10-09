import { describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import AskUserQuestionCard from "./AskUserQuestionCard.vue";

const secondQuestion = {
  id: "region",
  header: "Region",
  question: "Which region should be used?",
  options: [{ label: "US East", description: "Virginia" }],
};

function mountCard() {
  return mount(AskUserQuestionCard, {
    props: {
      request: {
        request_id: "ask-1",
        questions: [
          {
            id: "environment",
            header: "Environment",
            question: "Where should this deploy?",
            options: [
              { label: "Staging", description: "Safer" },
              { label: "Production", description: "Live traffic" },
            ],
            allow_other: true,
          },
        ],
      },
      submitting: false,
      isConnected: true,
    },
  });
}

async function mountTwoQuestionCard() {
  const wrapper = mountCard();
  await wrapper.setProps({
    request: {
      request_id: "ask-2",
      questions: [...wrapper.props("request").questions, secondQuestion],
    },
  });
  return wrapper;
}

function swipe(wrapper: ReturnType<typeof mountCard>, deltaX: number, deltaY = 0) {
  const body = wrapper.get('[data-testid="question-step-body"]');
  return body.trigger("touchstart", { touches: [{ clientX: 200, clientY: 200 }] }).then(() =>
    body.trigger("touchend", {
      changedTouches: [{ clientX: 200 + deltaX, clientY: 200 + deltaY }],
    }),
  );
}

describe("AskUserQuestionCard", () => {
  it("keeps long mobile questions scrollable with reachable actions", () => {
    const wrapper = mountCard();

    expect(wrapper.get('[data-testid="ask-user-question"]').classes()).toEqual(
      expect.arrayContaining(["flex", "min-h-0", "flex-col", "overflow-hidden"]),
    );
    expect(wrapper.get('[data-testid="question-form"]').classes()).toEqual(
      expect.arrayContaining(["min-h-0", "overflow-y-auto", "touch-pan-y"]),
    );
    expect(wrapper.get('[data-testid="question-actions"]').classes()).toEqual(
      expect.arrayContaining(["sticky", "bottom-0", "z-10"]),
    );
  });

  it("requires an answer and emits the selected option", async () => {
    const wrapper = mountCard();
    const submit = wrapper.get('button[type="submit"]');
    expect(submit.attributes("disabled")).toBeDefined();

    await wrapper.findAll('[data-testid="question-option"]')[0].trigger("click");
    expect(submit.attributes("disabled")).toBeUndefined();
    await wrapper.get("form").trigger("submit");

    expect(wrapper.emitted("answer")?.[0]).toEqual(["ask-1", { environment: ["Staging"] }]);
  });

  it("clears a single selection when the selected option is clicked again", async () => {
    const wrapper = mountCard();
    const option = wrapper.findAll('[data-testid="question-option"]')[0];
    const submit = wrapper.get('button[type="submit"]');

    await option.trigger("click");
    expect(option.attributes("aria-pressed")).toBe("true");
    expect(submit.attributes("disabled")).toBeUndefined();

    await option.trigger("click");
    expect(option.attributes("aria-pressed")).toBe("false");
    expect(submit.attributes("disabled")).toBeDefined();
  });

  it("lets a free-form answer replace a single selection", async () => {
    const wrapper = mountCard();
    await wrapper.findAll('[data-testid="question-option"]')[0].trigger("click");
    await wrapper.get("input").setValue("Preview");
    await wrapper.get("form").trigger("submit");
    expect(wrapper.emitted("answer")?.[0]).toEqual(["ask-1", { environment: ["Preview"] }]);
  });

  it("shows multiple questions one at a time and submits them after the last step", async () => {
    const wrapper = await mountTwoQuestionCard();

    expect(wrapper.text()).toContain("Where should this deploy?");
    expect(wrapper.text()).not.toContain("Which region should be used?");

    await wrapper.findAll('[data-testid="question-option"]')[0].trigger("click");
    await wrapper.get("form").trigger("submit");

    expect(wrapper.emitted("answer")).toBeUndefined();
    expect(wrapper.text()).not.toContain("Where should this deploy?");
    expect(wrapper.text()).toContain("Which region should be used?");

    await wrapper.get('[data-testid="previous-question"]').trigger("click");
    expect(wrapper.get('button[aria-pressed="true"]').text()).toContain("Staging");
    await wrapper.get("form").trigger("submit");

    await wrapper.get('[data-testid="question-option"]').trigger("click");
    await wrapper.get("form").trigger("submit");

    expect(wrapper.emitted("answer")?.[0]).toEqual([
      "ask-2",
      { environment: ["Staging"], region: ["US East"] },
    ]);
  });

  it("advances past an unanswered question and blocks only the final submit", async () => {
    const wrapper = await mountTwoQuestionCard();

    await wrapper.get("form").trigger("submit");
    expect(wrapper.text()).toContain("Which region should be used?");
    expect(wrapper.get('button[type="submit"]').attributes("disabled")).toBeDefined();
    expect(wrapper.get('[data-testid="question-unanswered"]').text()).toContain("2");

    await wrapper.get('[data-testid="question-option"]').trigger("click");
    expect(wrapper.get('[data-testid="question-unanswered"]').text()).toContain("1");
    expect(wrapper.get('button[type="submit"]').attributes("disabled")).toBeDefined();
  });

  it("swipes between questions in both directions", async () => {
    const wrapper = await mountTwoQuestionCard();

    await swipe(wrapper, -80);
    expect(wrapper.text()).toContain("Which region should be used?");

    await swipe(wrapper, 80);
    expect(wrapper.text()).toContain("Where should this deploy?");
  });

  it("ignores a gesture that is mostly vertical scrolling", async () => {
    const wrapper = await mountTwoQuestionCard();

    await swipe(wrapper, -60, -140);
    expect(wrapper.text()).toContain("Where should this deploy?");
  });

  it("jumps to a question from the progress bar", async () => {
    const wrapper = await mountTwoQuestionCard();

    await wrapper.findAll('[data-testid="question-step"]')[1].trigger("click");
    expect(wrapper.text()).toContain("Which region should be used?");

    await wrapper.findAll('[data-testid="question-step"]')[0].trigger("click");
    expect(wrapper.text()).toContain("Where should this deploy?");
  });
});
