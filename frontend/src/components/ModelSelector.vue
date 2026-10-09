<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from "vue";
import { onClickOutside } from "@vueuse/core";
import { Check, ChevronDown, ChevronRight } from "lucide-vue-next";
import { updateConversationModel } from "@/composables/apiConversations";
import { loadModelRegistry, useModelRegistry } from "@/composables/useModelRegistry";
import type { Conversation, ThinkLevel } from "@/composables/apiTypes";
import { saveRecentModel } from "@/lib/recentModel";
import { useI18n } from "vue-i18n";
import { errorMessage } from "@/lib/errorMessage";

const { t } = useI18n();

// One trigger, one two-level menu. The conversation's account, model and
// reasoning effort describe a single request profile, so they live behind a
// single compact control instead of three dropdowns competing for the
// composer's width — three triggers laid end to end never fit a phone, and the
// flex row resolved the overflow by squeezing them toward zero.
//
// Level 1 is the globally unique account name plus a thinking-level row;
// level 2 is that account's model list. Picking a level-2 model therefore sets
// provider + account + model in one action — the account alone is never a
// selection, which is why the old "switching account implies its latest
// model" guess is gone.
//
// The second level expands inline rather than flying out sideways: a flyout
// needs horizontal room the composer does not have on a phone, and the whole
// point of this control is that it survives a 320px-wide container.
//
// Account+type lock together once the model has actually replied: a session is
// bound to one CLI on disk, so switching either would orphan the rollout, and
// the backend rejects it with 409 anyway. A first turn that failed before the
// model answered leaves both open so the user can retry elsewhere. Model and
// thinking level stay swappable forever.
const props = defineProps<{
  conversation: Conversation | null;
  // Lock the account rows once the model has replied (see
  // hasModelReplied). Computed by the parent (not derived from session_id
  // here) because the create handler pre-mints a session id; the "has the
  // model actually answered" signal is the message history, not the id.
  conversationStarted: boolean;
  // The conversation owner's allowed accounts per provider type (default
  // first). Flattened into the menu's level-1 rows; a user bound to a single
  // account still gets that one row so the account label stays reachable.
  providerAccounts?: Record<string, string[]>;
}>();

const emit = defineEmits<{
  (e: "updated", conv: Conversation): void;
  (e: "error", message: string): void;
}>();

const saving = ref(false);
const open = ref(false);
const rootRef = ref<HTMLElement | null>(null);
const triggerRef = ref<HTMLElement | null>(null);
// Which level-1 row has its level-2 list unfolded. Accordion, not multi-open:
// one open section keeps the panel short enough to never need scrolling on a
// phone. Holds an account's composite value, or THINK_SECTION.
const expanded = ref("");
// Safe against collision: an account composite always contains DELIM.
const THINK_SECTION = "__think__";

// Shared with every other registry consumer, so an admin's provider edit
// (invalidateModelRegistry) reaches the picker without a hard refresh. A failed
// fetch leaves the menu empty — a disabled control rather than a broken page.
const { registry } = useModelRegistry();
function ensureRegistry() {
  void loadModelRegistry().catch(() => undefined);
}

onMounted(ensureRegistry);

// Current selection — defaults to the conversation's stored pair, with a
// fall-through to the registry's defaults so a legacy conversation
// (provider=="") still renders something selectable.
const currentProvider = computed(() => {
  if (props.conversation?.provider) return props.conversation.provider;
  return registry.value?.default_provider ?? "";
});

const currentModel = computed(() => props.conversation?.model ?? "");
const currentThinkLevel = computed<ThinkLevel>(() => props.conversation?.think_level ?? "");

const providersList = computed(() => registry.value?.providers ?? []);
const accountList = computed(() => registry.value?.accounts ?? []);

// Accounts the owner may use for the current provider type (default first).
const accountsForCurrentProvider = computed(
  () => props.providerAccounts?.[currentProvider.value] ?? [],
);
// Pinned account, or the user's default (first bound) when unpinned.
const currentAccount = computed(
  () => props.conversation?.account_name || accountsForCurrentProvider.value[0] || "",
);

// Composite provider/account identity, one per bound account across all types,
// ordered by the registry's provider order then the binding order within a
// type. The composite carries both fields via a delimiter neither can hold, so
// a row is addressable by a single string. Only the globally unique account
// name is rendered.
interface AccountOption {
  provider: string;
  account: string;
  label: string;
  value: string;
}
const DELIM = "\n";
function optionValue(provider: string, account: string) {
  return `${provider}${DELIM}${account}`;
}
const accountOptions = computed<AccountOption[]>(() => {
  if (!registry.value) return [];
  const pa = props.providerAccounts ?? {};
  // The registry's accounts are authoritative, so bindings left behind after a
  // provider is removed cannot be selected.
  const configuredAccounts = new Set(
    accountList.value.map((account) => optionValue(account.provider, account.account)),
  );
  const order = providersList.value.map((p) => p.name);
  const rank = (type: string) => {
    const i = order.indexOf(type);
    return i < 0 ? Number.MAX_SAFE_INTEGER : i;
  };
  const types = Object.keys(pa).sort((a, b) => rank(a) - rank(b));
  const opts: AccountOption[] = [];
  for (const type of types) {
    for (const account of pa[type] ?? []) {
      if (!configuredAccounts.has(optionValue(type, account))) continue;
      opts.push({
        provider: type,
        account,
        label: account,
        value: optionValue(type, account),
      });
    }
  }
  return opts;
});

