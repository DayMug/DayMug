<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import {
  FolderOpen,
  LayoutGrid,
  Pencil,
  Plus,
  Search,
  Settings,
  Trash2,
  UserRound,
  X,
} from "lucide-vue-next";
import { DialogDescription, DialogTitle } from "reka-ui";
import { Dialog, DialogContent } from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useConfirm } from "@/composables/useConfirm";
import {
  createMarketplaceApp,
  deleteMarketplaceApp,
  listMarketplaceApps,
  updateMarketplaceApp,
  type MarketplaceApp,
} from "@/composables/apiMarketplace";

const props = defineProps<{ open: boolean }>();

const emit = defineEmits<{
  "update:open": [value: boolean];
}>();

interface VisitRecord {
  count: number;
  lastVisited: number;
}

const VISITS_KEY = "daymug.marketplace.visits";
const { t } = useI18n();
const { confirm } = useConfirm();
const apps = ref<MarketplaceApp[]>([]);
const visits = ref<Record<string, VisitRecord>>(readVisits());
const query = ref("");
const loading = ref(false);
const saving = ref(false);
const deletingID = ref("");
const error = ref("");
const manageMode = ref(false);
const formMode = ref<"" | "create" | "edit">("");
const editingID = ref("");
const brokenIcons = ref(new Set<string>());
const form = ref({ name: "", description: "", url: "", icon_url: "", deploy_dir: "" });

function readVisits(): Record<string, VisitRecord> {
  try {
    const parsed = JSON.parse(localStorage.getItem(VISITS_KEY) ?? "{}") as Record<
      string,
      Partial<VisitRecord>
    >;
    return Object.fromEntries(
      Object.entries(parsed).flatMap(([id, value]) => {
        const count = Number(value?.count);
        const lastVisited = Number(value?.lastVisited);
        return Number.isFinite(count) && count > 0
          ? [[id, { count, lastVisited: Number.isFinite(lastVisited) ? lastVisited : 0 }]]
          : [];
      }),
    );
  } catch {
    return {};
  }
}

function saveVisits() {
  try {
    localStorage.setItem(VISITS_KEY, JSON.stringify(visits.value));
  } catch {
    // A blocked/full localStorage should not prevent opening an app.
  }
}

const visibleApps = computed(() => {
  const needle = query.value.trim().toLocaleLowerCase();
  return [...apps.value]
    .filter((app) => {
      if (!needle) return true;
      return [app.name, app.description, app.url, app.deploy_dir, app.created_by_name].some(
        (value) => value.toLocaleLowerCase().includes(needle),
      );
    })
    .sort((a, b) => {
      const aVisit = visits.value[a.id] ?? { count: 0, lastVisited: 0 };
      const bVisit = visits.value[b.id] ?? { count: 0, lastVisited: 0 };
      return (
        bVisit.count - aVisit.count ||
        bVisit.lastVisited - aVisit.lastVisited ||
        a.name.localeCompare(b.name)
      );
    });
});

async function loadApps() {
  loading.value = true;
  error.value = "";
  try {
    apps.value = await listMarketplaceApps();
  } catch {
    error.value = t("marketplace.errors.load");
  } finally {
    loading.value = false;
  }
}

watch(
  () => props.open,
  (open) => {
    if (!open) return;
    query.value = "";
    manageMode.value = false;
    closeForm();
    visits.value = readVisits();
    void loadApps();
  },
  { immediate: true },
);

function toggleManageMode() {
  manageMode.value = !manageMode.value;
  closeForm();
  error.value = "";
}

function openCreateForm() {
  formMode.value = "create";
  editingID.value = "";
  form.value = { name: "", description: "", url: "", icon_url: "", deploy_dir: "" };
  manageMode.value = false;
  error.value = "";
}

function openEditForm(app: MarketplaceApp) {
  formMode.value = "edit";
  editingID.value = app.id;
  form.value = {
    name: app.name,
    description: app.description,
    url: app.url,
    icon_url: app.icon_url,
    deploy_dir: app.deploy_dir,
  };
  error.value = "";
}

function closeForm() {
  form.value = { name: "", description: "", url: "", icon_url: "", deploy_dir: "" };
  formMode.value = "";
  editingID.value = "";
}

