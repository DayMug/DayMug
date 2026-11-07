<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { onClickOutside } from "@vueuse/core";
import { Check, ChevronDown, FolderOpen, Laptop, Lock, Server, X } from "lucide-vue-next";
import { useI18n } from "vue-i18n";
import {
  clearConversationDevice,
  pickDeviceFolder,
  setConversationDevice,
} from "@/composables/apiDevices";
import {
  loadConversationDevice,
  setConversationDeviceBinding,
  useConversationDevice,
} from "@/composables/useConversationDevice";
import { useDesktopDevice } from "@/composables/useDesktopDevice";
import {
  dirLabel,
  forgetDesktopDir,
  recentDesktopDirs,
  rememberDesktopDir,
} from "@/lib/desktopClient";
import { errorMessage } from "@/lib/errorMessage";
import { saveAgentLocation } from "@/lib/agentPrefs";

// Where this conversation's tools run: the server (the default, and the only
// option a browser ever has) or a folder on the computer hosting this page.
// Rendered by the composer only inside DayMug-Client.
const props = defineProps<{
  conversationId: string;
  // The agent the conversation belongs to; its next conversation starts in
  // whatever location the user picks here.
  agentId?: string;
  // The model has replied (ChatPage's conversationStarted). Together with a
  // folder binding this fixes the folder: the server refuses to change it.
  conversationStarted?: boolean;
}>();
const emit = defineEmits<{ (e: "error", message: string): void }>();

const { t } = useI18n();
const { device, ensureDesktopDevice } = useDesktopDevice();

// Shared with the chat page, whose file panel follows this binding.
const binding = useConversationDevice(() => props.conversationId);
const open = ref(false);
const busy = ref(false);
const recent = ref<string[]>([]);
const rootRef = ref<HTMLElement | null>(null);
onClickOutside(rootRef, () => (open.value = false));

function load() {
  void loadConversationDevice(props.conversationId);
}

onMounted(() => {
  void ensureDesktopDevice();
});
watch(() => props.conversationId, load, { immediate: true });

// Bound to this computer, as opposed to another of the user's devices.
const boundHere = computed(
  () => !!binding.value && !!device.value && binding.value.device_id === device.value.id,
);
// The history now refers to this folder's files, so the menu only shows it.
const locked = computed(() => !!binding.value && !!props.conversationStarted);
const online = computed(() => (boundHere.value ? !!device.value?.online : !!binding.value?.online));

const triggerLabel = computed(() => {
  if (!binding.value) return t("device.server");
  const dir = dirLabel(binding.value.work_dir);
  return boundHere.value ? dir : `${binding.value.device_name} · ${dir}`;
});
const triggerTitle = computed(() => {
  if (!binding.value) return t("device.serverTitle");
  const title = t("device.boundTitle", {
    name: binding.value.device_name,
    dir: binding.value.work_dir,
  });
  return locked.value ? `${title}\n${t("device.lockedNote")}` : title;
});

function toggle() {
  open.value = !open.value;
  if (open.value) {
    recent.value = recentDesktopDirs();
    void ensureDesktopDevice();
  }
}

async function run(action: () => Promise<void>) {
  if (busy.value) return;
  busy.value = true;
  try {
    await action();
  } catch (e) {
    emit("error", errorMessage(e, t("device.failed")));
  } finally {
    busy.value = false;
  }
}

async function bindTo(dir: string) {
  const d = await ensureDesktopDevice();
  if (!d) throw new Error(t("device.notPaired"));
  const id = props.conversationId;
  const b = await setConversationDevice(id, d.id, dir);
  setConversationDeviceBinding(id, b);
  rememberDesktopDir(b.work_dir);
  if (props.agentId) {
    saveAgentLocation(props.agentId, {
      kind: "device",
      device_id: b.device_id,
      work_dir: b.work_dir,
    });
  }
}

function pickFolder() {
  open.value = false;
  if (locked.value) return;
  void run(async () => {
    const d = await ensureDesktopDevice();
    if (!d) throw new Error(t("device.notPaired"));
    const start = boundHere.value ? (binding.value?.work_dir ?? "") : (recent.value[0] ?? "");
    const dir = await pickDeviceFolder(d.id, start);
    if (dir) await bindTo(dir);
  });
}

function removeRecent(dir: string) {
  forgetDesktopDir(dir);
  recent.value = recentDesktopDirs();
}

function pickRecent(dir: string) {
  open.value = false;
  if (locked.value) return;
  void run(() => bindTo(dir));
}

function useServer() {
  open.value = false;
  if (!binding.value || locked.value) return;
  void run(async () => {
    const id = props.conversationId;
    await clearConversationDevice(id);
    setConversationDeviceBinding(id, null);
    if (props.agentId) saveAgentLocation(props.agentId, { kind: "server" });
  });
}

