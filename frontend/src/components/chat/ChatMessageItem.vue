<script setup lang="ts">
import { computed, ref } from "vue";
import { ArrowDownToLine, Sparkles, Check, ChevronDown, Copy } from "lucide-vue-next";
import { useI18n } from "vue-i18n";
import { renderMarkdown } from "@/composables/useMarkdown";
import { vCodeCopy } from "@/directives/codeCopy";
import { vExternalLinks } from "@/directives/externalLinks";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { copyText } from "@/lib/clipboard";
import { formatSentTime } from "@/lib/format";
import { useTimeFormat } from "@/composables/useTimeFormat";
import ImageLightbox from "@/components/ImageLightbox.vue";
import FileIcon from "@/components/FileIcon.vue";
import "@/assets/markdown.css";

const { t } = useI18n();

const props = withDefaults(
  defineProps<{
    msg: {
      role: string;
      content: string;
      activityType?: string;
      // Persisted DB message id once the row has been claimed by a result
      // event (or restored from REST/backfill). Absence means the row is
      // still in-flight — used to drop the warm pill chrome on a thinking
      // row once the model's done with it.
      id?: string;
      // True when this activity belongs to a sub-agent (Task / Agent tool).
      // Renders the chip on an indented, dimmer track so the worker's events
      // are visually obviously *inside* the parent's tool call instead of
      // looking like duplicates of the parent's own stream.
      subagent?: boolean;
      // Per-turn usage attached to assistant rows. The short string is
      // the visible token-cost chip below the bubble; the detail is the
      // multi-line hover tooltip (un-abbreviated names + 5m/1h cache
      // TTL split). Absent on every other role.
      usage?: { short: string; detail: string };
      attachments?: Array<{ name: string; mime: string; path: string; url: string }>;
      sender?: { platform: string; id?: string; name?: string };
      // ISO-8601 timestamp of when the message was sent. Drives the
      // small muted "sent at" hint shown next to the username on
      // user / assistant bubbles. Absent on activity / error rows
      // (and on optimistic rows that haven't been stamped yet) — the
      // template skips the hint when undefined.
      created_at?: string;
      // True when the broad-cancel button aborted the turn this user
      // prompt triggered. Swaps the bubble to a muted variant + a
      // small "Cancelled" badge so the row visually reads as "this
      // didn't complete" instead of an ordinary sent message.
      cancelled?: boolean;
      wakeup?: boolean;
    };
    collapsed: boolean;
    // Display labels for the two parties in the conversation. Defaults
    // preserve the historical "You" / "Claude" framing so callers that
    // don't care can mount the component unchanged; ChatPage passes the
    // signed-in human's "You" and the active agent's display name so the
    // transcript reads as a conversation between *that* user and *that*
    // agent rather than the generic Claude branding.
    userLabel?: string;
    assistantLabel?: string;
    defaultToolCollapsed?: boolean;
    // Set by the transcript on every row but the newest: the browser may then
    // skip layout/paint for the row while it is off screen (see
    // .chat-row-deferrable). Live rows never opt in regardless.
    deferOffscreen?: boolean;
  }>(),
  {
    userLabel: "You",
    assistantLabel: "Claude",
    defaultToolCollapsed: false,
    deferOffscreen: false,
  },
);

const emit = defineEmits<{
  "toggle-thinking": [];
}>();

// A thinking row is "live" until the result event stamps it with a
// persisted DB id (or until REST/backfill restores it pre-stamped).
// The summary line reads "thinking" with a pulse while live and "thought
// process" once claimed.
const isLiveThinking = computed(() => props.msg.activityType === "thinking" && !props.msg.id);

// A streaming row changes size every frame; skipping its rendering while it
// is off screen would only make its placeholder height jump around.
const deferClass = computed(() =>
  props.deferOffscreen && props.msg.activityType !== "stream" && !isLiveThinking.value
    ? "chat-row-deferrable"
    : undefined,
);

// For tool activity: split the first line off as the header (e.g. "[Read]")
// and keep the remainder as detail. UI design's tool card highlights the
// header and dims the detail; this matches the editorial "chip" pattern.
const toolHead = computed(() => props.msg.content.split("\n")[0]);
const toolDetail = computed(() => {
  const idx = props.msg.content.indexOf("\n");
  return idx >= 0 ? props.msg.content.slice(idx + 1).trim() : "";
});
const toolCollapsed = ref(props.defaultToolCollapsed);

