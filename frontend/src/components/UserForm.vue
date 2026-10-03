<script setup lang="ts">
import { ref, watch, onMounted, computed } from "vue";
import { ChevronDown } from "lucide-vue-next";
import DirPicker from "@/components/DirPicker.vue";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { useAuth } from "@/composables/useAuth";
import { loadModelRegistry } from "@/composables/useModelRegistry";
import type { ModelRegistry } from "@/composables/apiTypes";
import { displayPath } from "@/lib/displayPath";
import { useI18n } from "vue-i18n";

const { authMe } = useAuth();
const { t } = useI18n();
// The DirPicker is jailed to authMe.work_dir — admins and non-admins
// alike. The form falls back to a read-only display only when the human
// owner has no work_dir at all (an admin needs to set one before agents
// can be created).
const ownerHome = computed(() => authMe.value?.work_dir ?? "");
const canPick = computed(() => ownerHome.value !== "");

// The retired Advanced editor's values remain in the form payload as
// pass-through fields. Existing agents may still carry MCP / CLAUDE.md config,
// and saving an unrelated Basic or Role change must not silently erase it.
export interface UserFormData {
  name: string;
  work_dir: string;
  avatar: string;
  role_definition: string;
  default_model?: string;
  think_level?: string;
  case_mode?: boolean;
  mcp_config: string;
  claude_md_content: string;
  manage_claude_md: boolean;
}

const props = withDefaults(
  defineProps<{
    mode: "add" | "edit";
    initialData?: UserFormData;
    submitting?: boolean;
    activeSection?: 1 | 2 | 3;
  }>(),
  { submitting: false, initialData: undefined, activeSection: undefined },
);

const emit = defineEmits<{
  submit: [data: UserFormData];
  cancel: [];
}>();

const name = ref("");
const workDir = ref("");
const avatar = ref("");
const roleDefinition = ref("");
const defaultModel = ref("");
// Kept only as a pass-through for agents configured through the API. Reasoning
// effort is selected per conversation in /chat and is no longer editable on
// the Agent settings page; an unrelated edit must still preserve the stored
// default-model profile.
const persistedThinkLevel = ref("");
const caseMode = ref(false);
const mcpConfig = ref("");
const claudeMdContent = ref("");
const manageClaudeMd = ref(false);
const modelRegistry = ref<ModelRegistry | null>(null);

// In add mode, default work_dir to the signed-in human's home — the same
// jail root the picker (and backend) restrict to. Admins and non-admins
// follow the same rule now: agents always live inside their owning
// human's directory tree.
onMounted(() => {
  if (props.mode === "add" && !workDir.value) workDir.value = ownerHome.value;
  void loadModelRegistry()
    .then((registry) => {
      modelRegistry.value = registry;
    })
    .catch(() => {
      modelRegistry.value = null;
    });
});

const modelGroups = computed(() =>
  (modelRegistry.value?.providers ?? []).map((provider) => ({
    provider: provider.name,
    models: provider.models,
  })),
);

function loadData(data: UserFormData) {
  name.value = data.name;
  workDir.value = data.work_dir;
  avatar.value = data.avatar;
  roleDefinition.value = data.role_definition;
  defaultModel.value = data.default_model || "";
  persistedThinkLevel.value = data.think_level || "";
  caseMode.value = data.case_mode ?? false;
  mcpConfig.value = data.mcp_config;
  claudeMdContent.value = data.claude_md_content;
  manageClaudeMd.value = data.manage_claude_md;
}

watch(
  () => props.initialData,
  (data) => {
    if (data) loadData(data);
  },
  { immediate: true },
);

function handleSubmit() {
  if (props.submitting) return;
  const n = name.value.trim();
  const w = workDir.value.trim();
  if (!n || !w) return;
  emit("submit", {
    name: n,
    work_dir: w,
    avatar: avatar.value.trim(),
    role_definition: roleDefinition.value.trim(),
    default_model: defaultModel.value.trim(),
    think_level: persistedThinkLevel.value,
    case_mode: caseMode.value,
    mcp_config: mcpConfig.value.trim(),
    claude_md_content: claudeMdContent.value.trim(),
    manage_claude_md: manageClaudeMd.value,
  });
}

const isValid = ref(false);
watch(
  [name, workDir],
  () => {
    isValid.value = !!name.value.trim() && !!workDir.value.trim();
  },
  { immediate: true },
);
</script>

