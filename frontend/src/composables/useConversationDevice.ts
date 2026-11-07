import { computed, reactive } from "vue";
import { desktopClient } from "@/lib/desktopClient";
import { fetchConversationDevice, type ConversationDeviceBinding } from "./apiDevices";
import { useDesktopDevice } from "./useDesktopDevice";

// Which computer folder each conversation is bound to, shared by the run
// location selector (which changes it) and the chat page (whose file panel
// and composer follow it). `undefined` = not loaded yet, `null` = runs on the
// server. Every page loads it, a plain browser included: a conversation bound
// to a computer is read-only everywhere but in that computer's client.
const bindings = reactive(new Map<string, ConversationDeviceBinding | null>());

// The chat socket's refusal of a prompt for a conversation bound to another
// computer (service.ErrConversationOnDevice). Kept verbatim so the dispatcher
// can tell it apart from turn-ending errors.
export const DEVICE_CONVERSATION_REFUSAL =
  "this conversation runs in a folder on another computer; continue it in DayMug-Client on that computer";

export async function loadConversationDevice(
  conversationId: string,
): Promise<ConversationDeviceBinding | null> {
  if (!conversationId) return null;
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

// A conversation bound to a computer folder can be driven only from the
// DayMug-Client on that computer; anywhere else (a browser, the client on
// another computer) it is view-only. Inside a client whose own device is not
// resolved yet the page stays writable — the server refuses a wrong sender
// anyway, and a false lock would strand the user on their own computer.
export function useConversationReadOnly(conversationId: () => string) {
  const binding = useConversationDevice(conversationId);
  const { device } = useDesktopDevice();
  return computed(() => {
    const b = binding.value;
    if (!b) return false;
    if (!desktopClient()) return true;
    return !!device.value && device.value.id !== b.device_id;
  });
}
