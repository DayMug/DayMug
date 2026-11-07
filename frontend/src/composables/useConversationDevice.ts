import { computed, reactive } from "vue";
import { desktopClient } from "@/lib/desktopClient";
import { fetchConversationDevice, type ConversationDeviceBinding } from "./apiDevices";

// Which computer folder each conversation is bound to, shared by the run
// location selector (which changes it) and the chat page (whose file panel
// follows it). `undefined` = not loaded yet, `null` = runs on the server.
// Only DayMug-Client pages ever load anything: a browser keeps every
// conversation on the server view and issues no device request.
const bindings = reactive(new Map<string, ConversationDeviceBinding | null>());

export async function loadConversationDevice(
  conversationId: string,
): Promise<ConversationDeviceBinding | null> {
  if (!conversationId || !desktopClient()) return null;
  try {
    const b = await fetchConversationDevice(conversationId);
    bindings.set(conversationId, b);
    return b;
  } catch {
    if (!bindings.has(conversationId)) bindings.set(conversationId, null);
    return bindings.get(conversationId) ?? null;
  }
}

export function setConversationDeviceBinding(
  conversationId: string,
  binding: ConversationDeviceBinding | null,
) {
  bindings.set(conversationId, binding);
}

export function useConversationDevice(conversationId: () => string) {
  return computed(() => bindings.get(conversationId()) ?? null);
}