async function submitApp() {
  if (saving.value) return;
  const editing = editingID.value;
  saving.value = true;
  error.value = "";
  const input = {
    name: form.value.name,
    description: form.value.description,
    url: form.value.url,
    icon_url: form.value.icon_url || undefined,
    deploy_dir: form.value.deploy_dir || undefined,
  };
  try {
    if (editing) {
      const updated = await updateMarketplaceApp(editing, input);
      apps.value = apps.value.map((item) => (item.id === editing ? updated : item));
      // A replaced icon URL deserves a fresh load attempt.
      brokenIcons.value.delete(editing);
    } else {
      apps.value = [await createMarketplaceApp(input), ...apps.value];
    }
    closeForm();
  } catch {
    error.value = t(editing ? "marketplace.errors.update" : "marketplace.errors.create");
  } finally {
    saving.value = false;
  }
}

function visit(app: MarketplaceApp) {
  const previous = visits.value[app.id] ?? { count: 0, lastVisited: 0 };
  visits.value = {
    ...visits.value,
    [app.id]: { count: previous.count + 1, lastVisited: Date.now() },
  };
  saveVisits();
  window.open(app.url, "_blank", "noopener,noreferrer");
}

async function remove(app: MarketplaceApp) {
  const accepted = await confirm({
    title: t("marketplace.deleteTitle"),
    message: t("marketplace.deleteConfirm", { name: app.name }),
    confirmText: t("common.delete"),
    variant: "destructive",
  });
  if (!accepted) return;

  deletingID.value = app.id;
  error.value = "";
  try {
    await deleteMarketplaceApp(app.id);
    apps.value = apps.value.filter((item) => item.id !== app.id);
    const nextVisits = { ...visits.value };
    delete nextVisits[app.id];
    visits.value = nextVisits;
    saveVisits();
  } catch {
    error.value = t("marketplace.errors.delete");
  } finally {
    deletingID.value = "";
  }
}

function markIconBroken(id: string) {
  brokenIcons.value.add(id);
}
</script>

