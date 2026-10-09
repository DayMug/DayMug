import type { Conversation } from "./useApi";
import {
  activeAgentId,
  closeConversationDrawer,
  conversations,
  conversationsByAgent,
  conversationsHasMore,
  deletingIds,
  isConversationDrawerOpen,
  isConversationPanelOpen,
  isCreatingConversation,
  isLoadingMoreConversations,
  loadingAgentIds,
  renamingIds,
  toggleConversationDrawer,
  toggleConversationPanel,
} from "@/stores/conversationListStore";
import {
  CONVERSATION_PAGE_SIZE,
  fetchConversations,
  fetchConversationsPage,
  createConversation,
  deleteConversation,
  renameConversation,
  updateConversationNotifications,
  updateConversationPinned,
  reorderPinnedConversations,
} from "./useApi";
import { useChat, bindConversationLookup } from "./useChat";
import { clearDraft } from "@/lib/chatDrafts";
import { loadRecentModel } from "@/lib/recentModel";
import { dropAttention } from "@/stores/conversationAttentionStore";

const BROWSER_SYNC_CHANNEL = "daymug-conversations";
const browserSyncSource =
  typeof crypto !== "undefined" && "randomUUID" in crypto
    ? crypto.randomUUID()
    : Math.random().toString(36).slice(2);
let browserSyncChannel: BroadcastChannel | null | undefined;

// Inject a conversation-row lookup into useChat so switchToConversation can
// skip its dedicated GET /api/conversations/:id round-trip whenever the
// row is already cached in the conversation-list store (the active sidebar list
// or any agent bucket the user has expanded). useChat falls back to the
// network fetch when the lookup returns null — first-time deep-link into
// an unseen conversation. Wired at module load so the binding is in place
// before the router's first switchToConversation can fire.
bindConversationLookup((id) => {
  const fromActive = conversations.value.find((c) => c.id === id);
  if (fromActive) return fromActive;
  for (const bucket of Object.values(conversationsByAgent.value)) {
    const hit = bucket.find((c) => c.id === id);
    if (hit) return hit;
  }
  return null;
});

// Generation token for "which agent the active sidebar list is bound to".
// Both loaders below await a fetch before writing `conversations` +
// `activeAgentId`; without this, switching from agent A to agent B while A's
// request is still in flight lets A's slower response repaint B's sidebar with
// A's rows (and hand switchToConversation a conversation the user left).
let activeListRun = 0;

function syncAgentMap(userId: string, list: Conversation[]) {
  conversationsByAgent.value = {
    ...conversationsByAgent.value,
    [userId]: [...list],
  };
}

// Sort pinned rows above unpinned ones; within the pinned block honour the
// manual `pin_order` (ascending), and within the unpinned block fall back to
// updated_at DESC — matches the backend's `ORDER BY pinned DESC, pin_order ASC,
// updated_at DESC`. Used after any in-place mutation that might land a row out
// of order (peer-tab updates, message-driven `updated_at` bumps, pin/reorder).
function sortByUpdatedAtDesc(list: Conversation[]): Conversation[] {
  return [...list].sort((a, b) => {
    const pa = a.pinned ? 1 : 0;
    const pb = b.pinned ? 1 : 0;
    if (pa !== pb) return pb - pa;
    if (a.pinned && b.pinned) {
      const oa = a.pin_order ?? 0;
      const ob = b.pin_order ?? 0;
      if (oa !== ob) return oa - ob;
    }
    const ua = a.updated_at ?? "";
    const ub = b.updated_at ?? "";
    if (ua !== ub) return ua > ub ? -1 : 1;
    // updated_at is second-resolution (datetime('now')), so rows touched in
    // the same second tie. Break deterministically by created_at then id —
    // matches the backend's `... updated_at DESC, created_at DESC, id DESC`
    // so an optimistic local insert and a server refresh agree on the order.
    const ca = a.created_at ?? "";
    const cb = b.created_at ?? "";
    if (ca !== cb) return ca > cb ? -1 : 1;
    if (a.id !== b.id) return a.id > b.id ? -1 : 1;
    return 0;
  });
}

