<script setup lang="ts">
// The composer pill: textarea, attachment affordances and send/cancel controls. Upload lifecycle lives in useChatAttachments and draft
// persistence in useChatDraft, so what remains here is the input itself and the
// keyboard contract that ties the three together.
import { ref, watch, nextTick, computed, toRef } from "vue";
import { onClickOutside } from "@vueuse/core";
import { Textarea } from "@/components/ui/textarea";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { ArrowUp, ChevronUp, ListEnd, Paperclip } from "lucide-vue-next";
import ChatAttachmentStrip from "./ChatAttachmentStrip.vue";
import { providerCapabilities, useModelRegistry } from "@/composables/useModelRegistry";
import { isImageMime, useChatAttachments } from "@/composables/useChatAttachments";
import { useChatDraft } from "@/composables/useChatDraft";
import { isPrimaryModifier, primaryModifierLabel } from "@/lib/primaryModifier";
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
  cancel: [];
  // Fired once we've stuffed recallText into the editor, so the parent can
  // clear its own copy and avoid re-stuffing on the next render.
  "recall-consumed": [];
  // Fired once an attachment finishes uploading — see useChatAttachments for
  // why the parent needs to know.
  uploaded: [];
}>();

const { t } = useI18n();
const submitShortcut = computed(() => `${primaryModifierLabel()}+Enter`);

// Every entry must teach something the UI doesn't already make obvious.
// Generic invitations ("What shall we build?") cost a rotation slot to tell
// the user what an empty input box already tells them.
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
const inputPlaceholderKey =
  INPUT_PLACEHOLDER_KEYS[Math.floor(Math.random() * INPUT_PLACEHOLDER_KEYS.length)];
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
} = useChatAttachments(toRef(props, "conversationId"), () => emit("uploaded"));

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
}

onClickOutside(sendControlsRef, closeSendMenu);

type SendMode = "insert" | "queue";

function handleSend(mode: SendMode = defaultSendMode.value) {
  // A half-uploaded attachment would land in history with an unreachable path,
  // so canSend also gates on in-flight uploads.
  if (!canSend.value) return;
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
  const insert = mode === "insert";
  if (messageAttachments.length > 0) {
    emit("send", composed, messageAttachments, insert || undefined);
  } else if (insert) {
    emit("send", composed, undefined, true);
  } else {
    emit("send", composed);
  }
  inputText.value = "";
  clearAfterSend();
  closeSendMenu();
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

watch(
  () => props.isThinking,
  (thinking) => {
    if (!thinking) closeSendMenu();
  },
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
        class="composer-pill @container relative mx-auto flex min-h-[99px] max-w-[760px] flex-col items-stretch rounded-xl border border-[#eeeeee] bg-card shadow-none transition-[border-color,box-shadow] focus-within:border-[#b8b8b8] focus-within:shadow-[0_0_0_2px_rgba(25,25,25,0.035)] dark:border-border"
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
            <!-- The primary half inserts into a live task by default. The
               up-arrow exposes the less common FIFO path for a message that
               should wait until the current task finishes. CLI transports
               can't insert, so there the disc queues and the chevron goes. -->
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
              <!-- The FIFO path is the rarer one, so it gets the quiet ghost
                   chevron and the primary action keeps the filled disc. The
                   disc carries no visible label, so `aria-label` is what a
                   screen reader announces — it must stay in sync with the
                   tooltip's send/insert wording. -->
              <Tooltip v-if="isThinking && canInsert">
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
                    @click="sendMenuOpen = !sendMenuOpen"
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
                v-if="sendMenuOpen && isThinking && canInsert"
                role="menu"
                data-testid="send-options-menu"
                class="absolute bottom-full right-0 z-50 mb-2 w-60 max-w-[calc(100vw-2rem)] rounded-xl border border-border bg-popover p-1.5 text-popover-foreground shadow-xl"
              >
                <button
                  type="button"
                  role="menuitem"
                  data-testid="queue-after-current-btn"
                  class="flex w-full items-start gap-2.5 rounded-lg px-3 py-2.5 text-left outline-none transition-colors hover:bg-accent focus-visible:bg-accent focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50"
                  :disabled="!canSend"
                  @click="handleSend('queue')"
                >
                  <ListEnd :size="16" class="mt-0.5 shrink-0" />
                  <span class="min-w-0">
                    <span class="block text-sm font-semibold">{{
                      t("chat.sendAfterCurrent")
                    }}</span>
                    <span class="mt-0.5 block text-xs leading-4 text-muted-foreground">{{
                      t("chat.sendAfterCurrentHint")
                    }}</span>
                  </span>
                </button>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
  </TooltipProvider>
</template>

<style scoped>
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
