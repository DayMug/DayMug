// Global singleton store: which agents / conversations are running or waiting
// for an account slot. See src/stores/index.ts for the conventions every store
// in this directory follows.
import { computed, ref } from "vue";

// Global activity arrives as an authoritative WebSocket snapshot on init and
// after every job transition. Arrays keep template bindings cheap
// and serializable while the server handles deduplication.
export const runningAgentIds = ref<string[]>([]);
export const runningConversationIds = ref<string[]>([]);
export interface RunningConversationActivity {
  conversation_id: string;
  agent_id: string;
  agent_name?: string;
  owner_name?: string;
  owner_username?: string;
  account_name: string;
  model: string;
  started_at: string;
  // The turn has ended but the agent process is parked on background work it
  // left running; it wakes and continues on its own when that work finishes.
  background?: boolean;
}

export const runningConversations = ref<RunningConversationActivity[]>([]);
// Bumped on every snapshot. Each one follows a turn lifecycle change, which is
// also when the server rewrites a conversation's attention flag, so the app
// shell refetches that list off this counter.
export const activityVersion = ref(0);
export const queuedConversations = ref<RunningConversationActivity[]>([]);
// Turns parked on an AskUserQuestion prompt. They hold no slot and show no
// spinner, but they are still live: leaving this set is a completion.
export const waitingConversations = ref<RunningConversationActivity[]>([]);

// A queued turn is already active from the user's point of view: it owns a
// prompt, can be cancelled, and will begin as soon as an account slot opens.
// Keep the server's running-only ids intact for diagnostics while exposing the
// union used by every activity affordance in the navigation UI.
export const activeAgentIds = computed(() => [
  ...new Set([
    ...runningAgentIds.value,
    ...queuedConversations.value.map((activity) => activity.agent_id),
  ]),
]);
export const activeConversationIds = computed(() => [
  ...new Set([
    ...runningConversationIds.value,
    ...queuedConversations.value.map((activity) => activity.conversation_id),
  ]),
]);

export const backgroundConversationIds = computed(() =>
  runningConversations.value
    .filter((activity) => activity.background)
    .map((activity) => activity.conversation_id),
);

export function applyConversationActivity(
  agentIds?: string[],
  conversationIds?: string[],
  running?: RunningConversationActivity[],
  queued?: RunningConversationActivity[],
  waiting?: RunningConversationActivity[],
) {
  runningAgentIds.value = agentIds ?? [];
  runningConversationIds.value = conversationIds ?? [];
  runningConversations.value = running ?? [];
  queuedConversations.value = queued ?? [];
  waitingConversations.value = waiting ?? [];
  activityVersion.value += 1;
}

export function resetAgentActivityStore() {
  runningAgentIds.value = [];
  runningConversationIds.value = [];
  runningConversations.value = [];
  queuedConversations.value = [];
  waitingConversations.value = [];
  activityVersion.value = 0;
}
