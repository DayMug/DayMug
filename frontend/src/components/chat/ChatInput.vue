<script setup lang="ts">
// The composer pill: textarea, attachment affordances and send/cancel controls. Upload lifecycle lives in useChatAttachments and draft
// persistence in useChatDraft, so what remains here is the input itself and the
// keyboard contract that ties the three together.
import { ref, watch, nextTick, computed, toRef } from "vue";
import { onClickOutside, useNow } from "@vueuse/core";
import { Textarea } from "@/components/ui/textarea";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { ArrowUp, ChevronLeft, ChevronUp, Clock, ListEnd, Paperclip } from "lucide-vue-next";
import ChatAttachmentStrip from "./ChatAttachmentStrip.vue";
import { providerCapabilities, useModelRegistry } from "@/composables/useModelRegistry";
import { isImageMime, useChatAttachments } from "@/composables/useChatAttachments";
import { useChatDraft } from "@/composables/useChatDraft";
import { isPrimaryModifier, primaryModifierLabel } from "@/lib/primaryModifier";
import {
  defaultAbsoluteSendAt,
  formatSendAt,
  formatUntil,
  isValidSendAt,
  parseLocalDateTime,
  relativeSendAt,
  toLocalDateTimeInput,
  type ScheduleUnit,
} from "@/lib/scheduleTime";
import { useTimeFormat } from "@/composables/useTimeFormat";
import { useI18n } from "vue-i18n";
import type { MessageAttachment } from "@/composables/useWebSocket";

const props = withDefaults(
  defineProps<{
    isThinking: boolean;
    isConnected: boolean;
    // The server sends the active turn snapshot after the socket opens. Hide
    // the split control until that snapshot arrives so refresh never paints an
    // idle/black intermediate state before a running/white final state.
    turnStatusKnown?: boolean;
    // Conversation id the upload endpoint needs so the file lands in the
    // conversation agent's uploads directory. Empty during the brief window
    // before a conversation is selected — the upload button stays disabled.
    conversationId: string;
    // Text the dispatcher returned via cancel_ack: prompts the user had queued
    // that got dropped on cancel. Watched here so the editor refills with their
    // content for re-editing; we acknowledge by emitting `recall-consumed`.
    recallText?: string;
    recallAttachments?: MessageAttachment[];
    // Provider id ("claude" / "codex") of the current conversation's backend.
    // Decides whether a mid-turn send can be inserted into the running task.
    // Empty during the boot window — insert then stays the default.
    provider?: string;
    // The conversation's model is declared text-only, so attached images
    // reach the agent as paths it can't actually look at.
    imageInputUnsupported?: boolean;
    readOnly?: boolean;
  }>(),
  {
    recallText: "",
    recallAttachments: () => [],
    provider: "",
    imageInputUnsupported: false,
    readOnly: false,
    turnStatusKnown: true,
  },
);

const emit = defineEmits<{
  send: [text: string, attachments?: MessageAttachment[], insert?: boolean];
  // "Send later": hold the message on the server until sendAt.
  schedule: [text: string, attachments: MessageAttachment[] | undefined, sendAt: Date];
  cancel: [];
  // Fired once we've stuffed recallText into the editor, so the parent can
  // clear its own copy and avoid re-stuffing on the next render.
  "recall-consumed": [];
}>();

const { t, locale } = useI18n();
const { timeHour12 } = useTimeFormat();
const submitShortcut = computed(() => `${primaryModifierLabel()}+Enter`);

