<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { onClickOutside } from "@vueuse/core";
import { Check, ChevronDown, FolderOpen, Laptop, Server } from "lucide-vue-next";
import { useI18n } from "vue-i18n";
import {
  clearConversationDevice,
  fetchConversationDevice,
  pickDeviceFolder,
  setConversationDevice,
  type ConversationDeviceBinding,
} from "@/composables/apiDevices";
import { useDesktopDevice } from "@/composables/useDesktopDevice";
import { dirLabel, recentDesktopDirs, rememberDesktopDir } from "@/lib/desktopClient";
import { errorMessage } from "@/lib/errorMessage";

// Where this conversation's tools run: the server (the default, and the only
// option a browser ever has) or a folder on the computer hosting this page.
// Rendered by the composer only inside DayMug-Client.
const props = defineProps<{ conversationId: string }>();
const emit = defineEmits<{ (e: "error", message: string): void }>();

const { t } = useI18n();
const { device, ensureDesktopDevice } = useDesktopDevice();

const binding = ref<ConversationDeviceBinding | null>(null);
const open = ref(false);
const busy = ref(false);
const recent = ref<string[]>([]);
const rootRef = ref<HTMLElement | null>(null);
onClickOutside(rootRef, () => (open.value = false));

async function load() {
  const id = props.conversationId;
  try {
    const b = await fetchConversationDevice(id);
    if (id === props.conversationId) binding.value = b;
  } catch {
    binding.value = null;
  }
}

onMounted(() => {
  void ensureDesktopDevice();
});
watch(() => props.conversationId, load, { immediate: true });

// Bound to this computer, as opposed to another of the user's devices.
const boundHere = computed(
  () => !!binding.value && !!device.value && binding.value.device_id === device.value.id,
);
const online = computed(() => (boundHere.value ? !!device.value?.online : !!binding.value?.online));

const triggerLabel = computed(() => {
  if (!binding.value) return t("device.server");
  const dir = dirLabel(binding.value.work_dir);
  return boundHere.value ? dir : `${binding.value.device_name} · ${dir}`;
});
const triggerTitle = computed(() =>
  binding.value
    ? t("device.boundTitle", { name: binding.value.device_name, dir: binding.value.work_dir })
    : t("device.serverTitle"),
);

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
  binding.value = await setConversationDevice(props.conversationId, d.id, dir);
  rememberDesktopDir(binding.value.work_dir);
}

function pickFolder() {
  open.value = false;
  void run(async () => {
    const d = await ensureDesktopDevice();
    if (!d) throw new Error(t("device.notPaired"));
    const start = boundHere.value ? (binding.value?.work_dir ?? "") : (recent.value[0] ?? "");
    const dir = await pickDeviceFolder(d.id, start);
    if (dir) await bindTo(dir);
  });
}

function pickRecent(dir: string) {
  open.value = false;
  void run(() => bindTo(dir));
}

function useServer() {
  open.value = false;
  if (!binding.value) return;
  void run(async () => {
    await clearConversationDevice(props.conversationId);
    binding.value = null;
  });
}

const menuRowClass =
  "flex w-full items-center gap-1.5 rounded-lg px-2 py-1.5 text-left text-xs outline-none transition-colors hover:bg-accent hover:text-accent-foreground focus-visible:bg-accent disabled:pointer-events-none disabled:opacity-50";
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
      <button
        v-if="binding"
        type="button"
        role="menuitem"
        :class="menuRowClass"
        disabled
        data-testid="device-option-current"
      >
        <Laptop class="size-3 shrink-0 opacity-70" />
        <span class="truncate font-mono" :title="binding.work_dir">{{ binding.work_dir }}</span>
        <Check class="ml-auto size-3 shrink-0 opacity-70" />
      </button>
      <template v-for="dir in recent" :key="dir">
        <button
          v-if="!boundHere || dir !== binding?.work_dir"
          type="button"
          role="menuitem"
          :class="menuRowClass"
          data-testid="device-option-recent"
          @click="pickRecent(dir)"
        >
          <Laptop class="size-3 shrink-0 opacity-40" />
          <span class="truncate font-mono" :title="dir">{{ dir }}</span>
        </button>
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
    </div>
  </div>
</template>