// Small muted "sent at" hint shown after the username on user / assistant
// bubbles. Date order and separators come from the browser locale via
// Intl.DateTimeFormat(undefined, …); the clock (12h vs 24h) is governed
// by the user's Appearance setting, since browsers can't see macOS's
// "24-hour time" toggle. Returns "" when there's no created_at to format
// — the template uses that to skip the span entirely.
const { timeHour12 } = useTimeFormat();
const sentTimeLabel = computed(() =>
  props.msg.created_at ? formatSentTime(props.msg.created_at, Date.now(), timeHour12.value) : "",
);

// The composer appends each attachment's absolute path to the outgoing text
// so the agent can open the file (ChatInput.handleSend). That path is part of
// the prompt and stays in the stored message, but it is noise in the bubble:
// the attachment strip right below already shows the same files by name, with
// a preview. So the trailing block of refs is dropped at render time.
//
// The match is exact rather than shape-based. An attachment's `ref` is its
// `path` resolved against the owner's home, which the browser never sees — but
// `ref` always ends with `/<path>`, so the tail of the content is peeled one
// whitespace-separated token at a time and each must end with the
// corresponding attachment's path, in the order the composer wrote them. Any
// mismatch leaves the content completely untouched: a message whose text
// merely happens to end in something path-shaped is not ours to edit.
function stripAttachmentRefs(content: string, paths: string[]): string {
  let end = content.length;
  for (let i = paths.length - 1; i >= 0; i--) {
    const head = content.slice(0, end).replace(/\s+$/, "");
    // Stored upload names are sanitized to [A-Za-z0-9._-], so no ref can
    // contain whitespace and splitting on it is unambiguous.
    const start =
      Math.max(head.lastIndexOf(" "), head.lastIndexOf("\n"), head.lastIndexOf("\t")) + 1;
    const token = head.slice(start);
    if (token !== paths[i] && !token.endsWith("/" + paths[i])) return content;
    end = start;
  }
  return content.slice(0, end).replace(/\s+$/, "");
}

const userContent = computed(() => {
  const paths = props.msg.attachments?.map((a) => a.path) ?? [];
  if (!props.msg.content || paths.length === 0) return props.msg.content;
  return stripAttachmentRefs(props.msg.content, paths);
});

// Clicking an image attachment used to open a new tab, which lost the chat
// behind it and offered no zoom — the usual reason to click a screenshot in
// the first place. It now opens in place; the viewer lives per message row
// because only one can be open at a time anyway.
const lightbox = ref<{ src: string; alt: string } | null>(null);

function openImage(attachment: { url: string; name: string }) {
  lightbox.value = { src: attachment.url, alt: attachment.name };
}

// The result card's subtitle names what kind of file the agent produced, the
// way the Iris reference labels a report "文档". Extension is all the browser
// knows about an attachment, so that is what decides the bucket.
const ATTACHMENT_KINDS: Record<string, string> = {
  md: "document",
  txt: "document",
  doc: "document",
  docx: "document",
  pdf: "document",
  xls: "sheet",
  xlsx: "sheet",
  csv: "sheet",
  tsv: "sheet",
  ppt: "slides",
  pptx: "slides",
  key: "slides",
  zip: "archive",
  gz: "archive",
  tgz: "archive",
  "7z": "archive",
  rar: "archive",
  html: "webpage",
  htm: "webpage",
};

function attachmentKind(name: string): string {
  const ext = name.includes(".") ? name.slice(name.lastIndexOf(".") + 1).toLowerCase() : "";
  const kind = t(`chat.attachmentKind.${ATTACHMENT_KINDS[ext] ?? "file"}`);
  return ext ? `${kind} · ${ext.toUpperCase()}` : kind;
}

const messageLabel = computed(() => {
  const senderLabel = props.msg.sender?.name || props.msg.sender?.id;
  if (senderLabel) return senderLabel;
  return props.msg.role === "assistant" ? props.assistantLabel : props.userLabel;
});

// Per-instance "just copied" state so the button can swap to a check icon
// briefly without affecting sibling messages. 1500ms matches the typical
// affordance window — long enough to register, short enough that the
// button doesn't sit in the confirmed state and confuse a second copy.
const copied = ref(false);
let copyResetTimer: ReturnType<typeof setTimeout> | null = null;