const currentAccountValue = computed(() =>
  optionValue(currentProvider.value, currentAccount.value),
);

// Models for one account. A missing entry is a stale binding and has no
// models.
function modelsFor(provider: string, account: string): string[] {
  return (
    accountList.value.find((a) => a.provider === provider && a.account === account)?.models ?? []
  );
}

// Locked rows stay visible but unopenable: hiding them would make the
// conversation look like it has no alternatives at all, when in fact the
// alternatives are simply out of reach until a new conversation.
function accountLocked(opt: AccountOption) {
  return props.conversationStarted && opt.value !== currentAccountValue.value;
}

const THINK_LEVELS: ThinkLevel[] = ["", "low", "medium", "high", "max"];
const THINK_KEY: Record<string, string> = {
  "": "modelSelector.thinkDefault",
  low: "modelSelector.thinkLow",
  medium: "modelSelector.thinkMedium",
  high: "modelSelector.thinkHigh",
  max: "modelSelector.thinkMax",
};
function thinkLabel(level: ThinkLevel) {
  return t(THINK_KEY[level] ?? THINK_KEY[""]);
}

const disabled = computed(() => !props.conversation || saving.value);

// Shared level-1/level-2 row styling. Inline rather than an @apply rule so the
// scoped <style> stays plain CSS — the only thing it is needed for here is the
// coarse-pointer padding bump, which Tailwind cannot express per input type.
const menuRowClass =
  "menu-row flex w-full items-center gap-1.5 rounded-lg px-2 py-1.5 text-left text-xs outline-none transition-colors hover:bg-accent hover:text-accent-foreground focus-visible:bg-accent disabled:pointer-events-none disabled:opacity-50";

// Trigger text, narrowest useful form first. The account name only earns its
// width when the user actually has somewhere else to switch to, and the
// thinking suffix only when it differs from the provider default.
const accountLabel = computed(() => currentAccount.value);
const showAccountInTrigger = computed(() => accountOptions.value.length > 1);
const modelLabel = computed(() => currentModel.value || t("modelSelector.modelPlaceholder"));
const triggerTitle = computed(() =>
  [accountLabel.value, currentModel.value, thinkLabel(currentThinkLevel.value)]
    .filter(Boolean)
    .join(" · "),
);

// Always pass an explicit account so the backend validates the model against
// the exact account it will run under (never the ambiguous "" → first-config
// account fallback, which can disagree with the user's default binding).
async function applySelection(
  provider: string,
  model: string,
  account: string,
  thinkLevel: ThinkLevel,
) {
  const conv = props.conversation;
  if (!conv) return;
  const unchanged =
    provider === conv.provider &&
    model === conv.model &&
    account === (conv.account_name || currentAccount.value) &&
    thinkLevel === (conv.think_level ?? "");
  if (unchanged) return;
  saving.value = true;
  try {
    const updated = await updateConversationModel(conv.id, provider, model, account, thinkLevel);
    saveRecentModel(
      updated.provider,
      updated.model,
      updated.account_name || account,
      updated.think_level ?? thinkLevel,
    );
    emit("updated", updated);
  } catch (err) {
    emit("error", errorMessage(err, t("modelSelector.updateFailed")));
  } finally {
    saving.value = false;
  }
}

function closeMenu(focusTrigger = false) {
  open.value = false;
  if (focusTrigger) void nextTick(() => triggerRef.value?.focus());
}

function toggleMenu() {
  if (disabled.value) return;
  open.value = !open.value;
  // Open onto the current account's models: the one list the user is most
  // likely to want is never more than a tap away.
  if (open.value) expanded.value = currentAccountValue.value;
}

function toggleSection(value: string) {
  expanded.value = expanded.value === value ? "" : value;
}

function pickModel(opt: AccountOption, model: string) {
  closeMenu(true);
  void applySelection(opt.provider, model, opt.account, currentThinkLevel.value);
}

function pickThinkLevel(level: ThinkLevel) {
  closeMenu(true);
  void applySelection(currentProvider.value, currentModel.value, currentAccount.value, level);
}

onClickOutside(rootRef, () => closeMenu());

// If the parent swaps to a different conversation, make sure the registry has
// already loaded so the menu can render valid options immediately — and drop a
// menu left open over the previous conversation.
watch(
  () => props.conversation?.id,
  () => {
    closeMenu();
    ensureRegistry();
  },
);
</script>

