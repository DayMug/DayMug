<script setup lang="ts">
import { computed, ref } from "vue";
import { useRouter } from "vue-router";
import { useI18n } from "vue-i18n";
import { useUsers } from "@/composables/useUsers";
import UserForm from "@/components/UserForm.vue";
import type { UserFormData } from "@/components/UserForm.vue";
import SettingsDetailHeader from "./SettingsDetailHeader.vue";
import { usePageTitle } from "@/composables/useDocumentTitle";

const router = useRouter();
const { t } = useI18n();
const { handleCreateUser } = useUsers();
usePageTitle(computed(() => t("settings.titles.newAgent")));

const submitting = ref(false);

async function onSubmit(data: UserFormData) {
  if (submitting.value) return;
  submitting.value = true;
  try {
    await handleCreateUser({
      name: data.name,
      work_dir: data.work_dir,
      avatar: data.avatar,
      role_definition: data.role_definition,
      default_model: data.default_model,
      think_level: data.think_level,
      case_mode: data.case_mode,
    });
    router.push({ name: "settings" });
  } finally {
    submitting.value = false;
  }
}

function onCancel() {
  router.push({ name: "settings" });
}
</script>

<template>
  <div class="mx-auto w-full max-w-[860px] px-4 pb-8">
    <SettingsDetailHeader
      :title="t('settings.titles.newAgent')"
      :eyebrow="t('settings.groups.agents')"
    />
    <UserForm mode="add" :submitting="submitting" @submit="onSubmit" @cancel="onCancel" />
  </div>
</template>
