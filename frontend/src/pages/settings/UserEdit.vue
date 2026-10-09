<script setup lang="ts">
import { ref, computed } from "vue";
import { useRoute, useRouter } from "vue-router";
import { useUsers } from "@/composables/useUsers";
import UserForm from "@/components/UserForm.vue";
import AgentBotsEditor from "@/components/AgentBotsEditor.vue";
import type { UserFormData } from "@/components/UserForm.vue";
import AgentAvatar from "@/components/AgentAvatar.vue";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Dot } from "@/components/ui/dot";
import { Trash2 } from "lucide-vue-next";
import { useI18n } from "vue-i18n";
import SettingsDetailHeader from "./SettingsDetailHeader.vue";
import { usePageTitle } from "@/composables/useDocumentTitle";

const route = useRoute();
const router = useRouter();
const { t } = useI18n();
const { users, loadUsers, handleUpdateUser, handleDeleteUser } = useUsers();

const showDeleteConfirm = ref(false);
const deleteConfirmInput = ref("");
const submitting = ref(false);
const deleting = ref(false);
// Failures stay on this page: leaving the editor is the success signal, so a
// rejected save or delete has to be visible here or it reads as a success.
const saveError = ref("");
const deleteError = ref("");

// The detail editor uses one persistent form split into focused tab panels.
// v-show keeps each panel mounted, so switching tabs never discards edits.
const activeTab = ref<1 | 2 | 3>(1);

const userObj = computed(() => {
  const id = route.params.id as string;
  return users.value.find((u) => u.id === id);
});

const initialData = computed<UserFormData | undefined>(() => {
  const user = userObj.value;
  if (!user) return undefined;
  return {
    name: user.name,
    work_dir: user.work_dir,
    avatar: user.avatar,
    role_definition: user.role_definition,
    default_model: user.default_model ?? "",
    think_level: user.think_level ?? "",
    case_mode: user.case_mode ?? false,
    mcp_config: user.mcp_config,
    claude_md_content: user.claude_md_content,
    manage_claude_md: user.manage_claude_md,
  };
});

const userName = computed(() => initialData.value?.name ?? "");
const userAvatar = computed(() => initialData.value?.avatar ?? "");
const isLoginUser = computed(() => Boolean(userObj.value?.username));

usePageTitle(computed(() => userName.value || t("settings.titles.editAgent")));

// Truncated agent id, mono — UI design styles this as the metadata line
// (e.g. "agt_3f1a7c · created Apr 12 · last active 2m ago"). We have less
// metadata than the design; just show id + created date.
const agentMeta = computed(() => {
  const u = userObj.value;
  if (!u) return "";
  const shortId = `agt_${u.id.slice(0, 6)}`;
  if (u.created_at) {
    const d = new Date(u.created_at);
    if (!isNaN(d.getTime())) {
      const fmt = d.toLocaleDateString(undefined, { month: "short", day: "numeric" });
      return `${shortId} · ${t("settings.agentForm.agentMetaCreated", { date: fmt })}`;
    }
  }
  return shortId;
});

async function onSubmit(data: UserFormData) {
  const id = route.params.id as string;
  if (!id) return;
  if (submitting.value) return;
  submitting.value = true;
  saveError.value = "";
  try {
    const res = await handleUpdateUser(id, {
      name: data.name,
      work_dir: data.work_dir,
      avatar: data.avatar,
      role_definition: data.role_definition,
      default_model: data.default_model,
      think_level: data.think_level,
      case_mode: data.case_mode,
      mcp_config: data.mcp_config,
      claude_md_content: data.claude_md_content,
      manage_claude_md: data.manage_claude_md,
    });
    // Staying put also preserves the edits the backend rejected, so the user
    // can correct the offending field instead of retyping the whole form.
    if (!res.ok) {
      saveError.value = t("settings.agentForm.saveFailed", { error: res.error });
      return;
    }
    router.push({ name: "settings" });
  } finally {
    submitting.value = false;
  }
}

function onCancel() {
  router.push({ name: "settings" });
}

function startDelete() {
  showDeleteConfirm.value = true;
  deleteConfirmInput.value = "";
  deleteError.value = "";
}

function cancelDelete() {
  showDeleteConfirm.value = false;
  deleteConfirmInput.value = "";
  deleteError.value = "";
}

async function confirmDelete() {
  if (isLoginUser.value) return;
  const id = route.params.id as string;
  if (!id) return;
  if (deleteConfirmInput.value !== userName.value) return;
  if (deleting.value) return;
  deleting.value = true;
  deleteError.value = "";
  try {
    const res = await handleDeleteUser(id);
    if (!res.ok) {
      deleteError.value = t("settings.agentForm.deleteFailed", { error: res.error });
      return;
    }
    router.push({ name: "settings" });
  } finally {
    deleting.value = false;
  }
}

// We render a hidden submit button inside UserForm by reaching into its
// API. Cleaner than re-implementing the form, since UserForm exposes a
// `submit` event triggered from its internal Save button. But UI design
// pins Save to the editor header; we replicate that by adding a header
// Save that scrolls / focuses to UserForm and relies on its existing
// Save action. Simpler: leave UserForm's bottom Save in place and treat
// the header buttons as visual anchors for now.
</script>