// Every entry must teach something the UI doesn't already make obvious.
// Generic invitations ("What shall we build?") cost a rotation slot to tell
// the user what an empty input box already tells them.
// Tips that need a keyboard or a mouse (shortcuts, right-click, modifier+click)
// are dropped on a touch screen, where they would teach something impossible.
const INPUT_PLACEHOLDER_KEYS = [
  "chat.inputPlaceholders.sendShortcut",
  "chat.inputPlaceholders.quickDelete",
  "chat.inputPlaceholders.queue",
  "chat.inputPlaceholders.reorder",
  "chat.inputPlaceholders.swipe",
  "chat.inputPlaceholders.fileMenu",
  "chat.inputPlaceholders.fileNewTab",
  "chat.inputPlaceholders.share",
] as const;
const TOUCH_PLACEHOLDER_KEYS = INPUT_PLACEHOLDER_KEYS.filter((key) =>
  [
    "chat.inputPlaceholders.queue",
    "chat.inputPlaceholders.reorder",
    "chat.inputPlaceholders.swipe",
  ].includes(key),
);
const placeholderPool: readonly string[] = window.matchMedia?.("(pointer: coarse)").matches
  ? TOUCH_PLACEHOLDER_KEYS
  : INPUT_PLACEHOLDER_KEYS;
const inputPlaceholderKey = placeholderPool[Math.floor(Math.random() * placeholderPool.length)];
const inputPlaceholder = computed(() =>
  t(inputPlaceholderKey, { shortcut: submitShortcut.value, modifier: primaryModifierLabel() }),
);

const inputText = ref("");
const inputRef = ref<InstanceType<typeof Textarea>>();
const inputElement = computed(() => inputRef.value?.$el as HTMLTextAreaElement | undefined);
const fileInputRef = ref<HTMLInputElement | null>(null);
const sendControlsRef = ref<HTMLElement | null>(null);
const sendMenuOpen = ref(false);

function focusInput() {
  inputElement.value?.focus();
}

useChatDraft(toRef(props, "conversationId"), inputText);

const {
  attachments,
  uploads,
  isUploading,
  uploadFile,
  cancelUpload,
  removeAttachment,
  dismissUpload,
  restoreAttachments,
  clearAfterSend,
} = useChatAttachments(toRef(props, "conversationId"));

const showImageInputWarning = computed(
  () => props.imageInputUnsupported && attachments.value.some((a) => isImageMime(a.mime)),
);

// When the dispatcher hands back cancelled prompts, drop them into the editor
// and signal the parent to clear its copy. Prepended so a user who was already
// mid-typing doesn't lose what they had.
watch(
  () => props.recallText,
  (next) => {
    if (!next) return;
    restoreAttachments(props.recallAttachments);
    inputText.value = inputText.value ? `${next}\n\n${inputText.value}` : next;
    nextTick(focusInput);
    emit("recall-consumed");
  },
);

function handlePaste(e: ClipboardEvent) {
  if (props.readOnly) return;
  const data = e.clipboardData;
  if (!data) return;
  // Text wins over attachments when both are on the clipboard. Copying a cell
  // range out of Excel / Numbers / Sheets puts both a text/plain payload and a
  // rendered image of the selection on the board; users almost always want the
  // text, and the image-upload branch would otherwise silently shadow it.
  if (data.getData("text/plain")) return;
  const fileItems: { file: File; name: string }[] = [];
  for (const item of data.items) {
    // Only intercept file-kind items. Plain-text paste falls through to the
    // textarea so copy-paste of typed prompts behaves normally. Any MIME is
    // fine — images, PDFs, archives all flow through the same upload path.
    if (item.kind !== "file") continue;
    const f = item.getAsFile();
    if (!f) continue;
    // Screenshot-tool images arrive with no filename; synthesise one so the
    // chip and the on-disk name aren't empty.
    const fallbackExt = f.type.startsWith("image/") ? ".png" : "";
    fileItems.push({ file: f, name: f.name || `pasted-${Date.now()}${fallbackExt}` });
  }
  if (fileItems.length === 0) return;
  e.preventDefault();
  for (const { file, name } of fileItems) void uploadFile(file, name);
}

function handleDrop(e: DragEvent) {
  if (props.readOnly) return;
  const files = e.dataTransfer?.files;
  if (!files || files.length === 0) return;
  e.preventDefault();
  for (const f of Array.from(files)) void uploadFile(f, f.name);
}