<template>
  <div ref="rootRef" class="relative min-w-0" data-testid="model-selector">
    <button
      ref="triggerRef"
      type="button"
      class="model-trigger relative flex h-7 min-w-0 max-w-full items-center gap-1 rounded-lg px-1 py-0 text-[12px] text-[#333333] dark:text-muted-foreground transition-colors hover:bg-accent hover:text-foreground disabled:pointer-events-none disabled:opacity-50 @max-sm:gap-0.5 @max-sm:px-1"
      :disabled="disabled"
      :title="triggerTitle"
      aria-haspopup="menu"
      :aria-expanded="open"
      data-testid="model-selector-trigger"
      @click="toggleMenu"
      @keydown.esc="closeMenu()"
    >
      <span
        v-if="showAccountInTrigger && accountLabel"
        class="hidden shrink-0 opacity-70 @sm:inline"
        data-testid="model-selector-account"
        >{{ accountLabel }}</span
      >
      <span class="truncate text-foreground" data-testid="model-selector-model">{{
        modelLabel
      }}</span>
      <span
        v-if="currentThinkLevel"
        class="shrink-0 text-[11px] text-[#8f8f8f]"
        data-testid="model-selector-think"
        >{{ thinkLabel(currentThinkLevel) }}</span
      >
      <ChevronDown class="size-[13px] shrink-0" />
    </button>

    <!-- Opens upward: the composer sits on the bottom edge of the viewport, so
         a downward panel would render off-screen. -->
    <div
      v-if="open"
      role="menu"
      data-testid="model-selector-menu"
      class="absolute bottom-full left-0 z-50 mb-2 max-h-[min(60vh,22rem)] w-64 max-w-[calc(100vw-2rem)] overflow-y-auto rounded-xl border border-border bg-popover p-1 text-popover-foreground shadow-xl"
      @keydown.esc="closeMenu(true)"
    >
      <template v-for="opt in accountOptions" :key="opt.value">
        <button
          type="button"
          role="menuitem"
          :class="menuRowClass"
          :disabled="accountLocked(opt)"
          :aria-expanded="expanded === opt.value"
          :title="
            accountLocked(opt) ? t('modelSelector.accountLocked') : t('modelSelector.pickAccount')
          "
          data-testid="model-account-row"
          :data-value="opt.value"
          @click="toggleSection(opt.value)"
        >
          <ChevronRight
            class="size-3 shrink-0 opacity-60 transition-transform"
            :class="{ 'rotate-90': expanded === opt.value }"
          />
          <span class="truncate font-mono">{{ opt.label }}</span>
          <Check
            v-if="opt.value === currentAccountValue"
            class="ml-auto size-3 shrink-0 opacity-70"
          />
        </button>
        <div v-if="expanded === opt.value" class="mb-0.5 pl-4">
          <button
            v-for="m in modelsFor(opt.provider, opt.account)"
            :key="m"
            type="button"
            role="menuitem"
            :class="menuRowClass"
            data-testid="model-option"
            :data-value="m"
            @click="pickModel(opt, m)"
          >
            <span class="truncate font-mono">{{ m }}</span>
            <Check
              v-if="m === currentModel && opt.value === currentAccountValue"
              class="ml-auto size-3 shrink-0 opacity-70"
            />
          </button>
          <p
            v-if="modelsFor(opt.provider, opt.account).length === 0"
            class="px-2 py-1.5 text-xs text-muted-foreground"
          >
            {{ t("modelSelector.noModels") }}
          </p>
        </div>
      </template>

      <div v-if="accountOptions.length" class="-mx-1 my-1 h-px bg-border" />

      <button
        type="button"
        role="menuitem"
        :class="menuRowClass"
        :aria-expanded="expanded === THINK_SECTION"
        :title="t('modelSelector.pickThinkLevel')"
        data-testid="model-think-row"
        @click="toggleSection(THINK_SECTION)"
      >
        <ChevronRight
          class="size-3 shrink-0 opacity-60 transition-transform"
          :class="{ 'rotate-90': expanded === THINK_SECTION }"
        />
        <span class="truncate">{{ t("modelSelector.thinkLabel") }}</span>
        <span class="ml-auto shrink-0 text-muted-foreground">{{
          thinkLabel(currentThinkLevel)
        }}</span>
      </button>
      <div v-if="expanded === THINK_SECTION" class="pl-4">
        <button
          v-for="level in THINK_LEVELS"
          :key="level || 'default'"
          type="button"
          role="menuitem"
          :class="menuRowClass"
          data-testid="model-think-option"
          :data-value="level"
          @click="pickThinkLevel(level)"
        >
          <span class="truncate">{{ thinkLabel(level) }}</span>
          <Check v-if="level === currentThinkLevel" class="ml-auto size-3 shrink-0 opacity-70" />
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* Touch height without touch-sized chrome. The trigger is 24px so it shares a
 * composer line with the send cluster, which is well under the ~44px a finger
 * needs; the overlay extends only vertically so neighbours on the same line
 * cannot swallow each other's taps. Menu rows get real padding instead — the
 * panel has the room. */
@media (pointer: coarse) {
  .model-trigger::after {
    content: "";
    position: absolute;
    left: 0;
    right: 0;
    top: -0.625rem;
    bottom: -0.625rem;
  }
  /* Menu rows get a real finger-sized box instead — the panel has the room
   * the composer line does not. */
  .menu-row {
    padding-top: 0.5rem;
    padding-bottom: 0.5rem;
  }
}
</style>