async function copyContent() {
  // Best-effort: the confirmation flips regardless of outcome, matching
  // the previous inline implementation which swallowed copy failures —
  // the user can still select and copy manually.
  await copyText(props.msg.content);
  copied.value = true;
  if (copyResetTimer) clearTimeout(copyResetTimer);
  copyResetTimer = setTimeout(() => {
    copied.value = false;
  }, 1500);
}
</script>

<template>
  <!-- Activity messages -->
  <!-- Sub-agent (Task / Agent) events get an indented, dimmer track with a
       leading rule so the worker's output is visually nested inside the
       parent's tool call instead of looking like a duplicate of the parent's
       own stream. -->
  <div
    v-if="msg.role === 'activity'"
    class="text-[13px]"
    :class="[
      deferClass,
      msg.activityType === 'thinking' ? 'mb-[19px]' : 'mb-2',
      msg.subagent ? 'subagent-track pl-4 border-l-2 border-[var(--edge-soft)] opacity-80' : '',
    ]"
    :data-subagent="msg.subagent ? 'true' : undefined"
  >
    <!-- Thinking — collapsible warm pill, ochre accent.
         Rendered as markdown live so headings/lists/code in extended-
         thinking output look right *while* it streams, instead of
         flipping from <pre> plain text to formatted prose only after
         the model finishes. markdown-it tolerates partial input
         (an unclosed ``` just reads as inline so far), so streaming
         deltas never produce a parse error. -->
    <!-- Iris renders a finished reasoning pass as one quiet summary line over
         a hairline ("用时 48 秒 ⌄"); the prose itself only unfolds on demand.
         While the pass is still live the same line reads "思考中" with a
         pulse, so the row doesn't change shape when the result lands. -->
    <div
      v-if="msg.activityType === 'thinking'"
      class="thinking-entry cursor-pointer"
      :data-live="isLiveThinking ? 'true' : undefined"
      @click="emit('toggle-thinking')"
    >
      <div
        class="thinking-summary flex h-[42px] items-center gap-[5px] border-b border-[#e5e8ef] text-[13px] text-[#83848a] transition-colors hover:text-foreground dark:border-border dark:text-muted-foreground"
        :aria-expanded="!collapsed"
      >
        <span
          v-if="isLiveThinking"
          class="inline-block size-1.5 rounded-full bg-[#6963db] animate-pulse"
          aria-hidden="true"
        />
        <span>{{ isLiveThinking ? t("chat.thinking") : t("chat.thought") }}</span>
        <ChevronDown
          class="toggle-hint size-[15px] shrink-0 transition-transform"
          :class="collapsed ? '' : 'rotate-180'"
          aria-hidden="true"
        />
      </div>
      <div
        v-if="!collapsed"
        v-mermaid
        v-code-copy="{ copy: t('chat.copyCode'), copied: t('chat.copied') }"
        v-external-links
        class="thinking-text markdown-body chat-prose pt-3 text-[13px] leading-[1.7] text-[#83848a] dark:text-muted-foreground"
        v-html="renderMarkdown(msg.content, { cache: !isLiveThinking })"
      ></div>
    </div>
    <!-- Tool — single-row chip with spark icon + name + check; detail
         indented below in muted mono. UI design uses a card-style chip. -->
    <div
      v-else-if="msg.activityType === 'tool'"
      class="rounded-md border border-border bg-card px-3 py-2"
    >
      <button
        type="button"
        class="tool-header flex w-full items-center gap-2 text-left text-xs"
        :class="toolDetail ? 'cursor-pointer' : 'cursor-default'"
        @click="toolDetail && (toolCollapsed = !toolCollapsed)"
      >
        <Sparkles class="size-3 shrink-0" :style="{ color: 'var(--primary)' }" />
        <span class="font-semibold text-foreground">{{ toolHead }}</span>
        <span class="flex-1" />
        <span v-if="toolDetail" class="text-[11px] font-normal text-muted-foreground">{{
          toolCollapsed ? "+" : "-"
        }}</span>
        <Check class="size-3 shrink-0" :style="{ color: 'oklch(0.62 0.18 150)' }" />
      </button>
      <pre
        v-if="toolDetail && !toolCollapsed"
        class="tool-detail mt-1 mb-0 text-[11px] leading-[1.45] text-[var(--ink-3)] whitespace-pre-wrap break-words"
        >{{ toolDetail }}</pre
      >
    </div>
    <!-- Info — muted chip matching the .usage-entry / model banner styling
         so the workdir hint, context-cleared divider, and other notices
         all read as the same class of metadata. `whitespace-pre-wrap`
         preserves newlines in multi-line notices while single-line uses
         render unchanged. The
         pill flips from inline to block once a newline is present so
         multi-line content gets a sensible width instead of laddering
         off the right edge. -->
    <div
      v-else-if="msg.activityType === 'info'"
      class="info-entry mb-3 rounded text-[12px] leading-5 text-[#83848a] whitespace-pre-wrap break-words dark:text-muted-foreground"
      :class="msg.content.includes('\n') ? 'block' : 'inline-block'"
    >
      {{ msg.content }}
    </div>
    <!-- Model banner — same chip styling the usage chip uses below.
         A metadata header for the conversation, not part of the prose;
         lives at the top of the chat next to the workdir hint. -->
    <div
      v-else-if="msg.activityType === 'model'"
      class="usage-entry inline-block bg-[var(--paper-alt)] text-muted-foreground rounded px-2.5 py-1 text-[11px] font-mono"
    >
      {{ msg.content }}
    </div>
    <!-- Stream — left ruled, markdown-rendered live so the in-flight
         reply already shows headings / lists / code blocks the same
         way the final assistant bubble will, instead of plain text
         that snaps to markdown only after the result event. Body
         sizing matches the finalised assistant turn so the row
         doesn't visibly re-flow when the result event lands and the
         stream chip is swapped for the persisted assistant message;
         the indigo left rule is what marks it as still in flight. -->
    <div
      v-else
      v-mermaid
      v-code-copy="{ copy: t('chat.copyCode'), copied: t('chat.copied') }"
      v-external-links
      class="stream-text markdown-body chat-prose mb-5 text-[14px] leading-[1.72] text-[#292a2f] dark:text-[var(--ink-2)]"
      v-html="renderMarkdown(msg.content, { cache: false })"
    ></div>
  </div>

  <!-- Regular chat messages -->
  <!-- USER — right-aligned quiet paper bubble. Messages stay
       fully expanded while the 78% container caps line length; long
       unbroken text wraps inside that boundary instead of creating an
       inner scrollbar. UI design sets borderTopRightRadius: 4px to mark
       "this is from you" with a corner cue similar to iMessage. When the
       user broad-cancelled the turn this prompt triggered, the bubble
       swaps to a muted paper-alt variant with a dashed border + a small
       Cancelled badge so the row reads as "this turn didn't complete"
       without disappearing. -->
  <div v-else-if="msg.role === 'user'" class="my-5 flex justify-end" :class="deferClass">
    <div class="flex max-w-[620px] flex-col items-end">
      <div class="sr-only mb-1 flex items-baseline gap-1.5">
        <span
          class="msg-label text-[10px] font-semibold uppercase tracking-[0.08em] text-muted-foreground"
        >
          {{ messageLabel }}
        </span>
        <span
          v-if="msg.cancelled"
          class="msg-cancelled-badge text-[10px] font-semibold uppercase tracking-[0.08em] rounded px-1.5 py-0.5 bg-destructive/10 text-destructive border border-destructive/30"
        >
          {{ t("chat.userMessageCancelled") }}
        </span>
      </div>
      <!-- The name label above is screen-reader only (the layout already says
           who is speaking), but the time is information, so it stays visible. -->
      <time
        v-if="sentTimeLabel"
        class="msg-sent-at mb-1 text-[11px] leading-4 text-muted-foreground/70 tabular-nums"
        :datetime="msg.created_at"
        >{{ sentTimeLabel }}</time
      >
      <div
        v-if="msg.cancelled"
        class="msg-content user-bubble user-bubble-cancelled px-3.5 py-2.5 rounded-xl text-[14px] leading-[1.55] break-words bg-[var(--paper-alt)] text-muted-foreground border border-dashed border-muted-foreground/40"
        style="border-top-right-radius: 4px"
      >
        <div
          v-if="userContent"
          v-external-links
          class="user-message-markdown markdown-body"
          v-html="renderMarkdown(userContent, { breaks: true })"
        ></div>
      </div>
      <div
        v-else
        class="msg-content user-bubble break-words rounded-[14px_14px_3px_14px] bg-[#efefef] px-4 py-3.5 text-[14px] leading-[1.6] text-[#28292d] [overflow-wrap:anywhere] dark:bg-[#242424] dark:text-[#f5f5f5]"
      >
        <div
          v-if="userContent"
          v-external-links
          class="user-message-markdown markdown-body"
          v-html="renderMarkdown(userContent, { breaks: true })"
        ></div>
        <div
          v-if="msg.attachments?.length"
          class="mt-3 grid justify-items-start gap-2"
          data-testid="message-attachments"
        >
          <template v-for="attachment in msg.attachments" :key="attachment.path">
            <button
              v-if="attachment.mime.startsWith('image/')"
              type="button"
              class="block w-fit max-w-full overflow-hidden rounded-lg bg-background/10 cursor-zoom-in"
              :aria-label="t('chat.imageViewer.open', { name: attachment.name })"
              @click="openImage(attachment)"
            >
              <img
                :src="attachment.url"
                :alt="attachment.name"
                loading="lazy"
                class="block max-w-full max-h-[28rem] object-contain"
                data-testid="message-attachment-image"
              />
            </button>
            <a
              v-else
              :href="attachment.url"
              target="_blank"
              rel="noopener noreferrer"
              class="block w-fit max-w-full overflow-hidden rounded-lg border border-[#d9d9db] bg-white/70 text-[#55575e] transition-colors hover:border-[#c4c4c7] dark:border-border dark:bg-background/10 dark:text-muted-foreground"
            >
              <span
                class="flex items-center gap-1.5 px-[9px] py-[7px] text-[11px] leading-[1.6] break-all"
                data-testid="message-attachment-file"
              >
                <FileIcon :is-dir="false" :file-name="attachment.name" :size="15" />
                {{ attachment.name }}
              </span>
            </a>
          </template>
        </div>
      </div>
    </div>
  </div>

  <!-- ASSISTANT — prose sits flat without decorative chrome. -->
  <div v-else-if="msg.role === 'assistant'" class="group/message relative mb-5" :class="deferClass">
    <div class="sr-only mb-2 flex items-baseline gap-2">
      <span
        class="msg-label text-[12px] font-semibold tracking-tight"
        :style="{ color: 'var(--foreground)' }"
        >{{ messageLabel }}</span
      >
      <span
        v-if="msg.wakeup"
        class="msg-wakeup-badge text-[10px] font-semibold uppercase tracking-[0.08em] rounded px-1.5 py-0.5 bg-[var(--paper-alt)] text-muted-foreground border border-muted-foreground/30"
        data-testid="assistant-wakeup-badge"
        :title="t('chat.backgroundWakeupHint')"
      >
        {{ t("chat.backgroundWakeup") }}
      </span>
    </div>
    <time
      v-if="sentTimeLabel"
      class="msg-sent-at mb-1 block text-[11px] leading-4 text-muted-foreground/70 tabular-nums"
      :datetime="msg.created_at"
      >{{ sentTimeLabel }}</time
    >
    <div
      v-mermaid
      v-code-copy="{ copy: t('chat.copyCode'), copied: t('chat.copied') }"
      v-external-links
      class="msg-content markdown-body chat-prose text-[14px] leading-[1.72] text-[#292a2f] dark:text-[var(--ink-2)]"
      v-html="renderMarkdown(msg.content)"
    ></div>
    <div
      v-if="msg.attachments?.length"
      class="mt-6 grid gap-3"
      data-testid="assistant-image-attachments"
    >
      <template v-for="attachment in msg.attachments" :key="attachment.path">
        <button
          v-if="attachment.mime.startsWith('image/')"
          type="button"
          class="block w-fit max-w-full overflow-hidden rounded-lg border border-border bg-[var(--paper-alt)] cursor-zoom-in"
          :aria-label="t('chat.imageViewer.open', { name: attachment.name })"
          @click="openImage(attachment)"
        >
          <img
            :src="attachment.url"
            :alt="attachment.name"
            loading="lazy"
            class="block max-w-full max-h-[36rem] object-contain"
            data-testid="assistant-attachment-image"
          />
        </button>
        <!-- A file the agent produced is the turn's deliverable, so it gets
             the Iris result card rather than an inline chip. -->
        <a
          v-else
          :href="attachment.url"
          target="_blank"
          rel="noopener noreferrer"
          class="assistant-attachment-card grid grid-cols-[38px_minmax(0,1fr)_auto] items-center gap-[11px] rounded-[13px] border border-[#dedee0] bg-card p-[13px] text-foreground no-underline transition-colors hover:border-[#c4c4c7] dark:border-border"
        >
          <span
            class="grid size-[38px] place-items-center rounded-[9px] bg-[#f0f0f1] text-[#565960] dark:bg-[var(--paper-alt)] dark:text-muted-foreground"
          >
            <FileIcon :is-dir="false" :file-name="attachment.name" :size="20" />
          </span>
          <span class="flex min-w-0 flex-col gap-[3px]">
            <strong
              class="truncate text-[13px] font-semibold"
              data-testid="assistant-attachment-file"
              >{{ attachment.name }}</strong
            >
            <span class="truncate text-[11px] text-[#7d8495] dark:text-muted-foreground">{{
              attachmentKind(attachment.name)
            }}</span>
          </span>
          <span
            class="flex items-center gap-1 pr-1 text-[11px] font-semibold text-[#323338] dark:text-foreground"
          >
            {{ t("chat.downloadAttachment") }}
            <ArrowDownToLine class="size-[15px]" aria-hidden="true" />
          </span>
        </a>
      </template>
    </div>
    <!-- Footer row: usage chip pinned left, copy button pinned right. Kept
         in a flex container so the copy affordance always sits at the
         bottom-right of the message even when no usage chip is present.
         The copy button writes `msg.content` (raw markdown) to the
         clipboard — what the user sees rendered is what they get back
         in source form, suitable for pasting into another markdown
         surface. -->
    <!-- The copy affordance hangs in the 20px gap under the turn instead of
         owning a row of its own: a reserved footer row doubled the space
         between consecutive turns, which is most of what made the transcript
         read looser than the reference. -->
    <TooltipProvider :delay-duration="200">
      <div v-if="msg.usage" class="mt-2 flex items-center">
        <Tooltip>
          <TooltipTrigger as-child>
            <div
              class="usage-entry inline-block bg-[var(--paper-alt)] text-muted-foreground rounded px-2.5 py-1 text-[11px] font-mono cursor-default"
            >
              {{ msg.usage.short }}
            </div>
          </TooltipTrigger>
          <TooltipContent v-if="msg.usage.detail" side="top">{{ msg.usage.detail }}</TooltipContent>
        </Tooltip>
      </div>
      <Tooltip>
        <TooltipTrigger as-child>
          <button
            type="button"
            class="copy-btn absolute right-0 top-full z-10 inline-flex h-5 items-center gap-1 rounded px-1.5 text-[11px] text-muted-foreground opacity-0 transition-[opacity,color,background-color] hover:bg-[var(--paper-alt)] hover:text-foreground focus-visible:opacity-100 group-hover/message:opacity-100"
            :aria-label="copied ? t('chat.copied') : t('chat.copyMarkdown')"
            @click="copyContent"
          >
            <Check v-if="copied" class="size-3.5" :style="{ color: 'oklch(0.62 0.18 150)' }" />
            <Copy v-else class="size-3.5" />
          </button>
        </TooltipTrigger>
        <TooltipContent side="top">{{
          copied ? t("chat.copiedBang") : t("chat.copyMarkdown")
        }}</TooltipContent>
      </Tooltip>
    </TooltipProvider>
  </div>

  <!-- WARNING -->
  <div v-else-if="msg.role === 'warning'" class="my-4" :class="deferClass">
    <div
      class="msg-label mb-1 text-[10px] font-semibold uppercase tracking-[0.08em] text-amber-700 dark:text-amber-400"
    >
      {{ t("chat.warning") }}
    </div>
    <div
      class="msg-content warning-content whitespace-pre-wrap break-words rounded-md border border-amber-300 bg-amber-50 px-3.5 py-2.5 text-[13px] leading-relaxed text-amber-800 dark:border-amber-800 dark:bg-amber-950/30 dark:text-amber-300"
    >
      {{ msg.content }}
    </div>
  </div>

  <!-- ERROR -->
  <div v-else-if="msg.role === 'error'" class="my-4" :class="deferClass">
    <div
      class="msg-label text-[10px] font-semibold uppercase tracking-[0.08em] mb-1 text-destructive"
    >
      {{ t("chat.error") }}
    </div>
    <div
      class="msg-content error-content px-3.5 py-2.5 rounded-md text-[13px] leading-relaxed whitespace-pre-wrap break-words bg-destructive/10 text-destructive border border-destructive/30"
    >
      {{ msg.content }}
    </div>
  </div>

  <ImageLightbox v-if="lightbox" :src="lightbox.src" :alt="lightbox.alt" @close="lightbox = null" />
</template>