function handleFilePick(e: Event) {
  if (props.readOnly) return;
  const input = e.target as HTMLInputElement;
  if (!input.files) return;
  for (const f of Array.from(input.files)) void uploadFile(f, f.name);
  // Reset so picking the same file twice in a row still fires `change`.
  input.value = "";
}

// Attachments alone (no typed text) are a valid send — the composed message is
// just the refs. That only stays correct because useChatAttachments drops the
// list when `conversationId` changes; otherwise a brand-new conversation would
// inherit the previous one's chips and look sendable with an empty box.
const canSend = computed(
  () =>
    !!(inputText.value.trim() || attachments.value.length > 0) &&
    !props.readOnly &&
    props.isConnected &&
    !isUploading(),
);

function closeSendMenu() {
  sendMenuOpen.value = false;
  menuView.value = "options";
}

onClickOutside(sendControlsRef, closeSendMenu);

type SendMode = "insert" | "queue";

// composeMessage turns the editor and its attachments into what goes over the
// wire, or null when there is nothing sendable.
function composeMessage(): { text: string; attachments?: MessageAttachment[] } | null {
  // A half-uploaded attachment would land in history with an unreachable path,
  // so canSend also gates on in-flight uploads.
  if (!canSend.value) return null;
  const text = inputText.value.trim();
  const refs = attachments.value
    .map((a) => a.ref)
    .filter(Boolean)
    .join(" ");
  // Attachment refs go after the typed prompt so the agent reads human intent
  // first and path references second — matching how `@path` tokens are written.
  const composed = text && refs ? `${text}\n\n${refs}` : text || refs;
  // `ref` goes into the message text (the path the agent opens); `path` goes
  // into the metadata, because that is the form the file-read API resolves.
  const messageAttachments = attachments.value
    .filter((a) => a.url)
    .map((a) => ({ name: a.name, mime: a.mime, path: a.path, url: a.url! }));
  return messageAttachments.length > 0
    ? { text: composed, attachments: messageAttachments }
    : { text: composed };
}

function clearComposer() {
  inputText.value = "";
  clearAfterSend();
  closeSendMenu();
}

function handleSend(mode: SendMode = defaultSendMode.value) {
  const message = composeMessage();
  if (!message) return;
  const insert = mode === "insert";
  if (message.attachments) {
    emit("send", message.text, message.attachments, insert || undefined);
  } else if (insert) {
    emit("send", message.text, undefined, true);
  } else {
    emit("send", message.text);
  }
  clearComposer();
}

// --- Send later -----------------------------------------------------------
// The menu's third mode. Relative ("in 30 minutes") covers the common case
// in two clicks; absolute ("at 09:00 tomorrow") covers the rest. Either way
// the server holds the message and sends it like a plain send when it is due.
type ScheduleKind = "relative" | "absolute";
const SCHEDULE_PRESETS: { amount: number; unit: ScheduleUnit }[] = [
  { amount: 5, unit: "minutes" },
  { amount: 15, unit: "minutes" },
  { amount: 30, unit: "minutes" },
  { amount: 1, unit: "hours" },
  { amount: 2, unit: "hours" },
  { amount: 1, unit: "days" },
];
const SCHEDULE_UNITS: ScheduleUnit[] = ["minutes", "hours", "days"];

const menuView = ref<"options" | "schedule">("options");
const scheduleKind = ref<ScheduleKind>("relative");
const relativeAmount = ref<number | "">(30);
const relativeUnit = ref<ScheduleUnit>("minutes");
const absoluteValue = ref("");
const now = useNow({ interval: 15_000 });

const scheduledAt = computed<Date | null>(() =>
  scheduleKind.value === "relative"
    ? relativeSendAt(Number(relativeAmount.value), relativeUnit.value, now.value)
    : parseLocalDateTime(absoluteValue.value),
);
const scheduleValid = computed(() => isValidSendAt(scheduledAt.value, now.value));
const schedulePreview = computed(() => {
  const at = scheduledAt.value;
  if (!scheduleValid.value || !at) return t("chat.schedule.invalid");
  return t("chat.schedule.preview", {
    time: formatSendAt(at, now.value, locale.value, timeHour12.value),
    relative: formatUntil(at, now.value, locale.value),
  });
});