<template>
  <!-- Editorial layout: each section is a 2-column grid where the left
       column carries an oversize Roman numeral + serif-italic title +
       muted subtitle, and the right column holds the form fields. On
       narrow viewports the grid collapses to a single column so the
       title still leads, just stacked. -->
  <div class="max-w-[860px]">
    <!-- Section I — Basic identity and workspace fields. -->
    <section
      v-show="!activeSection || activeSection === 1"
      id="form-panel-1"
      class="form-section"
      data-form-panel="1"
      :role="activeSection ? 'tabpanel' : undefined"
      :aria-labelledby="activeSection ? 'form-tab-1' : undefined"
    >
      <header class="form-section-head">
        <div class="form-section-roman">I.</div>
        <div class="form-section-title">{{ t("settings.agentForm.basic") }}</div>
        <div class="form-section-sub">{{ t("settings.agentForm.basicSub") }}</div>
      </header>
      <div class="form-section-body">
        <div class="grid gap-4 md:grid-cols-2">
          <div>
            <label class="field-label">{{ t("settings.agentForm.displayName") }}</label>
            <Input v-model="name" :placeholder="t('settings.agentForm.usernamePlaceholder')" />
          </div>
          <div>
            <label class="field-label">{{ t("settings.agentForm.avatar") }}</label>
            <Input v-model="avatar" :placeholder="t('settings.agentForm.avatarPlaceholder')" />
          </div>
        </div>
        <div>
          <label class="field-label"
            >{{ t("settings.agentForm.workingDirectory") }}
            <span class="font-normal normal-case tracking-normal opacity-70">{{
              t("settings.agentForm.workingDirectoryHelp")
            }}</span>
          </label>
          <DirPicker v-if="canPick" v-model="workDir" :root-path="ownerHome" />
          <div
            v-else
            class="px-2 py-1 border border-border rounded bg-accent/30 text-[12px] text-muted-foreground font-mono"
          >
            {{ displayPath(workDir) }}
            <span class="text-[11px] italic opacity-70">
              {{ t("settings.agentForm.noHomeDirectory") }}
            </span>
          </div>
        </div>
        <div>
          <label class="field-label">
            {{ t("settings.agentForm.defaultModel") }}
            <span class="font-normal normal-case tracking-normal opacity-70">
              {{ t("settings.agentForm.optional") }}
            </span>
          </label>
          <div class="relative">
            <select
              v-model="defaultModel"
              data-testid="default-model"
              class="h-9 w-full appearance-none rounded-md border border-input bg-background px-3 pr-12 text-sm"
            >
              <option value="">{{ t("settings.agentForm.noDefaultModel") }}</option>
              <optgroup v-for="group in modelGroups" :key="group.provider" :label="group.provider">
                <option v-for="model in group.models" :key="model" :value="model">
                  {{ model }}
                </option>
              </optgroup>
            </select>
            <ChevronDown
              data-testid="default-model-chevron"
              aria-hidden="true"
              class="pointer-events-none absolute right-4 top-1/2 size-4 -translate-y-1/2 text-foreground"
            />
          </div>
          <p class="mt-1.5 text-[11px] leading-relaxed text-muted-foreground">
            {{ t("settings.agentForm.defaultModelHelp") }}
          </p>
        </div>
        <div>
          <label class="flex items-start gap-2.5">
            <input
              v-model="caseMode"
              type="checkbox"
              data-testid="case-mode"
              class="mt-0.5 size-4 shrink-0 rounded border-input accent-primary"
            />
            <span class="field-label mb-0">{{ t("settings.agentForm.caseMode") }}</span>
          </label>
          <p class="mt-1.5 text-[11px] leading-relaxed text-muted-foreground">
            {{ t("settings.agentForm.caseModeHelp") }}
          </p>
        </div>
      </div>
    </section>

    <!-- Section II — Role & persona -->
    <section
      v-show="!activeSection || activeSection === 2"
      id="form-panel-2"
      class="form-section"
      data-form-panel="2"
      :role="activeSection ? 'tabpanel' : undefined"
      :aria-labelledby="activeSection ? 'form-tab-2' : undefined"
    >
      <header class="form-section-head">
        <div class="form-section-roman">II.</div>
        <div class="form-section-title">{{ t("settings.agentForm.rolePersona") }}</div>
        <div class="form-section-sub">{{ t("settings.agentForm.rolePersonaSub") }}</div>
      </header>
      <div class="form-section-body">
        <div>
          <label class="field-label">{{ t("settings.agentForm.roleDefinition") }}</label>
          <Textarea
            v-model="roleDefinition"
            class="resize-y font-mono text-[13px]"
            :placeholder="t('settings.agentForm.rolePlaceholder')"
            :rows="mode === 'edit' ? 6 : 3"
          />
        </div>
      </div>
    </section>

    <div
      v-show="!activeSection || activeSection === 3"
      id="form-panel-3"
      data-form-panel="3"
      :role="activeSection ? 'tabpanel' : undefined"
      :aria-labelledby="activeSection ? 'form-tab-3' : undefined"
    >
      <slot name="integrations"></slot>
    </div>

    <div class="flex items-center gap-2.5 mt-7">
      <Button :disabled="!isValid || submitting" @click="handleSubmit">
        {{ mode === "add" ? t("common.add") : t("common.save") }}
      </Button>
      <Button variant="outline" :disabled="submitting" @click="emit('cancel')">{{
        t("common.cancel")
      }}</Button>
      <slot name="actions"></slot>
    </div>
  </div>
</template>
