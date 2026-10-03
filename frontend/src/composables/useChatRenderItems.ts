import { computed, type ComputedRef, type Ref } from "vue";
import type { ChatMessage } from "@/stores/chatMessageStore";

// A transcript entry as the template consumes it: either a single message or
// a folded run of consecutive tool calls. `key` identifies the entry across
// re-renders — see stableKey below for why it isn't the render index.
export type RenderItem =
  | { kind: "message"; key: string; msg: ChatMessage }
  | {
      kind: "tool-group";
      key: string;
      subagent: boolean;
      tools: { key: string; msg: ChatMessage }[];
    };

export type ToolGroupItem = Extract<RenderItem, { kind: "tool-group" }>;

// Identity of a rendered row, used both for `v-for` keys and for the
// transcript's fold state. Position in the list is *not* usable for either:
// prepending a page of older history shifts every index down, so index-keyed
// fold state would migrate to whichever row inherited the number.
//
// Memoised against the message object, because that reference is what the
// message pipeline keeps stable — prepending history and the density filter
// both rebuild the array out of the existing entries, and a live thinking row
// is mutated in place when `claimTrailingThinking` stamps it with its
// persisted id. A minted key therefore stays constant across that mid-flight
// transition. DB ids cannot be keys: overlapping delivery paths can briefly
// produce distinct rows with the same id, and Vue then leaves orphaned DOM
// bubbles behind even after switching conversations. Rows that arrive as fresh
// objects (another conversation, a REST-restored page) mint a new key, which
// is exactly when fold state should not carry over.
const stableKeys = new WeakMap<ChatMessage, string>();
let mintedKeys = 0;

function stableKey(msg: ChatMessage): string {
  const known = stableKeys.get(msg);
  if (known) return known;
  const key = `row:${++mintedKeys}`;
  stableKeys.set(msg, key);
  return key;
}

// useChatRenderItems turns the flat message list into the render plan the
// transcript draws: consecutive `tool` activity rows fold into one card, and
// sub-agent runs group separately so the indented worker track doesn't merge
// into the parent's tool chain.
export function useChatRenderItems(messages: Ref<ChatMessage[]>): ComputedRef<RenderItem[]> {
  return computed<RenderItem[]>(() => {
    const items: RenderItem[] = [];
    const list = messages.value;

    let i = 0;
    while (i < list.length) {
      const m = list[i];
      if (m.role === "activity" && m.activityType === "tool") {
        const subagent = !!m.subagent;
        const group: { key: string; msg: ChatMessage }[] = [];
        while (
          i < list.length &&
          list[i].role === "activity" &&
          list[i].activityType === "tool" &&
          !!list[i].subagent === subagent
        ) {
          group.push({ key: stableKey(list[i]), msg: list[i] });
          i++;
        }
        items.push({ kind: "tool-group", key: group[0].key, subagent, tools: group });
        continue;
      }
      items.push({ kind: "message", key: stableKey(m), msg: m });
      i++;
    }
    return items;
  });
}