function presetLabel(p: { amount: number; unit: ScheduleUnit }): string {
  return t(`chat.schedule.preset.${p.unit}`, { n: p.amount });
}

function presetActive(p: { amount: number; unit: ScheduleUnit }): boolean {
  return (
    scheduleKind.value === "relative" &&
    Number(relativeAmount.value) === p.amount &&
    relativeUnit.value === p.unit
  );
}

function pickPreset(p: { amount: number; unit: ScheduleUnit }) {
  scheduleKind.value = "relative";
  relativeAmount.value = p.amount;
  relativeUnit.value = p.unit;
}

function openSchedule() {
  if (!absoluteValue.value || !isValidSendAt(parseLocalDateTime(absoluteValue.value), now.value)) {
    absoluteValue.value = toLocalDateTimeInput(defaultAbsoluteSendAt(now.value));
  }
  menuView.value = "schedule";
}

function handleSchedule() {
  const at = scheduledAt.value;
  if (!isValidSendAt(at, new Date())) return;
  const message = composeMessage();
  if (!message) return;
  emit("schedule", message.text, message.attachments, at);
  clearComposer();
}

const { registry, ensureLoaded: ensureModelRegistry } = useModelRegistry();
// Fire-and-forget: a failed fetch leaves canInsert on its optimistic default.
void ensureModelRegistry().catch(() => {});

// Insert-by-default only where the transport can take input mid-turn (Agent
// SDK / app-server). A CLI transport runs one process per turn, so a message
// sent while it works can only wait for it to finish — offering "insert" there
// would promise something the server silently turns into a queue. Unknown
// capabilities (registry still loading, provider not in it) keep the insert
// default.
// An idle conversation has no turn to steer into, so it always sends plainly.
const canInsert = computed(
  () => providerCapabilities(registry.value, props.provider)?.supports_steering ?? true,
);
const defaultSendMode = computed<SendMode>(() =>
  props.isThinking && canInsert.value ? "insert" : "queue",
);
const sendLabel = computed(() => {
  const shortcut = submitShortcut.value;
  if (!props.isThinking) return t("chat.sendShortcut", { shortcut });
  return canInsert.value
    ? t("chat.insertShortcut", { shortcut })
    : t("chat.queueShortcut", { shortcut });
});

// "Send now" is the disc's own action, repeated in the menu so the three modes
// read as one choice. A CLI transport can't take input mid-turn, so while it
// works there is no "now" to offer — only after completion or later.
const canSendNow = computed(() => !props.isThinking || canInsert.value);
const sendNowLabel = computed(() => (props.isThinking ? t("chat.insertNow") : t("chat.sendNow")));
const sendNowHint = computed(() =>
  props.isThinking ? t("chat.insertNowHint") : t("chat.sendNowHint"),
);

function handleKeydown(e: KeyboardEvent) {
  if (props.readOnly) return;
  // IME composition: a CJK commit gesture also raises Enter, but we never want
  // to act on it. The DOM exposes this via `isComposing` and the legacy keyCode
  // 229; check both for older browsers that only set one.
  if (e.isComposing || e.keyCode === 229) return;

  // Primary modifier+Enter submits: Ctrl on Windows/Linux, Cmd on macOS. Plain
  // Enter is left alone so multi-line prompts compose naturally — the Send
  // button remains the discoverable path.
  if (e.key === "Enter" && isPrimaryModifier(e)) {
    e.preventDefault();
    handleSend();
  }
}

defineExpose({ focus: focusInput });
</script>