// appendPage merges a freshly-fetched window onto the rows already rendered,
// dropping ids we've seen. The dedupe is a correctness requirement of offset
// paging, not an optimisation: `updated_at` is the sort key and keeps moving,
// so a conversation that gets a reply while the user is mid-scroll slides
// upward and re-appears inside a later window it was already rendered above.
function appendPage(base: Conversation[], page: Conversation[]): Conversation[] {
  const seen = new Set(base.map((c) => c.id));
  const merged = [...base];
  for (const conv of page) {
    if (seen.has(conv.id)) continue;
    seen.add(conv.id);
    merged.push(conv);
  }
  return merged;
}

// A full-length window means the server had at least that many rows left, so
// there is (probably) another page. A short window is proof there is not.
function pageHasMore(page: Conversation[]): boolean {
  return page.length >= CONVERSATION_PAGE_SIZE;
}

// mergeRefreshedHead treats a freshly-fetched first page as authoritative for
// the rows it covers and keeps everything the user had already paged in below
// it. Rows deleted server-side are reconciled by the user-hub
// `conversation_removed` broadcast, so dropping the tail here would only cost
// the user their scroll position.
function mergeRefreshedHead(fresh: Conversation[], existing: Conversation[]): Conversation[] {
  const refreshed = new Set(fresh.map((c) => c.id));
  return sortByUpdatedAtDesc([...fresh, ...existing.filter((c) => !refreshed.has(c.id))]);
}

function updateInAgentMap(id: string, updater: (conv: Conversation) => Conversation) {
  const map = { ...conversationsByAgent.value };
  let changed = false;
  for (const uid of Object.keys(map)) {
    const idx = map[uid].findIndex((c) => c.id === id);
    if (idx === -1) continue;
    const arr = [...map[uid]];
    arr[idx] = updater(arr[idx]);
    map[uid] = arr;
    changed = true;
  }
  if (changed) conversationsByAgent.value = map;
}

function removeFromAgentMap(id: string) {
  const map = { ...conversationsByAgent.value };
  let changed = false;
  for (const uid of Object.keys(map)) {
    const filtered = map[uid].filter((c) => c.id !== id);
    if (filtered.length !== map[uid].length) {
      map[uid] = filtered;
      changed = true;
    }
  }
  if (changed) conversationsByAgent.value = map;
}

type ConversationApplyOptions = {
  broadcast?: boolean;
};

type BrowserConversationSyncMessage = {
  source: string;
  kind: "added" | "removed" | "updated";
  id: string;
  conversation?: Conversation;
};

function getBrowserSyncChannel(): BroadcastChannel | null {
  if (browserSyncChannel !== undefined) return browserSyncChannel;
  if (typeof BroadcastChannel === "undefined") {
    browserSyncChannel = null;
    return browserSyncChannel;
  }
  browserSyncChannel = new BroadcastChannel(BROWSER_SYNC_CHANNEL);
  browserSyncChannel.onmessage = (event: MessageEvent<BrowserConversationSyncMessage>) => {
    const msg = event.data;
    if (!msg || msg.source === browserSyncSource) return;
    if (msg.kind === "added" && msg.conversation) {
      applyRemoteAdded(msg.conversation);
      return;
    }
    if (msg.kind === "updated" && msg.conversation) {
      applyRemoteUpdated(msg.conversation);
      return;
    }
    if (msg.kind === "removed") {
      applyRemoteRemoved(msg.id);
    }
  };
  return browserSyncChannel;
}

function broadcastBrowserConversationSync(
  kind: BrowserConversationSyncMessage["kind"],
  id: string,
  conversation?: Conversation,
) {
  // Rows forwarded by the app-state watcher come from a Vue ref and are
  // therefore reactive proxies. BroadcastChannel uses structured clone,
  // which rejects proxies with DataCloneError; a shallow snapshot is enough
  // because Conversation is a flat JSON API row.
  const cloneableConversation = conversation ? { ...conversation } : undefined;
  getBrowserSyncChannel()?.postMessage({
    source: browserSyncSource,
    kind,
    id,
    ...(cloneableConversation ? { conversation: cloneableConversation } : {}),
  });
}