<template>
  <div class="mx-auto w-full max-w-[880px] px-4 pb-8">
    <SettingsDetailHeader
      :title="userName || t('settings.titles.editAgent')"
      :eyebrow="t('settings.agentForm.editingAgent')"
    />

    <!-- Editorial avatar / display-name block — kept below the
         compact detail header so the agent identity is still visually
         prominent on the page. -->
    <header class="flex items-start gap-5 mb-7">
      <AgentAvatar
        :name="userName"
        :avatar="userAvatar"
        class="size-16 shrink-0"
        fallback-class="text-xl font-bold"
      />
      <div class="flex-1 min-w-0">
        <div
          class="flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-[0.08em] text-muted-foreground mb-1"
        >
          <Dot tone="ok" :size="6" />
          {{ t("settings.agentForm.editingAgent") }}
        </div>
        <h2 class="text-[42px] font-semibold leading-none tracking-[-0.03em] text-foreground">
          {{ userName || "—" }}
        </h2>
        <div class="mt-2 text-[12px] font-mono text-muted-foreground">
          {{ agentMeta }}
        </div>
      </div>
    </header>

    <!-- Nothing below renders until the agent record is in hand. On a hard
         refresh / deep link the roster arrives after this component's setup,
         and an editor rendered against a missing record would offer a Save
         button wired to blank fields — that is exactly how MCP config and
         CLAUDE.md used to get wiped. No record, no submit path. It also keeps
         the delete confirmation honest: with no record the expected name is
         "" and an empty input would match it. -->
    <template v-if="initialData && userObj">
      <!-- A real tab list replaces the previous in-page jump links. The
           horizontal overflow is contained on narrow screens without
           exposing a vertical scrollbar from the active-tab underline. -->
      <div
        class="flex border-b border-border mb-8 -mt-2 overflow-x-auto overflow-y-hidden wy-scroll"
        role="tablist"
        :aria-label="t('settings.agentForm.editingAgent')"
      >
        <button
          v-for="(label, i) in [
            t('settings.agentForm.basic'),
            t('settings.agentForm.rolePersona'),
            t('settings.agentForm.integrations'),
          ]"
          :id="`form-tab-${i + 1}`"
          :key="i"
          type="button"
          role="tab"
          :aria-selected="activeTab === i + 1"
          :aria-controls="`form-panel-${i + 1}`"
          class="tab-anchor flex items-baseline gap-2 px-3 sm:px-4 py-3 text-[13px] cursor-pointer transition-colors whitespace-nowrap shrink-0 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset"
          :class="
            activeTab === ((i + 1) as 1 | 2 | 3)
              ? 'font-semibold text-foreground border-b-2 border-foreground -mb-px'
              : 'text-muted-foreground hover:text-foreground border-b-2 border-transparent -mb-px'
          "
          @click="activeTab = (i + 1) as 1 | 2 | 3"
        >
          <span
            class="font-mono text-[11px]"
            :class="activeTab === ((i + 1) as 1 | 2 | 3) ? 'text-primary' : 'text-[var(--ink-5)]'"
            >{{ String(i + 1).padStart(2, "0") }}</span
          >
          <span>{{ label }}</span>
        </button>
      </div>

      <UserForm
        mode="edit"
        :initial-data="initialData"
        :submitting="submitting"
        :active-section="activeTab"
        @submit="onSubmit"
        @cancel="onCancel"
      >
        <template #integrations>
          <AgentBotsEditor :agent-id="userObj.id" @changed="loadUsers()" />
        </template>
      </UserForm>

      <!-- Sits directly under the form's action bar so a rejected save is
           visible without scrolling back up from the Save button. -->
      <p
        v-if="saveError"
        class="mt-4 px-3 py-2 rounded-md border border-destructive/40 bg-destructive/10 text-[13px] text-destructive"
        data-testid="save-error"
        role="alert"
      >
        {{ saveError }}
      </p>

      <!-- Delete trigger — placed at the bottom so its confirmation box
           (rendered just below) is always in view when opened. -->
      <div v-if="!isLoginUser && !showDeleteConfirm" class="mt-8 pt-6 border-t border-border">
        <Button
          variant="ghost"
          size="sm"
          class="h-8 gap-1.5 text-destructive hover:bg-destructive/10 hover:text-destructive"
          @click="startDelete"
        >
          <Trash2 class="size-4" />
          {{ t("settings.agentForm.delete") }}
        </Button>
      </div>

      <!-- Delete confirmation -->
      <div
        v-if="showDeleteConfirm"
        class="mt-8 p-4 rounded-md border border-destructive/40 bg-destructive/10"
      >
        <p class="text-sm text-foreground leading-relaxed mb-3">
          {{ t("settings.agentForm.deleteConfirm", { name: userName }) }}
        </p>
        <Input
          v-model="deleteConfirmInput"
          class="mb-3 border-destructive/40 focus-visible:ring-destructive/30"
          :placeholder="t('settings.agentForm.deleteConfirmPlaceholder', { name: userName })"
        />
        <div class="flex gap-2.5">
          <Button
            variant="destructive"
            :disabled="deleteConfirmInput !== userName || deleting"
            @click="confirmDelete"
          >
            {{ t("settings.agentForm.confirmDelete") }}
          </Button>
          <Button variant="outline" :disabled="deleting" @click="cancelDelete">{{
            t("common.cancel")
          }}</Button>
        </div>
        <p
          v-if="deleteError"
          class="mt-3 text-[13px] text-destructive"
          data-testid="delete-error"
          role="alert"
        >
          {{ deleteError }}
        </p>
      </div>
    </template>

    <div v-else class="py-10 text-center text-sm text-muted-foreground">
      {{ t("common.loading") }}
    </div>
  </div>
</template>
