<script setup lang="ts">
import { Button } from "@/components/ui/button";
import { ref } from "vue";
import { useI18n } from "vue-i18n";

const { t } = useI18n();

// `action` is the verb that goes on the rename button ("重命名后<action>").
// Defaults to "移动" since the dialog was originally written for the move
// flow; upload reuses it with action="上传".
//
// `remaining` (upload only) is how many files are still queued behind this
// one. When it is set the dialog asks about just this file: Cancel becomes
// Skip, a separate button cancels the whole batch, and an "apply to all"
// checkbox (shown only when files remain) carries the choice forward.
const props = withDefaults(
  defineProps<{
    fileName: string;
    targetFolder: string;
    action?: string;
    remaining?: number;
  }>(),
  { action: "", remaining: undefined },
);

const emit = defineEmits<{
  cancel: [];
  skip: [applyAll: boolean];
  cancelAll: [];
  rename: [applyAll: boolean];
  overwrite: [applyAll: boolean];
}>();

const applyAll = ref(false);

// The backdrop dismisses only this conflict: a stray click must not throw
// away the rest of a batch.
function dismiss() {
  if (props.remaining === undefined) emit("cancel");
  else emit("skip", false);
}
</script>

<template>
  <div
    class="conflict-overlay fixed inset-0 bg-black/30 z-[1001] flex items-center justify-center p-4"
    @click.self="dismiss"
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
        <label v-if="remaining" class="flex items-center gap-2 text-muted-foreground">
          <input
            v-model="applyAll"
            type="checkbox"
            class="conflict-apply-all size-4 shrink-0 rounded border-input accent-primary"
          />
          <span>{{ t("files.conflict.applyAll", { n: remaining }) }}</span>
        </label>
      </div>
      <div class="px-4 pb-3 flex justify-end gap-2 flex-wrap">
        <template v-if="remaining !== undefined">
          <Button
            v-if="remaining"
            variant="ghost"
            size="sm"
            class="conflict-btn-cancel-all mr-auto"
            @click="emit('cancelAll')"
          >
            {{ t("files.conflict.cancelAll") }}
          </Button>
          <Button
            variant="outline"
            size="sm"
            class="conflict-btn-skip"
            @click="emit('skip', applyAll)"
          >
            {{ t("files.conflict.skip") }}
          </Button>
        </template>
        <Button
          v-else
          variant="outline"
          size="sm"
          class="conflict-btn-cancel"
          @click="emit('cancel')"
        >
          {{ t("common.cancel") }}
        </Button>
        <Button
          variant="outline"
          size="sm"
          class="conflict-btn-rename"
          @click="emit('rename', applyAll)"
        >
          {{
            t("files.conflict.renameAction", { action: action || t("files.conflict.moveAction") })
          }}
        </Button>
        <Button
          variant="default"
          size="sm"
          class="conflict-btn-overwrite"
          @click="emit('overwrite', applyAll)"
        >
          {{ t("files.conflict.overwrite") }}
        </Button>
      </div>
    </div>
  </div>
</template>