// applyConversations seeds the active conversation list from a payload the
// caller already fetched (the /api/app-state aggregate). Mirrors the
// post-fetch state mutations from loadConversations so the cold-start path
// can skip the network call without losing the rest of the wiring (per-
// agent cache, active agent id, default-conversation switch).
//
// When `preferredConversationId` matches a row in the list we hand it
// straight to useChat.switchToConversation; otherwise we default to the
// first row, matching loadConversations' fallback. An empty list triggers
// createNewConversation just like the network path.
//
// `hasMore` carries the preload's paging signal so the sidebar knows the
// payload was only the first page. Omitted (a test seeding a list directly)
// reads as "this is the whole list".
async function applyConversations(
  userId: string,
  list: import("./apiTypes").Conversation[],
  preferredConversationId?: string,
  hasMore = false,
) {
  // Claim the active-list slot even though this path is synchronous, so an
  // older in-flight loadConversations can't overwrite what we just seeded.
  activeListRun++;
  conversations.value = list;
  activeAgentId.value = userId;
  conversationsHasMore.value = hasMore;
  syncAgentMap(userId, list);
  if (list.length > 0) {
    const { switchToConversation } = useChat();
    const target =
      preferredConversationId && list.some((c) => c.id === preferredConversationId)
        ? preferredConversationId
        : list[0].id;
    await switchToConversation(target);
  } else {
    await createNewConversation(userId);
  }
}

async function loadConversations(userId: string, preferredConversationId?: string) {
  const run = ++activeListRun;
  try {
    // Mirror loadConversationsForAgent: set the per-agent loading flag
    // around the fetch so the inline AgentConversationList renders
    // "Loading conversations…" instead of flashing "No conversations
    // yet." in the brief window between the row auto-expanding (when
    // currentUser becomes set on a fresh page load) and this fetch
    // resolving.
    setAgentLoading(userId, true);
    let list: Conversation[];
    try {
      list = await fetchConversationsPage(userId);
    } finally {
      setAgentLoading(userId, false);
    }
    // Cache the fetched rows for this agent regardless — the bucket is keyed
    // by agent, so it can never be the wrong list for someone else.
    syncAgentMap(userId, list);
    // A newer load/refresh/app-state apply rebound the sidebar: leave it be.
    if (run !== activeListRun) return;
    conversations.value = list;
    activeAgentId.value = userId;
    conversationsHasMore.value = pageHasMore(list);
    if (list.length > 0) {
      const { switchToConversation } = useChat();
      // Honour the caller's preferred conversation (typically the URL's
      // :conversationId on hard reload) so we don't briefly flash the
      // most recent conversation before the watcher swaps in the right
      // one.
      const target =
        preferredConversationId && list.some((c) => c.id === preferredConversationId)
          ? preferredConversationId
          : list[0].id;
      await switchToConversation(target);
    } else {
      await createNewConversation(userId);
    }
  } catch {
    // ignore
  }
}

// loadMoreConversations appends the next window to the active sidebar list.
// Append, never replace: the rows above are what the user is currently looking
// at, so a replace would make them flicker.
//
// The offset is the rendered row count rather than a stored cursor — that way
// it stays correct no matter how page one arrived (app-state preload, a plain
// load, or a refresh) and cannot drift out of sync with the list.
async function loadMoreConversations() {
  if (isLoadingMoreConversations.value || !conversationsHasMore.value) return;
  const userId = activeAgentId.value;
  if (!userId) return;
  const run = activeListRun;
  isLoadingMoreConversations.value = true;
  try {
    const page = await fetchConversationsPage(userId, conversations.value.length);
    // The sidebar rebound to another agent mid-flight — this window belongs to
    // a list nobody is showing any more.
    if (run !== activeListRun || activeAgentId.value !== userId) return;
    const merged = appendPage(conversations.value, page);
    conversations.value = merged;
    syncAgentMap(userId, merged);
    conversationsHasMore.value = pageHasMore(page);
  } catch {
    // Leave hasMore set so the next click retries the same window instead of
    // stranding the user at a dead button.
  } finally {
    isLoadingMoreConversations.value = false;
  }
}

function setAgentLoading(userId: string, loading: boolean) {
  if (loading) {
    if (loadingAgentIds.value[userId]) return;
    loadingAgentIds.value = { ...loadingAgentIds.value, [userId]: true };
  } else {
    if (!loadingAgentIds.value[userId]) return;
    const next = { ...loadingAgentIds.value };
    delete next[userId];
    loadingAgentIds.value = next;
  }
}

