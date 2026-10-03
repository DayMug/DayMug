<script setup lang="ts">
// Human-user administration. The page owns the list, the single error line and
// every server round-trip; the create form, batch toolbar, inline edit row and
// password dialog are children that hold only their own draft state.
import { onMounted, ref, computed } from "vue";
import { Pencil, KeyRound, CircleCheck, CircleSlash, Trash2 } from "lucide-vue-next";
import {
  adminListUsers,
  adminCreateUser,
  adminUpdateUser,
  adminDeleteUser,
  adminSetPassword,
  adminSetDisabled,
  adminBatchSetDefaultModel,
  adminBatchSetSandboxMode,
  adminBatchSetProviderBinding,
  adminFetchPublicConfig,
} from "@/composables/useApi";
import type {
  User,
  AdminCreateUserData,
  AdminUpdateUserData,
  AdminPublicConfig,
  ModelRegistry,
} from "@/composables/useApi";
import { loadModelRegistry } from "@/composables/useModelRegistry";
import { useAsyncOperation, type RunOptions } from "@/composables/useAsyncOperation";
import { useAdminProviderOptions } from "@/composables/useAdminProviderOptions";
import { useAuth } from "@/composables/useAuth";
import { useI18n } from "vue-i18n";
import SettingsDetailHeader from "./SettingsDetailHeader.vue";
import AdminUsersCreateForm from "./AdminUsersCreateForm.vue";
import AdminUsersBatchToolbar from "./AdminUsersBatchToolbar.vue";
import type { BatchApply } from "./AdminUsersBatchToolbar.vue";
import AdminUsersEditRow from "./AdminUsersEditRow.vue";
import AdminUsersPasswordDialog from "./AdminUsersPasswordDialog.vue";
import { usePageTitle } from "@/composables/useDocumentTitle";
import { useConfirm } from "@/composables/useConfirm";

const { authMe } = useAuth();
const { t } = useI18n();
const { confirm } = useConfirm();
usePageTitle(computed(() => t("settings.titles.adminUsers")));

const users = ref<User[]>([]);
const cfg = ref<AdminPublicConfig | null>(null);
// One operation for every in-page action: they all funnel into the single
// error line at the top (starting any action clears the previous action's
// error). Busy flags stay per-action and are passed to run() per call. The
// password dialog is the one exception — see pwError below.
const { error: errorMsg, run } = useAsyncOperation();
// Starts true so the first paint shows the loading row instead of flashing
// "no users" before onMounted's reload kicks in.
const loading = ref<boolean>(true);

const showCreate = ref<boolean>(false);
const createFormRef = ref<InstanceType<typeof AdminUsersCreateForm> | null>(null);
const createBusy = ref<boolean>(false);

// Per-row inline-edit state. Only one row open at a time.
const editingId = ref<string>("");
const editBusy = ref<boolean>(false);
// Enable/disable and delete share one flag: they are one-at-a-time actions,
// and disabling their buttons for the duration is what stops a double-click
// from firing a second delete against a row the first one already removed.
const rowBusy = ref<boolean>(false);

// Model registry (/api/models) drives the model pickers — both the per-user
// edit form and the batch toolbar. Loaded once alongside the user list; null
// until it resolves (the pickers render empty meanwhile).
const registry = ref<ModelRegistry | null>(null);

const selectedIds = ref<Set<string>>(new Set());
const batchBusy = ref<boolean>(false);
const batchMsg = ref<string>("");
const allSelected = computed<boolean>(
  () => users.value.length > 0 && selectedIds.value.size === users.value.length,
);

// runAction wraps run() for user-initiated actions. run() clears the error
// line itself, but batchMsg is page-owned: without this a stale green
// "N users updated" outlives the batch that produced it and keeps sitting
// above whatever the next action reports. reload() stays on plain run(),
// because applyBatch reloads *after* writing batchMsg.
function runAction<T>(fn: () => Promise<T>, opts?: RunOptions): Promise<T | undefined> {
  batchMsg.value = "";
  return run(fn, opts);
}

const {
  optionsByType: providerOptionsByType,
  types: providerTypes,
  labelForType,
} = useAdminProviderOptions(cfg);

// Precomputed so the children can stay i18n-agnostic about CLI-type naming.
const typeLabels = computed<Record<string, string>>(() =>
  Object.fromEntries(providerTypes.value.map((ptype) => [ptype, labelForType(ptype)])),
);

