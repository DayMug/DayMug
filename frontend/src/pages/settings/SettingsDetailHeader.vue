<script setup lang="ts">
import { useRouter } from "vue-router";
import { useI18n } from "vue-i18n";
import { ArrowLeft } from "lucide-vue-next";

const props = defineProps<{
  title: string;
  eyebrow?: string;
  parentRouteName?: string;
}>();

const router = useRouter();
const { t } = useI18n();

// The arrow means "up one level in the settings tree", not "browser back":
// arriving from /chat (sidebar "+ agent") or hopping between sub-pages must
// still land on the parent menu. When the previous history entry already is
// that parent we pop instead of pushing, so hub → page → back doesn't pile
// duplicate hub entries onto the stack.
function back() {
  const parent = router.resolve({ name: props.parentRouteName ?? "settings" });
  const previous = (window.history.state as { back?: unknown } | null)?.back;
  const previousPath = typeof previous === "string" ? previous.split(/[?#]/)[0] : "";
  if (previousPath === parent.path) {
    router.back();
    return;
  }
  void router.push(parent);
}
</script>

<template>
  <header
    class="settings-detail-header sticky top-0 z-10 -mx-4 sm:-mx-6 px-4 sm:px-6 py-3 mb-5 flex items-center gap-3 bg-background/85 backdrop-blur border-b border-border"
  >
    <button
      class="settings-detail-back size-8 flex items-center justify-center rounded text-muted-foreground hover:bg-accent hover:text-foreground cursor-pointer shrink-0"
      :title="t('settings.backToSettings')"
      @click="back"
    >
      <ArrowLeft class="size-4" />
    </button>
    <div class="min-w-0 flex-1">
      <div
        v-if="eyebrow"
        class="text-[10px] font-semibold uppercase tracking-[0.08em] text-muted-foreground mb-0.5 truncate"
      >
        {{ eyebrow }}
      </div>
      <h1 class="text-[16px] font-semibold tracking-tight text-foreground truncate">
        {{ title }}
      </h1>
    </div>
    <div class="shrink-0 flex items-center gap-2">
      <slot name="actions" />
    </div>
  </header>
</template>