// Lazy-load a non-active agent's conversations into the per-agent
// cache without touching the active conversation. Used by the mobile
// Agents screen when a row is expanded.
//
// One-shot by design: once an agent's list lands in the cache we stop
// re-fetching on every re-expand. In-place mutations (rename / delete /
// new conversation / title-update) keep the cache live, so refusing to
// refetch avoids the conversation-count line flickering away and
// re-appearing each time the user taps between rows.
//
// First page only. The drill-down is a preview rendered inside a collapsible
// row with no room for a "load more" affordance; tapping through to the agent
// rebinds the active list, which does page.
async function loadConversationsForAgent(userId: string): Promise<Conversation[]> {
  const existing = conversationsByAgent.value[userId];
  if (existing) return existing;
  setAgentLoading(userId, true);
  try {
    const list = await fetchConversationsPage(userId);
    syncAgentMap(userId, list);
    return list;
  } catch {
    return [];
  } finally {
    setAgentLoading(userId, false);
  }
}

// Search is expected to cover the agent's whole history, not just the first
// sidebar page. Load the complete list into the same canonical stores used by
// rename/delete/live-update paths so search results stay current after a row is
// mutated. A non-active mobile agent only updates its own bucket; the active
// sidebar is rebound only when it still belongs to this agent after the fetch.
async function loadAllConversationsForAgent(userId: string): Promise<boolean> {
  try {
    const list = await fetchConversations(userId);
    syncAgentMap(userId, list);
    if (activeAgentId.value === userId) {
      conversations.value = list;
      conversationsHasMore.value = false;
    }
    return true;
  } catch {
    return false;
  }
}

// refreshConversations rebinds the active list from the server. It fires after
// every assistant result, so it must not undo paging: once the user has loaded
// extra pages, the fresh first page is merged over what is already rendered
// instead of replacing it — otherwise sending a message would silently snap a
// long scrolled-open sidebar back to one page.
async function refreshConversations(userId: string) {
  const run = ++activeListRun;
  try {
    const page = await fetchConversationsPage(userId);
    // A superseded refresh still caches its rows in the agent bucket (that is
    // keyed by agent, so it can't be wrong), but must not merge them into a
    // sidebar list that now belongs to someone else.
    const paged = run === activeListRun && conversations.value.length > page.length;
    const list = paged ? mergeRefreshedHead(page, conversations.value) : page;
    syncAgentMap(userId, list);
    if (run !== activeListRun) return;
    conversations.value = list;
    activeAgentId.value = userId;
    // Only adopt the fresh page's signal when it *is* the whole rendered list.
    // With rows surviving below it the user is still mid-list, and a short
    // first page says nothing about what lies past the rows they paged in.
    if (!paged) conversationsHasMore.value = pageHasMore(page);
  } catch {
    // ignore
  }
}

// Force-refresh an agent's cached conversation list. Unlike
// loadConversationsForAgent (one-shot by design), this always hits the
// network — used by mobile pull-to-refresh, where the user explicitly
// asked to refetch. Also rebinds the active conversation list when the
// targeted agent is the one currently displayed.
async function refreshConversationsForAgent(userId: string) {
  setAgentLoading(userId, true);
  try {
    const list = await fetchConversationsPage(userId);
    syncAgentMap(userId, list);
    if (activeAgentId.value === userId) {
      conversations.value = list;
      conversationsHasMore.value = pageHasMore(list);
    }
  } catch {
    // ignore
  } finally {
    setAgentLoading(userId, false);
  }
}