// modelGroups flattens the registry into provider-labelled groups so the
// pickers can render one <optgroup> per provider. A model id is globally
// unique across providers, so the flat option values stay unambiguous.
const modelGroups = computed<{ provider: string; models: string[] }[]>(() => {
  if (!registry.value) return [];
  return registry.value.providers.map((p) => ({ provider: p.name, models: p.models }));
});

// Row actions are icon-only, so every one of them carries a title/aria-label —
// the class list is identical and long enough that repeating it four times per
// row buried those labels.
const iconBtn =
  "inline-flex size-7 items-center justify-center rounded border border-border text-muted-foreground hover:bg-accent hover:text-foreground cursor-pointer disabled:opacity-40 disabled:cursor-not-allowed";

function modelLabel(model: string | undefined): string {
  if (!model) return t("settings.adminUsers.noDefaultModel");
  return model;
}

function toggleSelect(id: string) {
  if (selectedIds.value.has(id)) selectedIds.value.delete(id);
  else selectedIds.value.add(id);
  selectedIds.value = new Set(selectedIds.value);
}

function toggleSelectAll() {
  if (allSelected.value) selectedIds.value = new Set();
  else selectedIds.value = new Set(users.value.map((u) => u.id));
}

// sendBatch dispatches one batch request and returns the localized summary.
// Splitting it out keeps applyBatch's shared choreography (guard, clear
// selection, reload) in one place instead of three near-identical copies.
async function sendBatch(ids: string[], req: BatchApply): Promise<string> {
  switch (req.kind) {
    case "model": {
      const res = await adminBatchSetDefaultModel({ user_ids: ids, model: req.model });
      return t("settings.adminUsers.batchDone", { count: res.updated });
    }
    case "sandbox": {
      const res = await adminBatchSetSandboxMode({ user_ids: ids, sandbox_mode: req.mode });
      return t("settings.adminUsers.batchSandboxDone", { count: res.updated });
    }
    case "provider": {
      const res = await adminBatchSetProviderBinding({
        user_ids: ids,
        provider_type: req.providerType,
        provider_names: req.providerNames,
      });
      return t("settings.adminUsers.batchProviderDone", {
        count: res.updated,
        type: labelForType(res.provider_type),
      });
    }
  }
}

async function applyBatch(req: BatchApply) {
  if (selectedIds.value.size === 0) return;
  const ids = [...selectedIds.value];
  await runAction(
    async () => {
      batchMsg.value = await sendBatch(ids, req);
      selectedIds.value = new Set();
      await reload();
    },
    { busy: batchBusy },
  );
}

// Password reset modal state — the dialog owns the typed password itself.
// It runs its own operation because the modal covers the page-level error
// line: a rejected reset reported up there would be invisible behind it.
const pwTargetId = ref<string>("");
const { error: pwError, busy: pwBusy, run: runPwReset } = useAsyncOperation();

// summariseBindings renders the row's per-type bindings as a compact
// "claude:hailing, codex:alt" pill. An unbound user shows "none" — there is no implicit default provider, so they can't run jobs
// until an admin binds one.
function summariseBindings(u: User): string {
  const map = u.provider_bindings ?? {};
  const keys = Object.keys(map).sort();
  if (keys.length === 0) return t("common.none");
  return keys.map((k) => `${k}:${map[k] || t("common.none")}`).join(", ");
}

// Two actions in quick succession each reload; if the responses land out of
// order the older list wins and rows the newer one dropped flash back into the
// table. Only the newest reload is allowed to write.
let reloadGeneration = 0;

async function reload() {
  const generation = ++reloadGeneration;
  await run(
    async () => {
      const [u, c, reg] = await Promise.all([
        adminListUsers(),
        adminFetchPublicConfig(),
        loadModelRegistry().catch(() => null),
      ]);
      if (generation !== reloadGeneration) return;
      users.value = u;
      cfg.value = c;
      registry.value = reg;
    },
    { busy: loading },
  );
}

async function handleCreate(data: AdminCreateUserData) {
  await runAction(
    async () => {
      await adminCreateUser(data);
      showCreate.value = false;
      createFormRef.value?.reset();
      await reload();
    },
    { busy: createBusy },
  );
}

function cancelEdit() {
  editingId.value = "";
}

async function saveEdit(payload: AdminUpdateUserData) {
  if (!editingId.value) return;
  const id = editingId.value;
  await runAction(
    async () => {
      await adminUpdateUser(id, payload);
      cancelEdit();
      await reload();
    },
    { busy: editBusy },
  );
}

async function toggleDisabled(u: User) {
  await runAction(
    async () => {
      await adminSetDisabled(u.id, !u.disabled);
      await reload();
    },
    { busy: rowBusy },
  );
}