<template>
  <TooltipProvider :delay-duration="200">
    <div
      class="shrink-0 bg-background/95 px-[9px] pb-2 pt-[7px] backdrop-blur md:px-[clamp(26px,5vw,72px)] md:pb-[18px] md:pt-2"
    >
      <ChatAttachmentStrip
        :attachments="attachments"
        :uploads="uploads"
        @cancel-upload="cancelUpload"
        @dismiss-upload="dismissUpload"
        @remove-attachment="removeAttachment"
      />
      <p
        v-if="showImageInputWarning"
        class="mx-auto mb-1.5 max-w-[760px] px-1 text-[11px] text-amber-700 dark:text-amber-400"
        data-testid="image-input-warning"
      >
        {{ t("chat.imageInputUnsupported") }}
      </p>

      <!-- Composer pill (UI design): rounded full with a quiet, neutral
           hairline border. Connection loss is expressed through the disabled
           controls instead of red/black border state changes.
           Always stacked: the textarea owns the pill's full width and every
           button sits on its own row underneath. A side-by-side row reserved
           the button column for the whole height of the box, so a wrapping
           message was squeezed into the left half with dead space above the
           send button. -->
      <div
        class="composer-pill @container relative mx-auto flex min-h-[99px] max-w-[760px] flex-col items-stretch rounded-xl border border-[#eeeeee] bg-card shadow-none transition-[border-color,box-shadow] focus-within:border-[#b8b8b8] focus-within:shadow-[0_0_0_2px_rgba(25,25,25,0.035)] max-md:rounded-[20px] max-md:border-black/[0.07] max-md:shadow-[0_2px_14px_rgba(20,20,30,0.06)] max-md:focus-within:border-black/[0.12] dark:border-border"
        data-cursor-surface="text"
        :class="{ 'bg-muted': !isConnected }"
        @drop.prevent="handleDrop"
        @dragover.prevent
      >
        <!-- Hidden file picker, opened by the paperclip button. No `accept`
             filter — the backend takes any MIME, so the picker should match.
             Drag-and-drop and paste route through the same upload helper. -->
        <input
          ref="fileInputRef"
          type="file"
          multiple
          class="hidden"
          data-testid="file-input"
          @change="handleFilePick"
        />
        <!-- Textarea — flush, auto-grows. Single-row line-height matches the
             box height so the placeholder sits centered without any
             padding-vs-line-height arithmetic. -->
        <Textarea
          ref="inputRef"
          v-model="inputText"
          :placeholder="
            props.readOnly
              ? t('chat.inputReadonly')
              : !isConnected
                ? t('chat.inputReconnecting')
                : isThinking
                  ? t('chat.inputQueue')
                  : inputPlaceholder
          "
          :disabled="props.readOnly || !isConnected"
          :rows="1"
          class="composer-input max-h-40 min-h-9 w-full resize-none border-0 bg-transparent px-3 pb-1 pt-3 text-sm text-[#191919] shadow-none focus-visible:ring-0 placeholder:text-[#bfbfbf] placeholder:opacity-100 dark:text-foreground dark:placeholder:text-muted-foreground"
          @paste="handlePaste"
          @keydown="handleKeydown"
        />

        <!-- Controls row: the conversation controls slot claims the left, then
             attach, then cancel (when busy), then the split send. The paperclip
             sits next to the send button rather than off in the opposite corner
             so the whole control cluster reads as one group.

             The slot used to render as its own strip above the pill. Folding it
             into this row is what removes that strip's height — the model and
             context readouts share the line the send button already needs.

             The model selector must stay on the send line. Now that account,
             model and thinking level share one compact trigger, the row can
             shrink that trigger instead of wrapping the send cluster beneath
             it. The slot still renders its contents directly into this row so
             the selector participates in flex sizing rather than being hidden
             inside a zero-width wrapper. -->
        <div
          class="mt-auto flex min-h-10 items-center gap-2 pb-2 pl-3 pr-2.5 @max-sm:gap-1.5"
          data-testid="composer-controls"
        >
          <!-- Iris keeps upload at the leading edge of the action row. That
               makes the composer scan as “add context → choose model → send”
               and leaves the primary action anchored at the far right. -->
          <Tooltip>
            <TooltipTrigger as-child>
              <Button
                type="button"
                variant="ghost"
                size="icon"
                class="-ml-1 size-7 shrink-0 rounded-lg text-[#4e4e4e] hover:text-foreground dark:text-muted-foreground"
                :aria-label="t('chat.attachFile')"
                data-testid="attach-btn"
                :disabled="!props.conversationId || props.readOnly"
                @click="fileInputRef?.click()"
              >
                <Paperclip :size="20" :stroke-width="1.4" />
              </Button>
            </TooltipTrigger>
            <TooltipContent side="top">{{ t("chat.attachFile") }}</TooltipContent>
          </Tooltip>

          <span class="min-w-0 flex-1" aria-hidden="true" />
          <slot name="controls" />

          <!-- ml-auto keeps the fixed-size action cluster at the right edge while
               the model label yields width through truncation when necessary. -->
          <div class="ml-auto flex shrink-0 items-center gap-2" data-testid="composer-send-cluster">
            <!-- The disc sends now: into the live task when the transport can
               take it, otherwise plainly (or, on a busy CLI transport, after
               the current task). The chevron opens the three send modes —
               now, after the current task, or at a chosen time. -->
            <Button
              v-if="isThinking"
              variant="destructive"
              size="sm"
              data-testid="cancel-btn"
              class="h-8 shrink-0 rounded-full px-3 text-xs"
              @click="emit('cancel')"
            >
              {{ t("chat.cancel") }}
            </Button>
            <div
              ref="sendControlsRef"
              class="group/send relative flex shrink-0 items-center gap-1"
              :class="{ invisible: !props.turnStatusKnown }"
              data-testid="send-controls"
              @keydown.esc="closeSendMenu"
            >
              <!-- The other modes are the rarer path, so they get the quiet
                   ghost chevron and the primary action keeps the filled disc.
                   The disc carries no visible label, so `aria-label` is what a
                   screen reader announces — it must stay in sync with the
                   tooltip's send/insert wording. -->
              <Tooltip>
                <TooltipTrigger as-child>
                  <Button
                    type="button"
                    class="size-7 shrink-0 cursor-pointer rounded-lg p-0 text-muted-foreground transition-none hover:text-foreground"
                    variant="ghost"
                    :aria-label="t('chat.sendOptions')"
                    aria-haspopup="menu"
                    :aria-expanded="sendMenuOpen"
                    data-testid="send-options-btn"
                    :disabled="!canSend"
                    @click="sendMenuOpen ? closeSendMenu() : (sendMenuOpen = true)"
                  >
                    <ChevronUp :size="15" />
                  </Button>
                </TooltipTrigger>
                <TooltipContent side="top">{{ t("chat.sendOptions") }}</TooltipContent>
              </Tooltip>

              <Tooltip>
                <TooltipTrigger as-child>
                  <Button
                    class="send-btn size-7 shrink-0 cursor-pointer rounded-full p-0 transition-none group-hover/send:bg-primary/90"
                    variant="default"
                    data-testid="send-btn"
                    :aria-label="sendLabel"
                    :disabled="!canSend"
                    @click="handleSend()"
                  >
                    <ArrowUp :size="15" :stroke-width="2.2" />
                  </Button>
                </TooltipTrigger>
                <TooltipContent side="top">{{ sendLabel }}</TooltipContent>
              </Tooltip>

              <div
                v-if="sendMenuOpen"
                role="menu"
                data-testid="send-options-menu"
                class="absolute bottom-full right-0 z-50 mb-2 w-72 max-w-[calc(100vw-2rem)] rounded-xl border border-border bg-popover p-1.5 text-popover-foreground shadow-xl"
              >
                <template v-if="menuView === 'options'">
                  <button
                    v-if="canSendNow"
                    type="button"
                    role="menuitem"
                    data-testid="send-now-btn"
                    class="send-menu-item"
                    :disabled="!canSend"
                    @click="handleSend(isThinking ? 'insert' : 'queue')"
                  >
                    <ArrowUp :size="16" class="mt-0.5 shrink-0" />
                    <span class="min-w-0">
                      <span class="block text-sm font-semibold">{{ sendNowLabel }}</span>
                      <span class="mt-0.5 block text-xs leading-4 text-muted-foreground">{{
                        sendNowHint
                      }}</span>
                    </span>
                  </button>
                  <button
                    type="button"
                    role="menuitem"
                    data-testid="queue-after-current-btn"
                    class="send-menu-item"
                    :disabled="!canSend || !isThinking"
                    @click="handleSend('queue')"
                  >
                    <ListEnd :size="16" class="mt-0.5 shrink-0" />
                    <span class="min-w-0">
                      <span class="block text-sm font-semibold">{{
                        t("chat.sendAfterCurrent")
                      }}</span>
                      <span class="mt-0.5 block text-xs leading-4 text-muted-foreground">{{
                        isThinking ? t("chat.sendAfterCurrentHint") : t("chat.sendAfterCurrentIdle")
                      }}</span>
                    </span>
                  </button>
                  <button
                    type="button"
                    role="menuitem"
                    data-testid="schedule-send-btn"
                    class="send-menu-item"
                    :disabled="!canSend"
                    @click="openSchedule"
                  >
                    <Clock :size="16" class="mt-0.5 shrink-0" />
                    <span class="min-w-0">
                      <span class="block text-sm font-semibold">{{ t("chat.schedule.menu") }}</span>
                      <span class="mt-0.5 block text-xs leading-4 text-muted-foreground">{{
                        t("chat.schedule.menuHint")
                      }}</span>
                    </span>
                  </button>
                </template>

                <div
                  v-else
                  class="flex flex-col gap-2.5 p-1.5"
                  data-testid="schedule-panel"
                  @keydown.enter.prevent="handleSchedule"
                >
                  <div class="flex items-center gap-1">
                    <button
                      type="button"
                      class="-ml-1 flex size-6 items-center justify-center rounded-md text-muted-foreground hover:bg-accent hover:text-foreground"
                      :aria-label="t('chat.schedule.back')"
                      data-testid="schedule-back-btn"
                      @click="menuView = 'options'"
                    >
                      <ChevronLeft :size="15" />
                    </button>
                    <span class="text-sm font-semibold">{{ t("chat.schedule.title") }}</span>
                  </div>

                  <div
                    role="tablist"
                    class="grid grid-cols-2 gap-0.5 rounded-lg bg-muted p-0.5 text-xs font-medium"
                  >
                    <button
                      v-for="kind in ['relative', 'absolute'] as const"
                      :key="kind"
                      type="button"
                      role="tab"
                      :aria-selected="scheduleKind === kind"
                      :data-testid="`schedule-kind-${kind}`"
                      class="rounded-md px-2 py-1 transition-colors"
                      :class="
                        scheduleKind === kind
                          ? 'bg-background text-foreground shadow-sm'
                          : 'text-muted-foreground hover:text-foreground'
                      "
                      @click="scheduleKind = kind"
                    >
                      {{ t(`chat.schedule.${kind}`) }}
                    </button>
                  </div>

                  <template v-if="scheduleKind === 'relative'">
                    <div class="grid grid-cols-3 gap-1.5">
                      <button
                        v-for="preset in SCHEDULE_PRESETS"
                        :key="`${preset.amount}-${preset.unit}`"
                        type="button"
                        data-testid="schedule-preset"
                        class="rounded-md border px-2 py-1 text-xs transition-colors"
                        :class="
                          presetActive(preset)
                            ? 'border-foreground/40 bg-accent text-foreground'
                            : 'border-border text-muted-foreground hover:bg-accent hover:text-foreground'
                        "
                        @click="pickPreset(preset)"
                      >
                        {{ presetLabel(preset) }}
                      </button>
                    </div>
                    <label class="flex items-center gap-2 text-xs text-muted-foreground">
                      <span class="shrink-0">{{ t("chat.schedule.after") }}</span>
                      <input
                        v-model.number="relativeAmount"
                        type="number"
                        min="1"
                        step="1"
                        inputmode="numeric"
                        data-testid="schedule-amount"
                        class="h-8 w-16 min-w-0 rounded-md border border-border bg-background px-2 text-sm text-foreground outline-none focus-visible:ring-2 focus-visible:ring-ring"
                      />
                      <select
                        v-model="relativeUnit"
                        data-testid="schedule-unit"
                        :aria-label="t('chat.schedule.unit')"
                        class="h-8 min-w-0 flex-1 rounded-md border border-border bg-background px-2 text-sm text-foreground outline-none focus-visible:ring-2 focus-visible:ring-ring"
                      >
                        <option v-for="unit in SCHEDULE_UNITS" :key="unit" :value="unit">
                          {{ t(`chat.schedule.units.${unit}`) }}
                        </option>
                      </select>
                    </label>
                  </template>
                  <input
                    v-else
                    v-model="absoluteValue"
                    type="datetime-local"
                    :min="toLocalDateTimeInput(now)"
                    :aria-label="t('chat.schedule.absolute')"
                    data-testid="schedule-datetime"
                    class="h-8 w-full rounded-md border border-border bg-background px-2 text-sm text-foreground outline-none focus-visible:ring-2 focus-visible:ring-ring"
                  />

                  <p
                    class="text-xs"
                    :class="scheduleValid ? 'text-muted-foreground' : 'text-destructive'"
                    data-testid="schedule-preview"
                  >
                    {{ schedulePreview }}
                  </p>
                  <Button
                    type="button"
                    size="sm"
                    class="h-8 w-full"
                    data-testid="schedule-confirm-btn"
                    :disabled="!canSend || !scheduleValid"
                    @click="handleSchedule"
                  >
                    <Clock :size="14" />
                    {{ t("chat.schedule.confirm") }}
                  </Button>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
  </TooltipProvider>