// applyRemoteAdded splices a conversation row created by another tab
// into the local sidebar list. Idempotent: a duplicate event (same id
// already present) becomes an in-place merge. Re-sorts by updated_at
// DESC so the row lands at its canonical position regardless of arrival
// order — important when this fires from the originator tab racing with
// its own POST response, or when a peer's row carries a fresher
// timestamp than what's locally cached.
function applyRemoteAdded(conv: Conversation, options: ConversationApplyOptions = {}) {
  // Only splice into the active sidebar list when the new conversation
  // belongs to the agent that list is currently bound to. Without this
  // guard, a "+ new" against agentA whose POST races a switch to
  // agentB would leave agentA's fresh row glued to the top of agentB's
  // sidebar until the next refresh. `activeAgentId` is empty before the
  // first loadConversations call (e.g. unit tests that seed the list
  // directly) — we treat that as accept-all so direct seeders aren't
  // forced to also stamp the active agent.
  if (!activeAgentId.value || conv.user_id === activeAgentId.value) {
    const idx = conversations.value.findIndex((c) => c.id === conv.id);
    let merged: Conversation[];
    if (idx === -1) {
      merged = [conv, ...conversations.value];
    } else {
      merged = [...conversations.value];
      merged[idx] = { ...merged[idx], ...conv };
    }
    conversations.value = sortByUpdatedAtDesc(merged);
  }
  // Mirror into the per-agent cache so the mobile Agents screen sees
  // the new row too. Only insert under the owning agent's bucket; if
  // that agent's list hasn't been lazy-loaded yet we leave it alone
  // (the next loadConversationsForAgent call will pull a fresh list).
  const map = { ...conversationsByAgent.value };
  const bucket = map[conv.user_id];
  if (bucket) {
    const j = bucket.findIndex((c) => c.id === conv.id);
    const nextBucket =
      j === -1
        ? [conv, ...bucket]
        : (() => {
            const arr = [...bucket];
            arr[j] = { ...arr[j], ...conv };
            return arr;
          })();
    map[conv.user_id] = sortByUpdatedAtDesc(nextBucket);
    conversationsByAgent.value = map;
  }
  if (options.broadcast) {
    broadcastBrowserConversationSync("added", conv.id, conv);
  }
}

// applyRemoteUpdated patches an existing conversation row with the
// canonical post-update fields and re-sorts by updated_at DESC so a
// row whose `updated_at` just bumped (e.g. the assistant just replied
// in another tab) jumps to the top of the sidebar. Falls through
// silently when the id isn't in the local list — the next switch /
// refresh will reconcile.
function applyRemoteUpdated(conv: Conversation, options: ConversationApplyOptions = {}) {
  const idx = conversations.value.findIndex((c) => c.id === conv.id);
  if (idx !== -1) {
    const next = [...conversations.value];
    next[idx] = { ...next[idx], ...conv };
    conversations.value = sortByUpdatedAtDesc(next);
  }
  updateInAgentMap(conv.id, (existing) => ({ ...existing, ...conv }));
  // Re-sort the per-agent bucket too so the mobile Agents screen sees
  // the same canonical order without a refresh.
  const bucket = conversationsByAgent.value[conv.user_id];
  if (bucket) {
    conversationsByAgent.value = {
      ...conversationsByAgent.value,
      [conv.user_id]: sortByUpdatedAtDesc(bucket),
    };
  }
  if (options.broadcast) {
    broadcastBrowserConversationSync("updated", conv.id, conv);
  }
}

// applyRemoteRemoved drops a conversation deleted by another tab. The
// active-conversation reconciliation (jumping to the next available
// conversation when the active one disappears) is handled by the
// app-shell watcher rather than here so we keep this function pure.
function applyRemoteRemoved(id: string, options: ConversationApplyOptions = {}): boolean {
  const present = conversations.value.some((c) => c.id === id);
  if (present) {
    conversations.value = conversations.value.filter((c) => c.id !== id);
  }
  removeFromAgentMap(id);
  dropAttention(id);
  if (options.broadcast) {
    broadcastBrowserConversationSync("removed", id);
  }
  return present;
}

function applyTitleUpdate(id: string, title: string) {
  const idx = conversations.value.findIndex((c) => c.id === id);
  if (idx !== -1) {
    conversations.value[idx] = { ...conversations.value[idx], title };
  }
  updateInAgentMap(id, (c) => ({ ...c, title }));
}