const menuRowClass =
  "flex w-full items-center gap-1.5 rounded-lg px-2 py-1.5 text-left text-xs outline-none transition-colors hover:bg-accent hover:text-accent-foreground focus-visible:bg-accent disabled:pointer-events-none disabled:opacity-50";
// The recent row's hover lives on its wrapper, so the remove button shares it.
const recentRowClass =
  "flex min-w-0 flex-1 items-center gap-1.5 rounded-lg px-2 py-1.5 text-left text-xs outline-none focus-visible:bg-accent";
</script>

<template>
  <div ref="rootRef" class="relative min-w-0 shrink" data-testid="device-selector">
    <button
      type="button"
      class="flex h-7 min-w-0 max-w-[14rem] items-center gap-1 rounded-lg px-1 text-[12px] text-[#333333] transition-colors hover:bg-accent hover:text-foreground disabled:opacity-50 dark:text-muted-foreground"
      :disabled="busy"
      :title="triggerTitle"
      aria-haspopup="menu"
      :aria-expanded="open"
      data-testid="device-selector-trigger"
      @click="toggle"
    >
      <span class="relative shrink-0">
        <Laptop v-if="binding" class="size-[13px]" />
        <Server v-else class="size-[13px]" />
        <span
          v-if="binding"
          class="absolute -right-0.5 -bottom-0.5 size-1.5 rounded-full"
          :class="online ? 'bg-emerald-500' : 'bg-muted-foreground'"
          data-testid="device-selector-status"
        />
      </span>
      <span class="truncate text-foreground" data-testid="device-selector-label">{{
        triggerLabel
      }}</span>
      <ChevronDown class="size-[13px] shrink-0" />
    </button>

    <div
      v-if="open"
      role="menu"
      data-testid="device-selector-menu"
      class="absolute bottom-full left-0 z-50 mb-2 w-72 max-w-[calc(100vw-2rem)] rounded-xl border border-border bg-popover p-1 text-popover-foreground shadow-xl"
      @keydown.esc="open = false"
    >
      <p class="px-2 py-1 text-[11px] text-muted-foreground">{{ t("device.heading") }}</p>
      <button
        v-if="!locked"
        type="button"
        role="menuitem"
        :class="menuRowClass"
        data-testid="device-option-server"
        @click="useServer"
      >
        <Server class="size-3 shrink-0 opacity-70" />
        <span class="truncate">{{ t("device.server") }}</span>
        <Check v-if="!binding" class="ml-auto size-3 shrink-0 opacity-70" />
      </button>
      <div
        v-if="binding"
        role="menuitem"
        aria-disabled="true"
        class="flex w-full items-center gap-1.5 rounded-lg px-2 py-1.5 text-xs"
        :class="locked ? '' : 'opacity-50'"
        data-testid="device-option-current"
      >
        <Laptop class="size-3 shrink-0 opacity-70" />
        <span class="truncate font-mono select-text" :title="binding.work_dir">{{
          binding.work_dir
        }}</span>
        <Lock v-if="locked" class="ml-auto size-3 shrink-0 opacity-70" />
        <Check v-else class="ml-auto size-3 shrink-0 opacity-70" />
      </div>
      <p
        v-if="locked"
        class="px-2 pt-0.5 pb-1.5 text-[11px] leading-snug text-muted-foreground"
        data-testid="device-locked-note"
      >
        {{ t("device.lockedNote") }}
      </p>
      <template v-else>
        <template v-for="dir in recent" :key="dir">
          <div
            v-if="!boundHere || dir !== binding?.work_dir"
            class="group flex items-center rounded-lg hover:bg-accent hover:text-accent-foreground"
            data-testid="device-recent-row"
          >
            <button
              type="button"
              role="menuitem"
              :class="recentRowClass"
              data-testid="device-option-recent"
              @click="pickRecent(dir)"
            >
              <Laptop class="size-3 shrink-0 opacity-40" />
              <span class="truncate font-mono" :title="dir">{{ dir }}</span>
            </button>
            <button
              type="button"
              class="mr-1 flex size-5 shrink-0 items-center justify-center rounded text-muted-foreground opacity-60 transition-opacity hover:bg-background hover:text-foreground hover:opacity-100 focus-visible:opacity-100"
              :title="t('device.removeRecent')"
              :aria-label="t('device.removeRecent')"
              data-testid="device-recent-remove"
              @click.stop="removeRecent(dir)"
            >
              <X class="size-3" />
            </button>
          </div>
        </template>
        <button
          type="button"
          role="menuitem"
          :class="menuRowClass"
          data-testid="device-option-pick"
          @click="pickFolder"
        >
          <FolderOpen class="size-3 shrink-0 opacity-70" />
          <span class="truncate">{{ t("device.pickFolder") }}</span>
        </button>
      </template>
    </div>
  </div>
</template>