</template>

<style scoped>
.send-menu-item {
  display: flex;
  width: 100%;
  align-items: flex-start;
  gap: 0.625rem;
  border-radius: 0.5rem;
  padding: 0.625rem 0.75rem;
  text-align: left;
  outline: none;
  transition: background-color 150ms;
}
.send-menu-item:hover:not(:disabled),
.send-menu-item:focus-visible {
  background: var(--accent);
}
.send-menu-item:focus-visible {
  box-shadow: 0 0 0 2px var(--ring);
}
.send-menu-item:disabled {
  cursor: not-allowed;
  opacity: 0.5;
}

/* Single-line state: the textarea is exactly 36px tall and the line-height
 * matches the box, so the cursor / placeholder / first line of typed text all
 * sit on the typographic center of the pill. Mobile browsers were previously
 * rendering the placeholder against the line-box baseline of an oversized
 * field-sizing-content box, which made the prompt look top-aligned. */
.composer-input:placeholder-shown,
.composer-input:not(:focus):placeholder-shown {
  height: 2.25rem;
  min-height: 2.25rem;
  line-height: 2.25rem;
}
.composer-input::placeholder {
  line-height: 2.25rem;
}

/* When the user starts typing, drop back to a normal 24px line so a multi-line
 * message wraps with comfortable spacing. The 6px top/bottom padding then keeps
 * the first line vertically centered inside the 36px min-height box. */
.composer-input:not(:placeholder-shown) {
  line-height: 1.5rem;
  padding-top: 0.375rem;
  padding-bottom: 0.375rem;
}

/* Desktop: a two-line writing surface with the prompt at its top-left, as in
 * the reference (54px min, 21px lines). The single-line centring above is for
 * the narrow layout, where the composer has to stay short. */
@media (min-width: 768px) {
  .composer-input,
  .composer-input:placeholder-shown,
  .composer-input:not(:focus):placeholder-shown,
  .composer-input:not(:placeholder-shown) {
    height: auto;
    min-height: 58px;
    line-height: 21px;
    padding-top: 12px;
    padding-bottom: 4px;
  }
  .composer-input::placeholder {
    line-height: 21px;
  }
}
</style>