async function createNewConversation(userId: string) {
  if (isCreatingConversation.value) return;
  isCreatingConversation.value = true;
  try {
    const recent = loadRecentModel();
    let conv: Conversation;
    try {
      conv = recent
        ? await createConversation(
            userId,
            undefined,
            recent.provider,
            recent.model,
            recent.account,
            recent.think_level,
          )
        : await createConversation(userId);
    } catch (err) {
      if (!recent) throw err;
      conv = await createConversation(userId);
    }
    // Idempotent insert. The originator is also subscribed to its own
    // user-hub room, so a `conversation_added` broadcast can race the
    // POST response and `applyRemoteAdded` may have already inserted
    // the row. An unconditional unshift here would then duplicate it
    // in the sidebar.
    applyRemoteAdded(conv, { broadcast: true });
    // Only refresh the per-agent cache from the active list when the
    // create was for the currently displayed agent — otherwise (the
    // mid-flight agent-switch race) conversations.value is some other
    // agent's list and copying it into userId's bucket would corrupt
    // the mobile Agents screen's view of that agent.
    if (!activeAgentId.value || userId === activeAgentId.value) {
      syncAgentMap(userId, conversations.value);
    }
    const { switchToConversation } = useChat();
    await switchToConversation(conv.id);
  } catch {
    // ignore
  } finally {
    isCreatingConversation.value = false;
  }
}

async function deleteConversationById(id: string) {
  if (deletingIds.has(id)) return;
  deletingIds.add(id);
  try {
    await deleteConversation(id);
    applyRemoteRemoved(id, { broadcast: true });
    // Drop any cached composer draft for this conversation — the
    // conversation is gone server-side so the draft can never be sent
    // anywhere meaningful, and leaving it would keep storage pinned for
    // the LRU-cap lifetime.
    clearDraft(id);
    const { currentConversationId, switchToConversation } = useChat();
    if (currentConversationId.value === id && conversations.value.length > 0) {
      await switchToConversation(conversations.value[0].id);
    }
  } catch {
    // ignore
  } finally {
    deletingIds.delete(id);
  }
}

async function renameConversationById(id: string, title: string) {
  if (renamingIds.has(id)) return;
  renamingIds.add(id);
  try {
    const updated = await renameConversation(id, title);
    const idx = conversations.value.findIndex((c) => c.id === id);
    if (idx !== -1) {
      conversations.value[idx] = updated;
    }
    updateInAgentMap(id, () => updated);
    broadcastBrowserConversationSync("updated", updated.id, updated);
  } catch {
    // ignore
  } finally {
    renamingIds.delete(id);
  }
}

async function selectConversation(id: string) {
  const { switchToConversation } = useChat();
  await switchToConversation(id);
}

async function toggleConversationNotifications(id: string, enabled: boolean) {
  // Optimistic update so the bell flips instantly; revert on failure.
  const idx = conversations.value.findIndex((c) => c.id === id);
  const previous = idx !== -1 ? conversations.value[idx].notifications_enabled : null;
  if (idx !== -1) {
    conversations.value[idx] = {
      ...conversations.value[idx],
      notifications_enabled: enabled,
    };
  }
  updateInAgentMap(id, (c) => ({ ...c, notifications_enabled: enabled }));
  try {
    const updated = await updateConversationNotifications(id, enabled);
    // Re-locate the row: anything that re-sorts or splices the list while the
    // request is in flight (a peer-tab update, a pin toggle, a refresh) would
    // make the pre-await index point at a different conversation.
    const j = conversations.value.findIndex((c) => c.id === id);
    if (j !== -1) {
      conversations.value[j] = updated;
    }
    updateInAgentMap(id, () => updated);
    broadcastBrowserConversationSync("updated", updated.id, updated);
  } catch {
    const j = conversations.value.findIndex((c) => c.id === id);
    if (j !== -1 && previous !== null) {
      conversations.value[j] = {
        ...conversations.value[j],
        notifications_enabled: previous,
      };
    }
    if (previous !== null) {
      updateInAgentMap(id, (c) => ({ ...c, notifications_enabled: previous }));
    }
  }
}