async function handleDelete(u: User) {
  if (
    !(await confirm({
      message: t("settings.adminUsers.confirmDelete", { username: u.username }),
      variant: "destructive",
    }))
  )
    return;
  await runAction(
    async () => {
      await adminDeleteUser(u.id);
      await reload();
    },
    { busy: rowBusy },
  );
}

function openPwReset(id: string) {
  pwTargetId.value = id;
  pwError.value = "";
}

function closePwReset() {
  pwTargetId.value = "";
  pwError.value = "";
}

async function submitPwReset(password: string) {
  if (!pwTargetId.value) return;
  const id = pwTargetId.value;
  batchMsg.value = "";
  // Closing is the only success signal the admin gets, so it has to be
  // conditional: a rejected reset keeps the dialog open with the reason.
  const done = await runPwReset(async () => {
    await adminSetPassword(id, password);
    return true;
  });
  if (done) pwTargetId.value = "";
}

onMounted(reload);
</script>

<template>
  <div class="mx-auto w-full max-w-[960px] px-4 pb-8">
    <SettingsDetailHeader
      :title="t('settings.adminUsers.title')"
      :eyebrow="t('settings.adminUsers.eyebrow')"
      parent-route-name="settings-admin"
    >
      <template #actions>
        <button
          class="px-3 py-1.5 rounded-md bg-primary text-primary-foreground text-[13px] font-medium hover:opacity-90 cursor-pointer"
          @click="showCreate = !showCreate"
        >
          {{ showCreate ? t("common.cancel") : t("settings.adminUsers.newUser") }}
        </button>
      </template>
    </SettingsDetailHeader>

    <p v-if="errorMsg" class="mb-3 text-[12px] text-red-600">{{ errorMsg }}</p>

    <AdminUsersCreateForm
      v-if="showCreate"
      ref="createFormRef"
      :provider-types="providerTypes"
      :provider-options-by-type="providerOptionsByType"
      :type-labels="typeLabels"
      :default-home-root="cfg?.default_home_root"
      :busy="createBusy"
      @submit="handleCreate"
      @cancel="showCreate = false"
    />

    <p
      v-if="batchMsg"
      class="mb-3 text-[12px] text-emerald-700 bg-emerald-50 border border-emerald-200 rounded px-2 py-1"
      data-testid="batch-model-status"
    >
      {{ batchMsg }}
    </p>

    <!-- Batch toolbar appears once at least one row is checked. -->
    <AdminUsersBatchToolbar
      v-if="selectedIds.size > 0"
      :selected-count="selectedIds.size"
      :model-groups="modelGroups"
      :provider-types="providerTypes"
      :provider-options-by-type="providerOptionsByType"
      :type-labels="typeLabels"
      :busy="batchBusy"
      @apply="applyBatch"
      @clear="selectedIds = new Set()"
    />

    <!-- List — three content columns: identity (name + flags + work dir),
         assignment (default model + account bindings) and an icon-only action
         group. Flags and model used to be columns of their own, which pushed
         the table past most viewports and forced every action label to wrap
         onto two lines. Still horizontally scrollable so the page itself never
         scrolls sideways on mobile. -->
    <div class="border border-border rounded-md overflow-x-auto bg-card wy-scroll">
      <table class="w-full min-w-[520px] text-[13px]">
        <thead class="bg-accent/50 text-muted-foreground text-[11px] uppercase">
          <tr>
            <th class="px-3 py-2 w-8">
              <input
                type="checkbox"
                :checked="allSelected"
                :aria-label="t('settings.adminUsers.batchApplyModel')"
                data-testid="select-all"
                @change="toggleSelectAll"
              />
            </th>
            <th class="text-left px-3 py-2">{{ t("settings.adminUsers.user") }}</th>
            <th class="text-left px-3 py-2">{{ t("settings.adminUsers.assignment") }}</th>
            <th class="text-right px-3 py-2 w-[132px]">
              {{ t("settings.adminUsers.actions") }}
            </th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="loading">
            <td colspan="4" class="px-3 py-4 text-center text-muted-foreground">
              {{ t("settings.adminUsers.loading") }}
            </td>
          </tr>
          <template v-for="u in users" :key="u.id">
            <tr v-if="editingId !== u.id" class="border-t border-border align-top">
              <td class="px-3 py-3 text-center">
                <input
                  type="checkbox"
                  :checked="selectedIds.has(u.id)"
                  :aria-label="u.username"
                  :data-testid="'select-' + u.id"
                  @change="toggleSelect(u.id)"
                />
              </td>
              <td class="px-3 py-3 max-w-[320px]">
                <div class="flex flex-wrap items-center gap-x-2 gap-y-1">
                  <span class="font-mono font-medium truncate">{{ u.username }}</span>
                  <span
                    v-if="u.name && u.name !== u.username"
                    class="text-muted-foreground truncate"
                  >
                    {{ u.name }}
                  </span>
                  <span
                    v-if="u.is_admin"
                    class="px-1.5 py-0.5 rounded bg-primary/15 text-primary text-[11px] leading-none"
                  >
                    {{ t("settings.adminUsers.statusAdmin") }}
                  </span>
                  <span
                    v-if="u.disabled"
                    class="px-1.5 py-0.5 rounded bg-red-500/15 text-red-700 text-[11px] leading-none"
                  >
                    {{ t("settings.adminUsers.statusDisabled") }}
                  </span>
                  <span
                    v-if="u.sandbox_mode === 'unrestricted'"
                    class="px-1.5 py-0.5 rounded bg-amber-500/15 text-amber-700 text-[11px] leading-none"
                    :title="t('settings.adminUsers.sandboxUnrestrictedHint')"
                    :data-testid="'sandbox-badge-' + u.id"
                  >
                    {{ t("settings.adminUsers.sandboxUnrestricted") }}
                  </span>
                </div>
                <div class="font-mono text-[11px] text-muted-foreground truncate">
                  {{ u.work_dir }}
                </div>
              </td>
              <td class="px-3 py-3">
                <div
                  :class="u.default_model ? 'font-mono' : 'text-muted-foreground'"
                  :data-testid="'default-model-' + u.id"
                >
                  {{ modelLabel(u.default_model) }}
                </div>
                <div class="text-[11px] text-muted-foreground" :data-testid="'bindings-' + u.id">
                  {{ summariseBindings(u) }}
                </div>
              </td>
              <td class="px-3 py-3">
                <div class="flex gap-1 justify-end">
                  <button
                    :class="iconBtn"
                    :title="t('settings.adminUsers.edit')"
                    :aria-label="t('settings.adminUsers.edit')"
                    :data-testid="'row-edit-' + u.id"
                    @click="editingId = u.id"
                  >
                    <Pencil class="size-4" />
                  </button>
                  <button
                    :class="iconBtn"
                    :title="t('settings.adminUsers.resetPassword')"
                    :aria-label="t('settings.adminUsers.resetPassword')"
                    :data-testid="'row-password-' + u.id"
                    @click="openPwReset(u.id)"
                  >
                    <KeyRound class="size-4" />
                  </button>
                  <button
                    :class="iconBtn"
                    :title="
                      u.disabled
                        ? t('settings.adminUsers.enable')
                        : t('settings.adminUsers.disable')
                    "
                    :aria-label="
                      u.disabled
                        ? t('settings.adminUsers.enable')
                        : t('settings.adminUsers.disable')
                    "
                    :data-testid="'row-toggle-' + u.id"
                    :disabled="u.id === authMe?.id || rowBusy"
                    @click="toggleDisabled(u)"
                  >
                    <CircleCheck v-if="u.disabled" class="size-4" />
                    <CircleSlash v-else class="size-4" />
                  </button>
                  <button
                    :class="[iconBtn, 'border-red-300 text-red-700 hover:bg-red-50']"
                    :title="t('common.delete')"
                    :aria-label="t('common.delete')"
                    :data-testid="'row-delete-' + u.id"
                    :disabled="u.id === authMe?.id || rowBusy"
                    @click="handleDelete(u)"
                  >
                    <Trash2 class="size-4" />
                  </button>
                </div>
              </td>
            </tr>
            <AdminUsersEditRow
              v-else
              :user="u"
              :provider-types="providerTypes"
              :provider-options-by-type="providerOptionsByType"
              :type-labels="typeLabels"
              :model-groups="modelGroups"
              :is-self="u.id === authMe?.id"
              :busy="editBusy"
              @save="saveEdit"
              @cancel="cancelEdit"
            />
          </template>
          <tr v-if="!loading && users.length === 0">
            <td colspan="4" class="px-3 py-4 text-center text-muted-foreground">
              {{ t("settings.adminUsers.noUsers") }}
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <AdminUsersPasswordDialog
      :open="!!pwTargetId"
      :busy="pwBusy"
      :error="pwError"
      @submit="submitPwReset"
      @close="closePwReset"
    />
  </div>
</template>
