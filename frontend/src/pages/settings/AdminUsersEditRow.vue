<script setup lang="ts">
// Inline edit row for one user. Seeds a private draft from the `user` prop so
// cancelling never writes through to the list, and emits the finished payload
// on save. Only one row is ever open, so the parent mounts exactly one of
// these at a time and the draft is re-seeded by the fresh mount.
import { computed, ref } from "vue";
import { useI18n } from "vue-i18n";
import type { AdminUpdateUserData, User } from "@/composables/useApi";
import DirPicker from "@/components/DirPicker.vue";

const props = defineProps<{
  user: User;
  providerTypes: string[];
  providerOptionsByType: Record<string, string[]>;
  typeLabels: Record<string, string>;
  modelGroups: { provider: string; models: string[] }[];
  /** True when the row is the signed-in admin — they may not demote themselves. */
  isSelf: boolean;
  busy: boolean;
}>();

const emit = defineEmits<{ save: [AdminUpdateUserData]; cancel: [] }>();

const { t } = useI18n();

// Seed the per-type account SET from the server (default first), falling back
// to the single-binding map.
function seedAccounts(u: User): Record<string, string[]> {
  if (u.provider_accounts) {
    return Object.fromEntries(Object.entries(u.provider_accounts).map(([k, v]) => [k, [...v]]));
  }
  if (u.provider_bindings) {
    return Object.fromEntries(
      Object.entries(u.provider_bindings)
        .filter(([, v]) => v)
        .map(([k, v]) => [k, [v]]),
    );
  }
  return {};
}

const form = ref<AdminUpdateUserData>({
  name: props.user.name,
  email: props.user.email,
  work_dir: props.user.work_dir,
  provider_accounts: seedAccounts(props.user),
  is_admin: props.user.is_admin,
  default_model: props.user.default_model,
});

// DirPicker expects a non-optional string; work_dir is optional in the API type.
const workDir = computed<string>({
  get: () => form.value.work_dir ?? "",
  set: (v) => {
    form.value.work_dir = v;
  },
});

// The array is stored default-first; the backend reads element 0 as the
// default account for that CLI type.
function accountsFor(ptype: string): string[] {
  return form.value.provider_accounts?.[ptype] ?? [];
}
function isAccountGranted(ptype: string, name: string): boolean {
  return accountsFor(ptype).includes(name);
}
function isDefaultAccount(ptype: string, name: string): boolean {
  return accountsFor(ptype)[0] === name;
}
function toggleAccount(ptype: string, name: string) {
  if (!form.value.provider_accounts) form.value.provider_accounts = {};
  const cur = accountsFor(ptype);
  form.value.provider_accounts[ptype] = cur.includes(name)
    ? cur.filter((n) => n !== name)
    : [...cur, name];
}
function setDefaultAccount(ptype: string, name: string) {
  if (!form.value.provider_accounts) form.value.provider_accounts = {};
  const cur = accountsFor(ptype);
  if (!cur.includes(name)) return;
  form.value.provider_accounts[ptype] = [name, ...cur.filter((n) => n !== name)];
}

function onSave() {
  if (props.busy) return;
  emit("save", {
    ...form.value,
    // provider_accounts is authoritative; shipping the single-value map
    // alongside it would make precedence ambiguous.
    provider_bindings: undefined,
  });
}
</script>

<template>
  <tr class="border-t border-border bg-accent/40">
    <td colspan="7" class="px-3 py-3">
      <div class="grid grid-cols-2 gap-3">
        <label class="block">
          <span class="text-[11px] text-muted-foreground">{{ t("settings.adminUsers.name") }}</span>
          <input
            v-model="form.name"
            class="w-full mt-1 px-2 py-1 border border-border rounded bg-background text-[13px]"
          />
        </label>
        <label class="block">
          <span class="text-[11px] text-muted-foreground">{{
            t("settings.adminUsers.email")
          }}</span>
          <input
            v-model="form.email"
            class="w-full mt-1 px-2 py-1 border border-border rounded bg-background text-[13px]"
          />
        </label>
        <div class="col-span-2">
          <span class="text-[11px] text-muted-foreground">{{
            t("settings.adminUsers.workDirAbsolute")
          }}</span>
          <div class="mt-1">
            <DirPicker v-model="workDir" />
          </div>
        </div>
        <!-- Per-CLI-type account grants. Each account can be granted
             (checkbox); the radio marks the type's default account, which
             unpinned conversations + auto-title resolve to. -->
        <div v-for="ptype in providerTypes" :key="'edit-' + ptype" class="block">
          <span class="text-[11px] text-muted-foreground">{{ typeLabels[ptype] }}</span>
          <div class="mt-1 space-y-1 rounded border border-border bg-background p-2">
            <div
              v-for="n in providerOptionsByType[ptype]"
              :key="n"
              class="flex items-center justify-between gap-2 text-[13px]"
            >
              <label class="flex items-center gap-2">
                <input
                  type="checkbox"
                  :data-testid="'edit-account-' + ptype + '-' + n"
                  :checked="isAccountGranted(ptype, n)"
                  @change="toggleAccount(ptype, n)"
                />
                <span>{{ n }}</span>
              </label>
              <label
                v-if="isAccountGranted(ptype, n)"
                class="flex items-center gap-1 text-[11px] text-muted-foreground"
              >
                <input
                  type="radio"
                  :name="'edit-default-' + ptype"
                  :data-testid="'edit-default-account-' + ptype + '-' + n"
                  :checked="isDefaultAccount(ptype, n)"
                  @change="setDefaultAccount(ptype, n)"
                />
                {{ t("settings.adminUsers.defaultAccount") }}
              </label>
            </div>
            <p
              v-if="(providerOptionsByType[ptype]?.length ?? 0) === 0"
              class="text-[11px] text-muted-foreground"
            >
              {{ t("common.none") }}
            </p>
          </div>
        </div>
        <label class="block">
          <span class="text-[11px] text-muted-foreground">{{
            t("settings.adminUsers.defaultModel")
          }}</span>
          <select
            v-model="form.default_model"
            data-testid="edit-default-model"
            class="w-full mt-1 px-2 py-1 border border-border rounded bg-background text-[13px]"
          >
            <option value="">{{ t("settings.adminUsers.noDefaultModel") }}</option>
            <optgroup v-for="g in modelGroups" :key="g.provider" :label="g.provider">
              <option v-for="m in g.models" :key="m" :value="m">{{ m }}</option>
            </optgroup>
          </select>
          <span class="text-[10px] text-muted-foreground">{{
            t("settings.adminUsers.defaultModelHint")
          }}</span>
        </label>
        <label class="flex items-center gap-2 mt-5">
          <input v-model="form.is_admin" type="checkbox" :disabled="isSelf" />
          <span class="text-[13px]">{{ t("settings.adminUsers.admin") }}</span>
        </label>
      </div>

      <div class="flex gap-2 mt-3 justify-end">
        <button
          class="px-3 py-1.5 rounded-md border border-border text-[13px] cursor-pointer disabled:opacity-50"
          :disabled="busy"
          @click="emit('cancel')"
        >
          {{ t("common.cancel") }}
        </button>
        <button
          class="px-3 py-1.5 rounded-md bg-primary text-primary-foreground text-[13px] cursor-pointer disabled:opacity-50"
          :disabled="busy"
          @click="onSave"
        >
          {{ t("common.save") }}
        </button>
      </div>
    </td>
  </tr>
</template>
