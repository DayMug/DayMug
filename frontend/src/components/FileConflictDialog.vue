<script setup lang="ts">
import { Button } from "@/components/ui/button";
import { useI18n } from "vue-i18n";

const { t } = useI18n();

// `action` is the verb that goes on the rename button ("重命名后<action>").
// Defaults to "移动" since the dialog was originally written for the move
// flow; upload reuses it with action="上传".
withDefaults(
  defineProps<{
    fileName: string;
    targetFolder: string;
    action?: string;
  }>(),
  { action: "" },
);

const emit = defineEmits<{
  cancel: [];
  rename: [];
  overwrite: [];
}>();
</script>

<template>
  <div
    class="conflict-overlay fixed inset-0 bg-black/30 z-[1001] flex items-center justify-center p-4"
    @click.self="emit('cancel')"
  >
    <div
      class="bg-card border border-border rounded-lg shadow-2xl w-full max-w-[460px] max-h-[calc(100vh-2rem)] overflow-y-auto text-[13px]"
    >
      <div class="px-4 py-3 border-b border-border">
        <span class="font-semibold text-foreground">{{ t("files.conflict.title") }}</span>
      </div>
      <div class="px-4 py-3 space-y-2">
        <div class="text-foreground">
          {{
            t("files.conflict.message", {
              target: targetFolder,
              file: fileName,
            })
          }}
        </div>
      </div>
      <div class="px-4 pb-3 flex justify-end gap-2 flex-wrap">
        <Button variant="outline" size="sm" class="conflict-btn-cancel" @click="emit('cancel')">
          {{ t("common.cancel") }}
        </Button>
        <Button variant="outline" size="sm" class="conflict-btn-rename" @click="emit('rename')">
          {{
            t("files.conflict.renameAction", { action: action || t("files.conflict.moveAction") })
          }}
        </Button>
        <Button
          variant="default"
          size="sm"
          class="conflict-btn-overwrite"
          @click="emit('overwrite')"
        >
          {{ t("files.conflict.overwrite") }}
        </Button>
      </div>
    </div>
  </div>
</template>