// Optimistic pin toggle. Mutating `pinned` changes the row's sort position
// (pinned rows float to the top), so the list is re-sorted both on the
// optimistic write and after the server reply lands. Reverts both the flag
// and the order on failure.
async function toggleConversationPinned(id: string, pinned: boolean) {
  const idx = conversations.value.findIndex((c) => c.id === id);
  const previous = idx !== -1 ? conversations.value[idx].pinned : null;
  if (idx !== -1) {
    const next = [...conversations.value];
    next[idx] = { ...next[idx], pinned };
    conversations.value = sortByUpdatedAtDesc(next);
  }
  updateInAgentMap(id, (c) => ({ ...c, pinned }));
  const ownerId = conversations.value.find((c) => c.id === id)?.user_id;
  if (ownerId) {
    const bucket = conversationsByAgent.value[ownerId];
    if (bucket) {
      conversationsByAgent.value = {
        ...conversationsByAgent.value,
        [ownerId]: sortByUpdatedAtDesc(bucket),
      };
    }
  }
  try {
    const updated = await updateConversationPinned(id, pinned);
    applyRemoteUpdated(updated, { broadcast: true });
  } catch {
    if (previous !== null) {
      const j = conversations.value.findIndex((c) => c.id === id);
      if (j !== -1) {
        const next = [...conversations.value];
        next[j] = { ...next[j], pinned: previous };
        conversations.value = sortByUpdatedAtDesc(next);
      }
      updateInAgentMap(id, (c) => ({ ...c, pinned: previous }));
    }
  }
}

// Apply a new pinned order to a list: ids become pinned at their index,
// previously-pinned rows absent from ids become unpinned, then re-sort.
function applyPinnedReorder(list: Conversation[], ids: string[]): Conversation[] {
  const pos = new Map(ids.map((id, i) => [id, i] as const));
  const next = list.map((c) => {
    const p = pos.get(c.id);
    if (p !== undefined) return { ...c, pinned: true, pin_order: p };
    if (c.pinned) return { ...c, pinned: false };
    return c;
  });
  return sortByUpdatedAtDesc(next);
}

// reorderPinned persists a drag-to-reorder of a user's pinned conversations.
// `ids` is the full ordered pinned set (authoritative). Optimistically reorders
// both the active list and the per-agent bucket, then reconciles with the
// server's canonical list; reverts on failure.
async function reorderPinned(userId: string, ids: string[]) {
  const prevBucket = conversationsByAgent.value[userId];
  const prevActive = activeAgentId.value === userId ? conversations.value : null;

  if (prevBucket) {
    conversationsByAgent.value = {
      ...conversationsByAgent.value,
      [userId]: applyPinnedReorder(prevBucket, ids),
    };
  }
  if (prevActive) {
    conversations.value = applyPinnedReorder(prevActive, ids);
  }

  try {
    const serverList = await reorderPinnedConversations(userId, ids);
    conversationsByAgent.value = {
      ...conversationsByAgent.value,
      [userId]: serverList,
    };
    if (activeAgentId.value === userId) {
      conversations.value = serverList;
      // The reorder endpoint answers with the authoritative *whole* list, so
      // after adopting it there is nothing left to page in.
      conversationsHasMore.value = false;
    }
    for (const conv of serverList) {
      broadcastBrowserConversationSync("updated", conv.id, conv);
    }
  } catch {
    if (prevBucket) {
      conversationsByAgent.value = { ...conversationsByAgent.value, [userId]: prevBucket };
    }
    if (prevActive) {
      conversations.value = prevActive;
    }
  }
}

// Start a fresh conversation, rooted at the agent's fixed work_dir (the
// backend always uses the owning user's `work_dir`). The previous
// conversation stays in the list and can be revisited from the sidebar.
// Used by the "New" button.
async function startNewConversation(userId: string) {
  await createNewConversation(userId);
}

export function useConversations() {
  getBrowserSyncChannel();
  return {
    conversations,
    conversationsByAgent,
    activeAgentId,
    conversationsHasMore,
    isLoadingMoreConversations,
    loadingAgentIds,
    isConversationPanelOpen,
    isConversationDrawerOpen,
    isCreatingConversation,
    toggleConversationPanel,
    toggleConversationDrawer,
    closeConversationDrawer,
    loadConversations,
    loadMoreConversations,
    applyConversations,
    loadConversationsForAgent,
    loadAllConversationsForAgent,
    refreshConversations,
    refreshConversationsForAgent,
    applyTitleUpdate,
    applyRemoteAdded,
    applyRemoteUpdated,
    applyRemoteRemoved,
    createNewConversation,
    deleteConversation: deleteConversationById,
    renameConversation: renameConversationById,
    selectConversation,
    startNewConversation,
    toggleConversationNotifications,
    toggleConversationPinned,
    reorderPinned,
  };
}
