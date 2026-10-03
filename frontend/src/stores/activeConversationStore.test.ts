import { beforeEach, describe, expect, it } from "vitest";

import type { Conversation } from "@/composables/apiTypes";
import {
  bindConversationLookup,
  conversationListEvent,
  lookupConversation,
  publishConversationListEvent,
  publishTitleUpdate,
  resetActiveConversationStore,
  titleUpdate,
} from "./activeConversationStore";

function row(id: string): Conversation {
  return {
    id,
    user_id: "u1",
    title: "Session",
    provider: "claude",
    model: "",
    work_dir: "/srv/work",
    session_id: "",
    notifications_enabled: false,
    pinned: false,
    pin_order: 0,
    account_name: "",
    created_at: "",
    updated_at: "",
  };
}

beforeEach(() => {
  resetActiveConversationStore();
});

describe("publishTitleUpdate", () => {
  it("bumps the version so a repeated title still wakes the watcher", () => {
    publishTitleUpdate("c1", "Same title");
    const first = titleUpdate.value?.version ?? 0;

    publishTitleUpdate("c1", "Same title");

    expect(titleUpdate.value).toMatchObject({ id: "c1", title: "Same title" });
    expect(titleUpdate.value?.version).toBe(first + 1);
  });
});

describe("publishConversationListEvent", () => {
  it("carries the full row so a peer tab can splice it in place", () => {
    publishConversationListEvent("updated", "c1", row("c1"));

    expect(conversationListEvent.value).toMatchObject({ kind: "updated", id: "c1" });
    expect(conversationListEvent.value?.conversation?.work_dir).toBe("/srv/work");
  });

  it("omits the row entirely for a removal", () => {
    publishConversationListEvent("removed", "c1");

    expect(conversationListEvent.value?.kind).toBe("removed");
    expect(conversationListEvent.value).not.toHaveProperty("conversation");
  });

  it("versions every publish so identical payloads still fire", () => {
    publishConversationListEvent("added", "c1", row("c1"));
    const first = conversationListEvent.value?.version ?? 0;

    publishConversationListEvent("added", "c1", row("c1"));

    expect(conversationListEvent.value?.version).toBe(first + 1);
  });
});

describe("conversation lookup injection", () => {
  it("routes through the injected cache and reports a miss as null", () => {
    const cache = new Map([["c1", row("c1")]]);
    bindConversationLookup((id) => cache.get(id) ?? null);

    expect(lookupConversation("c1")?.id).toBe("c1");
    expect(lookupConversation("nope")).toBeNull();
  });
});
