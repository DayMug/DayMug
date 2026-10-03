import { describe, it, expect, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import ChatMessageItem from "./ChatMessageItem.vue";

vi.mock("@/composables/useMarkdown", () => ({
  renderMarkdown: (s: string) =>
    s.startsWith("```")
      ? `<pre class="hljs"><code>${s.slice(4, -3)}</code></pre>`
      : `<p>${s.replace(/\*\*(.+?)\*\*/g, "<strong>$1</strong>")}</p>`,
}));

describe("ChatMessageItem", () => {
  it("renders user message", () => {
    const wrapper = mount(ChatMessageItem, {
      props: { msg: { role: "user", content: "hello" }, collapsed: false },
    });
    expect(wrapper.find(".msg-label").text()).toBe("You");
    expect(wrapper.find(".msg-content").text()).toBe("hello");
  });

  it("renders user message markdown instead of displaying its source", () => {
    const wrapper = mount(ChatMessageItem, {
      props: { msg: { role: "user", content: "**important**" }, collapsed: false },
    });

    const markdown = wrapper.get(".user-message-markdown");
    expect(markdown.classes()).toContain("markdown-body");
    expect(markdown.get("strong").text()).toBe("important");
    expect(markdown.text()).not.toContain("**");
  });

  it("renders persisted IM image attachments inside the user bubble", () => {
    const wrapper = mount(ChatMessageItem, {
      props: {
        msg: {
          role: "user",
          content: "[Alice] 看这张图",
          attachments: [
            {
              name: "photo.png",
              mime: "image/png",
              path: ".daymug/agents/agent-1/uploads/photo.png",
              url: "/api/users/agent/files/read?path=photo.png",
            },
          ],
        },
        collapsed: false,
      },
    });

    const image = wrapper.get('[data-testid="message-attachment-image"]');
    expect(image.attributes("src")).toBe("/api/users/agent/files/read?path=photo.png");
    expect(image.attributes("alt")).toBe("photo.png");
  });

  it("shrinks the user bubble image frame to the image so both corners stay rounded", () => {
    const wrapper = mount(ChatMessageItem, {
      props: {
        msg: {
          role: "user",
          content: "",
          attachments: [
            {
              name: "tall.png",
              mime: "image/png",
              path: ".daymug/agents/agent-1/uploads/tall.png",
              url: "/api/users/agent/files/read?path=tall.png",
            },
          ],
        },
        collapsed: false,
      },
    });

    // A block frame spans the whole bubble, so a max-height-limited image sits
    // left-aligned inside it and only picks up the left rounded corners.
    const frame = wrapper.get('[data-testid="message-attachment-image"]').element
      .parentElement as HTMLElement;
    expect(frame.className).toContain("w-fit");
    expect(frame.className).toContain("max-w-full");
  });

  it("renders a persisted IM file attachment as a download link", () => {
    const wrapper = mount(ChatMessageItem, {
      props: {
        msg: {
          role: "user",
          content: "请处理附件",
          attachments: [
            {
              name: "brief.pdf",
              mime: "application/pdf",
              path: ".daymug/agents/agent-1/uploads/brief.pdf",
              url: "/api/users/agent/files/read?path=brief.pdf",
            },
          ],
        },
        collapsed: false,
      },
    });

    expect(wrapper.get('[data-testid="message-attachment-file"]').text()).toContain("brief.pdf");
    expect(wrapper.find('[data-testid="message-attachment-image"]').exists()).toBe(false);
  });

  it("renders an agent-generated image below the assistant reply", () => {
    const wrapper = mount(ChatMessageItem, {
      props: {
        msg: {
          role: "assistant",
          content: "图片已生成",
          attachments: [
            {
              name: "chart.png",
              mime: "image/png",
              path: "./chart.png",
              url: "/api/users/agent/files/read?path=chart.png",
            },
          ],
        },
        collapsed: false,
      },
    });

    const image = wrapper.get('[data-testid="assistant-attachment-image"]');
    expect(image.attributes("src")).toBe("/api/users/agent/files/read?path=chart.png");
    expect(image.attributes("alt")).toBe("chart.png");
  });

  it("renders an agent-generated ordinary file as a download chip", () => {
    const wrapper = mount(ChatMessageItem, {
      props: {
        msg: {
          role: "assistant",
          content: "报告已生成",
          attachments: [
            {
              name: "report.pdf",
              mime: "application/pdf",
              path: "./report.pdf",
              url: "/api/users/agent/files/download?path=report.pdf",
            },
          ],
        },
        collapsed: false,
      },
    });

    const chip = wrapper.get('[data-testid="assistant-attachment-file"]');
    expect(chip.text()).toContain("report.pdf");
    expect(wrapper.find('[data-testid="assistant-attachment-image"]').exists()).toBe(false);
    expect(wrapper.get('[data-testid="assistant-image-attachments"] a').attributes("href")).toBe(
      "/api/users/agent/files/download?path=report.pdf",
    );
  });

  it("uses the supplied userLabel for user messages so the chat can show the human's name", () => {
    // ChatPage feeds in the signed-in human's display name as userLabel
    // so the transcript reads as a conversation between *that* person
    // and an agent, rather than the generic "You / Claude" framing.
    const wrapper = mount(ChatMessageItem, {
      props: {
        msg: { role: "user", content: "hello" },
        collapsed: false,
        userLabel: "Alice",
        assistantLabel: "Researcher",
      },
    });
    expect(wrapper.find(".msg-label").text()).toBe("Alice");
  });

  it("uses the mirrored IM sender instead of the shared web-user label", () => {
    const wrapper = mount(ChatMessageItem, {
      props: {
        msg: {
          role: "user",
          content: "deploy now",
          sender: { platform: "slack", id: "U_BOB", name: "Bob" },
        },
        collapsed: false,
        userLabel: "You",
      },
    });

    expect(wrapper.find(".msg-label").text()).toBe("Bob");
  });

  it("uses the supplied assistantLabel for assistant messages so the agent's name appears in place of 'Claude'", () => {
    const wrapper = mount(ChatMessageItem, {
      props: {
        msg: { role: "assistant", content: "hi" },
        collapsed: false,
        userLabel: "Alice",
        assistantLabel: "Researcher",
      },
    });
    expect(wrapper.find(".msg-label").text()).toBe("Researcher");
  });

  it("user bubble carries the .user-bubble class so ::selection styling can target it", () => {
    // The class remains the stable hook for selection styling after the
    // workspace redesign moved prompts onto a quiet paper surface.
    const wrapper = mount(ChatMessageItem, {
      props: { msg: { role: "user", content: "hello" }, collapsed: false },
    });
    expect(wrapper.find(".msg-content").classes()).toContain("user-bubble");
    expect(wrapper.find(".msg-content").classes()).toContain("bg-[#efefef]");
  });

  it("fully expands regular user bubbles while capping width and wrapping long text", () => {
    const wrapper = mount(ChatMessageItem, {
      props: {
        msg: {
          role: "user",
          content: "https://example.test/" + "a".repeat(800),
        },
        collapsed: false,
      },
    });
    const bubble = wrapper.find(".msg-content");
    expect(bubble.element.parentElement?.classList).toContain("max-w-[620px]");
    expect(bubble.classes()).not.toContain("max-h-[min(42vh,28rem)]");
    expect(bubble.classes()).not.toContain("overflow-y-auto");
    expect(bubble.classes()).toContain("[overflow-wrap:anywhere]");
  });

  it("badges an assistant turn the agent started on its own", () => {
    // A conversation that moves while nobody is typing has to read as
    // deliberate. Without the badge a background wakeup is indistinguishable
    // from the agent answering a question the user never asked.
    const wrapper = mount(ChatMessageItem, {
      props: {
        msg: { role: "assistant", content: "the codex leg finished", wakeup: true },
        collapsed: false,
      },
    });
    expect(wrapper.find('[data-testid="assistant-wakeup-badge"]').exists()).toBe(true);
  });

  it("leaves an ordinary assistant reply unbadged", () => {
    const wrapper = mount(ChatMessageItem, {
      props: { msg: { role: "assistant", content: "sure" }, collapsed: false },
    });
    expect(wrapper.find('[data-testid="assistant-wakeup-badge"]').exists()).toBe(false);
  });

  it("renders a default (non-cancelled) user bubble without the cancelled marker", () => {
    // Negative control for the cancelled variant: a plain user message
    // must NOT carry the cancelled bubble class or the Cancelled badge,
    // so the special style is only ever applied when explicitly opted in.
    const wrapper = mount(ChatMessageItem, {
      props: { msg: { role: "user", content: "hello" }, collapsed: false },
    });
    expect(wrapper.find(".msg-content").classes()).not.toContain("user-bubble-cancelled");
    expect(wrapper.find(".msg-cancelled-badge").exists()).toBe(false);
  });

  it("renders the cancelled-variant user bubble when msg.cancelled is true", () => {
    // The cancelled flag swaps the ink-black bubble for a muted paper-alt
    // variant with a dashed border + a small "Cancelled" badge near the
    // username — visually obvious the turn didn't complete normally,
    // without dropping the user's words from the transcript.
    const wrapper = mount(ChatMessageItem, {
      props: { msg: { role: "user", content: "hello", cancelled: true }, collapsed: false },
    });
    const bubble = wrapper.find(".msg-content");
    expect(bubble.classes()).toContain("user-bubble");
    expect(bubble.classes()).toContain("user-bubble-cancelled");
    const badge = wrapper.find(".msg-cancelled-badge");
    expect(badge.exists()).toBe(true);
    expect(badge.text()).toBe("Cancelled");
  });

  it("renders markdown in a cancelled user message too", () => {
    const wrapper = mount(ChatMessageItem, {
      props: {
        msg: { role: "user", content: "**cancelled prompt**", cancelled: true },
        collapsed: false,
      },
    });

    expect(wrapper.get(".user-bubble-cancelled strong").text()).toBe("cancelled prompt");
  });

  it("renders assistant message with markdown", () => {
    const wrapper = mount(ChatMessageItem, {
      props: { msg: { role: "assistant", content: "**bold**" }, collapsed: false },
    });
    expect(wrapper.find(".msg-label").text()).toBe("Claude");
    expect(wrapper.find(".markdown-body").html()).toContain("<p><strong>bold</strong></p>");
  });

  it("renders a copy button on assistant messages that writes the raw markdown to the clipboard", async () => {
    // The button copies msg.content (source markdown), not the rendered
    // HTML — what the user gets back in their paste buffer matches what
    // they'd see in any other markdown editor, which is the whole point
    // of the affordance. The click flips the icon to a check for ~1.5s
    // so the action is acknowledged without a separate toast.
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText },
      configurable: true,
    });

    const wrapper = mount(ChatMessageItem, {
      props: {
        msg: { role: "assistant", content: "# Hello\n\n**bold**" },
        collapsed: false,
      },
    });
    const btn = wrapper.find(".copy-btn");
    expect(btn.classes()).toContain("opacity-0");
    expect(btn.classes()).toContain("group-hover/message:opacity-100");
    expect(btn.exists()).toBe(true);
    expect(btn.attributes("aria-label")).toBe("Copy markdown");

    await btn.trigger("click");
    expect(writeText).toHaveBeenCalledWith("# Hello\n\n**bold**");
    // Wait a tick for the async copy + reactive state flush.
    await wrapper.vm.$nextTick();
    expect(wrapper.find(".copy-btn").attributes("aria-label")).toBe("Copied");
  });

  it("copies only the code from an assistant markdown code block", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText },
      configurable: true,
    });

    const wrapper = mount(ChatMessageItem, {
      props: {
        msg: { role: "assistant", content: "```\nconst answer = 42;\n```" },
        collapsed: false,
      },
    });
    const button = wrapper.get(".code-copy-button");
    expect(button.attributes("aria-label")).toBe("Copy code");

    await button.trigger("click");
    expect(writeText).toHaveBeenCalledWith("const answer = 42;\n");
    await flushPromises();
    expect(button.attributes("aria-label")).toBe("Copied");
  });

  it("does not render a copy button on user messages", () => {
    // The copy affordance only makes sense on agent replies — user
    // messages are short and already plain text in the bubble.
    const wrapper = mount(ChatMessageItem, {
      props: { msg: { role: "user", content: "hello" }, collapsed: false },
    });
    expect(wrapper.find(".copy-btn").exists()).toBe(false);
  });

  it("renders error message", () => {
    const wrapper = mount(ChatMessageItem, {
      props: { msg: { role: "error", content: "fail" }, collapsed: false },
    });
    expect(wrapper.find(".msg-label").text()).toBe("Error");
    expect(wrapper.find(".error-content").exists()).toBe(true);
  });

  it("renders warning messages with warning styling", () => {
    const wrapper = mount(ChatMessageItem, {
      props: { msg: { role: "warning", content: "usage reminder" }, collapsed: false },
    });
    expect(wrapper.find(".msg-label").text()).toBe("Warning");
    expect(wrapper.find(".warning-content").classes()).toContain("bg-amber-50");
    expect(wrapper.find(".error-content").exists()).toBe(false);
  });

  it("renders tool activity", () => {
    const wrapper = mount(ChatMessageItem, {
      props: {
        msg: { role: "activity", content: "[Read]\n  file: /tmp", activityType: "tool" },
        collapsed: false,
      },
    });
    expect(wrapper.find(".tool-header").text()).toContain("[Read]");
    expect(wrapper.find(".tool-detail").text()).toContain("file: /tmp");
  });

  it("can render tool activity with details collapsed by default", async () => {
    const wrapper = mount(ChatMessageItem, {
      props: {
        msg: { role: "activity", content: "[Read]\n  file: /tmp", activityType: "tool" },
        collapsed: false,
        defaultToolCollapsed: true,
      },
    });
    expect(wrapper.find(".tool-header").text()).toContain("[Read]");
    expect(wrapper.find(".tool-detail").exists()).toBe(false);

    await wrapper.find(".tool-header").trigger("click");
    expect(wrapper.find(".tool-detail").text()).toContain("file: /tmp");
  });

  it("renders thinking activity with collapse toggle (markdown live)", async () => {
    // Thinking content is markdown-rendered as it streams so headings /
    // lists / code blocks look right immediately, instead of snapping
    // from <pre> plain text to formatted prose at completion. The mock
    // wraps content in <p>…</p> so we assert against that shape.
    const wrapper = mount(ChatMessageItem, {
      props: {
        msg: { role: "activity", content: "Let me think...", activityType: "thinking" },
        collapsed: false,
      },
    });
    expect(wrapper.find(".thinking-entry").exists()).toBe(true);
    const body = wrapper.find(".thinking-text");
    expect(body.classes()).toContain("markdown-body");
    expect(body.html()).toContain("<p>Let me think...</p>");

    await wrapper.find(".thinking-entry").trigger("click");
    expect(wrapper.emitted("toggle-thinking")).toBeTruthy();
  });

  it("opens a link in the transcript in a new tab without collapsing the row", async () => {
    // A thinking row collapses when its body is clicked, so a link inside it
    // used to fold the reasoning away just as the new tab opened.
    const wrapper = mount(ChatMessageItem, {
      props: {
        msg: {
          role: "activity",
          content: '<a href="https://example.com/doc">the doc</a>',
          activityType: "thinking",
        },
        collapsed: false,
      },
    });

    const link = wrapper.get(".thinking-text a");
    await link.trigger("click");

    expect(link.attributes("target")).toBe("_blank");
    expect(link.attributes("rel")).toBe("noopener noreferrer");
    expect(wrapper.emitted("toggle-thinking")).toBeUndefined();
  });

  it("hides thinking text when collapsed", () => {
    const wrapper = mount(ChatMessageItem, {
      props: {
        msg: { role: "activity", content: "Thinking...", activityType: "thinking" },
        collapsed: true,
      },
    });
    expect(wrapper.find(".thinking-text").exists()).toBe(false);
    expect(wrapper.find(".thinking-summary").attributes("aria-expanded")).toBe("false");
  });

  it("renders info activity", () => {
    const wrapper = mount(ChatMessageItem, {
      props: {
        msg: { role: "activity", content: "Model: opus", activityType: "info" },
        collapsed: false,
      },
    });
    expect(wrapper.find(".info-entry").text()).toContain("Model: opus");
  });

  it("renders stream activity as markdown live", () => {
    // The in-flight assistant reply uses the same markdown pipeline as
    // the final assistant bubble so the rendered output doesn't visibly
    // re-flow when the result event lands and the row gets promoted.
    const wrapper = mount(ChatMessageItem, {
      props: {
        msg: { role: "activity", content: "streaming text", activityType: "stream" },
        collapsed: false,
      },
    });
    const stream = wrapper.find(".stream-text");
    expect(stream.classes()).toContain("markdown-body");
    expect(stream.html()).toContain("<p>streaming text</p>");
  });

  it("does not mark a regular activity as a sub-agent track", () => {
    const wrapper = mount(ChatMessageItem, {
      props: {
        msg: { role: "activity", content: "[Read]", activityType: "tool" },
        collapsed: false,
      },
    });
    expect(wrapper.find(".subagent-track").exists()).toBe(false);
    expect(wrapper.find('[data-subagent="true"]').exists()).toBe(false);
  });

  it("renders sub-agent activities in an indented track so they don't look like duplicates of the parent", () => {
    const wrapper = mount(ChatMessageItem, {
      props: {
        msg: {
          role: "activity",
          content: "[Read] calling...",
          activityType: "tool",
          subagent: true,
        },
        collapsed: false,
      },
    });
    expect(wrapper.find(".subagent-track").exists()).toBe(true);
    expect(wrapper.find('[data-subagent="true"]').exists()).toBe(true);
    // The chip itself still renders inside the indented wrapper.
    expect(wrapper.find(".tool-header").text()).toContain("[Read]");
  });

  it("renders model activity with the same chip styling as usage so the banner reads as metadata", () => {
    // The user wanted the top-of-chat "Model: ..." line to look like the
    // bottom-of-chat token-cost pill — both are footers/headers around
    // the conversation prose, neither should compete with assistant text
    // for the eye. We collapse them onto the shared .usage-entry chip
    // (muted bg, mono, [11px]) so they read as a single visual class.
    const wrapper = mount(ChatMessageItem, {
      props: {
        msg: {
          role: "activity",
          content: "Model: claude-opus-4-8 | Tools: 56 available",
          activityType: "model",
        },
        collapsed: false,
      },
    });
    const pill = wrapper.find(".usage-entry");
    expect(pill.exists()).toBe(true);
    expect(pill.text()).toContain("Model: claude-opus-4-8");
    expect(pill.classes()).toContain("font-mono");
  });

  it("renders a muted sent-at hint next to the username on user bubbles when created_at is supplied", () => {
    // The hint sits in its own .msg-sent-at span so the existing
    // .msg-label text assertions stay clean (the label keeps showing
    // just the name). formatSentTime defers to Intl.DateTimeFormat
    // with the browser's default locale; we assert presence + a
    // digit rather than exact characters because Node's ICU build
    // can format times differently from a real browser.
    const wrapper = mount(ChatMessageItem, {
      props: {
        msg: { role: "user", content: "hi", created_at: new Date().toISOString() },
        collapsed: false,
      },
    });
    const hint = wrapper.find(".msg-sent-at");
    expect(hint.exists()).toBe(true);
    expect(hint.text()).toMatch(/\d/);
    expect(wrapper.find(".msg-label").text()).toBe("You");
  });

  it("renders a sent-at hint on assistant bubbles too so both sides of the conversation are dated", () => {
    const wrapper = mount(ChatMessageItem, {
      props: {
        msg: { role: "assistant", content: "ok", created_at: new Date().toISOString() },
        collapsed: false,
      },
    });
    expect(wrapper.find(".msg-sent-at").exists()).toBe(true);
  });

  it("omits the sent-at hint when no created_at is supplied so optimistic / pre-stamp rows don't render an empty span", () => {
    const wrapper = mount(ChatMessageItem, {
      props: { msg: { role: "user", content: "hi" }, collapsed: false },
    });
    expect(wrapper.find(".msg-sent-at").exists()).toBe(false);
  });

  it("labels a live thinking row as in progress and a claimed one as a finished thought process", () => {
    // The summary line keeps its shape across the live → claimed flip (Iris
    // "用时 48 秒" line); only the label and the pulse change, so the row
    // doesn't jump when the result event stamps it with a DB id.
    const live = mount(ChatMessageItem, {
      props: {
        msg: { role: "activity", content: "pondering", activityType: "thinking" },
        collapsed: false,
      },
    });
    expect(live.find(".thinking-entry").attributes("data-live")).toBe("true");
    expect(live.find(".thinking-summary").text()).toContain("Thinking");

    const finalised = mount(ChatMessageItem, {
      props: {
        msg: {
          id: "msg-123",
          role: "activity",
          content: "pondering",
          activityType: "thinking",
        },
        collapsed: true,
      },
    });
    expect(finalised.find(".thinking-entry").attributes("data-live")).toBeUndefined();
    expect(finalised.find(".thinking-summary").text()).toContain("Thought process");
  });

  it("renders a non-image assistant attachment as a result card with its file kind", () => {
    const wrapper = mount(ChatMessageItem, {
      props: {
        msg: {
          role: "assistant",
          content: "done",
          attachments: [
            {
              name: "Q3 report.xlsx",
              mime: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
              path: "./Q3 report.xlsx",
              url: "/api/users/u1/files/download?path=.%2FQ3%20report.xlsx",
            },
          ],
        },
        collapsed: false,
      },
    });
    const card = wrapper.get(".assistant-attachment-card");
    expect(card.attributes("href")).toContain("/files/download");
    expect(card.text()).toContain("Spreadsheet · XLSX");
    expect(card.text()).toContain("Download");
  });

  // The composer appends the absolute upload path so the agent can open the
  // file. Leaving it in the bubble printed a wall of host paths above the
  // attachment chips that already name the same files.
  it("hides the appended upload paths from the user bubble", () => {
    const wrapper = mount(ChatMessageItem, {
      props: {
        msg: {
          role: "user",
          content:
            "分析这个文件\n\n/home/alice/.daymug/agents/a1/uploads/20260919-doc.ai" +
            " /home/alice/.daymug/agents/a1/uploads/20260919-shot.png",
          attachments: [
            {
              name: "doc.ai",
              mime: "application/postscript",
              path: ".daymug/agents/a1/uploads/20260919-doc.ai",
              url: "/api/users/h1/files/read?path=doc",
            },
            {
              name: "shot.png",
              mime: "image/png",
              path: ".daymug/agents/a1/uploads/20260919-shot.png",
              url: "/api/users/h1/files/read?path=shot",
            },
          ],
        },
        collapsed: false,
      },
    });

    const bubble = wrapper.find(".user-bubble").text();
    expect(bubble).toContain("分析这个文件");
    expect(bubble).not.toContain("/home/alice");
    expect(wrapper.findAll('[data-testid="message-attachment-image"]').length).toBe(1);
  });

  // An attachment-only message has nothing left once the refs go, and an empty
  // paragraph above the chips would leave a stray gap in the bubble.
  it("renders no text block when the message was only attachments", () => {
    const wrapper = mount(ChatMessageItem, {
      props: {
        msg: {
          role: "user",
          content: "/home/alice/.daymug/agents/a1/uploads/20260919-shot.png",
          attachments: [
            {
              name: "shot.png",
              mime: "image/png",
              path: ".daymug/agents/a1/uploads/20260919-shot.png",
              url: "/api/users/h1/files/read?path=shot",
            },
          ],
        },
        collapsed: false,
      },
    });

    expect(wrapper.find(".user-bubble").text()).toBe("");
    expect(wrapper.find('[data-testid="message-attachment-image"]').exists()).toBe(true);
  });

  // Anything that is not the composer's own trailing ref block is left alone:
  // a half-match must not silently eat the end of what the user typed.
  it("leaves the text untouched when the tail is not the attachment refs", () => {
    const content = "see /home/alice/.daymug/agents/a1/uploads/other.png instead";
    const wrapper = mount(ChatMessageItem, {
      props: {
        msg: {
          role: "user",
          content,
          attachments: [
            {
              name: "shot.png",
              mime: "image/png",
              path: ".daymug/agents/a1/uploads/20260919-shot.png",
              url: "/api/users/h1/files/read?path=shot",
            },
          ],
        },
        collapsed: false,
      },
    });

    expect(wrapper.find(".user-bubble").text()).toContain(content);
  });

  // Clicking a chat image used to open a new tab, losing the conversation
  // behind it and offering no zoom.
  it("opens the in-page viewer when an image attachment is clicked", async () => {
    const wrapper = mount(ChatMessageItem, {
      props: {
        msg: {
          role: "user",
          content: "看这张图",
          attachments: [
            {
              name: "shot.png",
              mime: "image/png",
              path: ".daymug/agents/a1/uploads/shot.png",
              url: "/api/users/h1/files/read?path=shot",
            },
          ],
        },
        collapsed: false,
      },
      global: { stubs: { teleport: true } },
    });

    expect(wrapper.find('[data-testid="image-lightbox"]').exists()).toBe(false);
    await wrapper.get('[data-testid="message-attachment-image"]').trigger("click");

    const viewer = wrapper.get('[data-testid="image-lightbox-image"]');
    expect(viewer.attributes("src")).toBe("/api/users/h1/files/read?path=shot");

    await wrapper.get('[data-testid="image-lightbox"]').trigger("click");
    expect(wrapper.find('[data-testid="image-lightbox"]').exists()).toBe(false);
  });

  it("opens the viewer for an assistant image attachment too", async () => {
    const wrapper = mount(ChatMessageItem, {
      props: {
        msg: {
          role: "assistant",
          content: "done",
          attachments: [
            {
              name: "chart.png",
              mime: "image/png",
              path: "out/chart.png",
              url: "/api/users/h1/files/read?path=chart",
            },
          ],
        },
        collapsed: false,
      },
      global: { stubs: { teleport: true } },
    });

    await wrapper.get('[data-testid="assistant-attachment-image"]').trigger("click");
    expect(wrapper.get('[data-testid="image-lightbox-image"]').attributes("src")).toBe(
      "/api/users/h1/files/read?path=chart",
    );
  });

  // Only images get the viewer — anything else still has to be downloadable.
  it("keeps a non-image attachment as a plain link", () => {
    const wrapper = mount(ChatMessageItem, {
      props: {
        msg: {
          role: "user",
          content: "请处理",
          attachments: [
            {
              name: "brief.pdf",
              mime: "application/pdf",
              path: ".daymug/agents/a1/uploads/brief.pdf",
              url: "/api/users/h1/files/read?path=brief",
            },
          ],
        },
        collapsed: false,
      },
    });

    const link = wrapper.get('[data-testid="message-attachment-file"]').element.closest("a");
    expect(link?.getAttribute("href")).toBe("/api/users/h1/files/read?path=brief");
    expect(link?.getAttribute("target")).toBe("_blank");
  });
});
