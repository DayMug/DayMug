<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { buttonVariants } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { useConfirm } from "@/composables/useConfirm";

// App-root host for the imperative confirm() dialog. Rendered once in App.vue;
// the singleton state in useConfirm decides whether it's visible and what it
// says. Replaces native window.confirm(), which iOS Safari hides in web apps.
const { t } = useI18n();
const { confirmState, settle } = useConfirm();

const open = computed(() => confirmState.value !== null);

function onOpenChange(value: boolean) {
  // reka-ui fires this on Escape; treat any close-without-action as cancel.
  if (!value) settle(false);
}
</script>

<template>
  <AlertDialog :open="open" @update:open="onOpenChange">
    <AlertDialogContent v-if="confirmState">
      <AlertDialogHeader>
        <AlertDialogTitle>
          {{ confirmState.title || t("common.confirm") }}
        </AlertDialogTitle>
        <AlertDialogDescription class="whitespace-pre-line">
          {{ confirmState.message }}
        </AlertDialogDescription>
      </AlertDialogHeader>
      <AlertDialogFooter>
        <AlertDialogCancel @click="settle(false)">
          {{ confirmState.cancelText || t("common.cancel") }}
        </AlertDialogCancel>
        <!-- Plain button, NOT AlertDialogAction (a reka DialogClose). The
             DialogClose's own click handler fires onOpenChange(false) before
             our @click, so a settle(false) would win the race and the
             confirmation would resolve as cancelled. Settling directly here
             closes the dialog through the controlled `open` prop instead. -->
        <button
          type="button"
          :class="
            cn(
              buttonVariants(),
              confirmState.variant === 'destructive' && buttonVariants({ variant: 'destructive' }),
            )
          "
          @click="settle(true)"
        >
          {{ confirmState.confirmText || t("common.confirm") }}
        </button>
      </AlertDialogFooter>
    </AlertDialogContent>
  </AlertDialog>
</template>