<template>
  <Dialog :open="open" @update:open="emit('update:open', $event)">
    <DialogContent
      :show-close-button="false"
      class="flex max-h-[82vh] flex-col gap-0 overflow-hidden p-0 sm:max-w-3xl lg:max-w-5xl xl:max-w-6xl"
      data-testid="marketplace-dialog"
    >
      <DialogTitle class="sr-only">{{ t("marketplace.title") }}</DialogTitle>
      <DialogDescription class="sr-only">{{ t("marketplace.description") }}</DialogDescription>

      <header class="flex items-center gap-3 border-b border-border px-4 py-3">
        <div class="flex min-w-0 flex-1 items-center gap-2">
          <span
            class="flex size-9 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary"
          >
            <LayoutGrid class="size-5" />
          </span>
          <div class="min-w-0">
            <h2 class="truncate text-base font-semibold">{{ t("marketplace.title") }}</h2>
            <p class="truncate text-xs text-muted-foreground">{{ t("marketplace.description") }}</p>
          </div>
        </div>
        <Button
          variant="ghost"
          size="icon"
          :aria-label="t('marketplace.add')"
          data-testid="marketplace-add"
          @click="openCreateForm"
        >
          <Plus class="size-5" />
        </Button>
        <Button
          variant="ghost"
          size="icon"
          :class="manageMode ? 'bg-accent text-accent-foreground' : ''"
          :aria-label="t('marketplace.manage')"
          data-testid="marketplace-settings"
          @click="toggleManageMode"
        >
          <Settings class="size-5" />
        </Button>
        <Button
          variant="ghost"
          size="icon"
          :aria-label="t('common.close')"
          @click="emit('update:open', false)"
        >
          <X class="size-5" />
        </Button>
      </header>

      <form
        v-if="formMode"
        class="grid min-h-0 flex-1 content-start gap-3 overflow-y-auto bg-muted/25 p-4 sm:grid-cols-2"
        data-testid="marketplace-app-form"
        @submit.prevent="submitApp"
      >
        <div>
          <label for="marketplace-name" class="mb-1 block text-xs font-medium">{{
            t("marketplace.fields.name")
          }}</label>
          <Input id="marketplace-name" v-model="form.name" maxlength="100" required />
        </div>
        <div>
          <label for="marketplace-url" class="mb-1 block text-xs font-medium">{{
            t("marketplace.fields.url")
          }}</label>
          <Input id="marketplace-url" v-model="form.url" type="url" maxlength="2048" required />
        </div>
        <div class="sm:col-span-2">
          <label for="marketplace-description" class="mb-1 block text-xs font-medium">{{
            t("marketplace.fields.description")
          }}</label>
          <textarea
            id="marketplace-description"
            v-model="form.description"
            maxlength="500"
            required
            class="min-h-20 w-full resize-y rounded-md border border-input bg-background px-3 py-2 text-sm outline-none focus-visible:ring-1 focus-visible:ring-ring"
          />
        </div>
        <div class="sm:col-span-2">
          <label for="marketplace-icon-url" class="mb-1 block text-xs font-medium">{{
            t("marketplace.fields.iconUrl")
          }}</label>
          <Input id="marketplace-icon-url" v-model="form.icon_url" type="url" maxlength="2048" />
        </div>
        <div class="sm:col-span-2">
          <label for="marketplace-deploy-dir" class="mb-1 block text-xs font-medium">{{
            t("marketplace.fields.deployDir")
          }}</label>
          <Input id="marketplace-deploy-dir" v-model="form.deploy_dir" maxlength="2048" />
        </div>
        <p v-if="error" role="alert" class="text-sm text-destructive sm:col-span-2">{{ error }}</p>
        <div class="flex justify-end gap-2 sm:col-span-2">
          <Button type="button" variant="outline" @click="closeForm">{{
            t("common.cancel")
          }}</Button>
          <Button type="submit" :disabled="saving">{{
            saving
              ? t("common.saving")
              : formMode === "edit"
                ? t("common.save")
                : t("common.create")
          }}</Button>
        </div>
      </form>

      <div v-if="!formMode" class="border-b border-border p-3">
        <label class="relative block">
          <Search class="absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            v-model="query"
            class="pl-9"
            :placeholder="t('marketplace.searchPlaceholder')"
            data-testid="marketplace-search"
          />
        </label>
      </div>

      <div v-if="!formMode" class="min-h-0 flex-1 overflow-y-auto p-4">
        <p v-if="error" role="alert" class="mb-3 text-sm text-destructive">{{ error }}</p>
        <p v-if="loading" class="py-10 text-center text-sm text-muted-foreground">
          {{ t("common.loading") }}
        </p>
        <p
          v-else-if="visibleApps.length === 0"
          class="py-10 text-center text-sm text-muted-foreground"
        >
          {{ query ? t("marketplace.noResults") : t("marketplace.empty") }}
        </p>
        <div
          v-else
          class="grid gap-3 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4"
          data-testid="marketplace-list"
        >
          <article
            v-for="app in visibleApps"
            :key="app.id"
            class="group relative flex min-w-0 items-start gap-3 rounded-lg border border-border p-3 transition-colors hover:bg-accent/50"
            data-testid="marketplace-app"
          >
            <button
              type="button"
              class="flex min-w-0 flex-1 items-start gap-3 text-left"
              @click="visit(app)"
            >
              <span
                class="relative flex size-10 shrink-0 items-center justify-center overflow-hidden rounded-lg bg-muted text-sm font-semibold text-muted-foreground"
              >
                <span>{{ app.name.slice(0, 1).toLocaleUpperCase() }}</span>
                <img
                  v-if="app.icon_url && !brokenIcons.has(app.id)"
                  :src="app.icon_url"
                  :alt="''"
                  class="absolute inset-0 size-full object-cover"
                  @error="markIconBroken(app.id)"
                />
              </span>
              <span class="min-w-0 flex-1">
                <span class="block truncate text-sm font-semibold">{{ app.name }}</span>
                <span class="mt-0.5 line-clamp-2 block text-xs leading-5 text-muted-foreground">{{
                  app.description
                }}</span>
                <span class="mt-1.5 flex items-center gap-1 text-[11px] text-muted-foreground">
                  <UserRound class="size-3 shrink-0" />
                  <span class="truncate">{{
                    t("marketplace.publisher", { name: app.created_by_name || app.created_by })
                  }}</span>
                </span>
                <span
                  v-if="app.deploy_dir"
                  class="mt-1 flex items-center gap-1 text-[11px] text-muted-foreground"
                >
                  <FolderOpen class="size-3 shrink-0" />
                  <span class="truncate" :title="app.deploy_dir">{{
                    t("marketplace.deployDir", { path: app.deploy_dir })
                  }}</span>
                </span>
              </span>
            </button>
            <Button
              v-if="manageMode"
              variant="ghost"
              size="icon"
              class="size-8 shrink-0"
              :aria-label="t('marketplace.editApp', { name: app.name })"
              data-testid="marketplace-edit"
              @click="openEditForm(app)"
            >
              <Pencil class="size-4" />
            </Button>
            <Button
              v-if="manageMode"
              variant="ghost"
              size="icon"
              class="size-8 shrink-0 text-destructive hover:bg-destructive/10 hover:text-destructive"
              :disabled="deletingID === app.id"
              :aria-label="t('marketplace.deleteApp', { name: app.name })"
              data-testid="marketplace-delete"
              @click="remove(app)"
            >
              <Trash2 class="size-4" />
            </Button>
          </article>
        </div>
      </div>
    </DialogContent>
  </Dialog>
</template>
