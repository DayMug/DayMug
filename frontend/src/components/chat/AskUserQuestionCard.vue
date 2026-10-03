<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { CircleHelp, Check } from "lucide-vue-next";
import type { UserQuestionRequest } from "@/composables/useWebSocket";

const props = defineProps<{
  request: UserQuestionRequest;
  submitting: boolean;
  isConnected: boolean;
}>();

const emit = defineEmits<{
  answer: [requestId: string, answers: Record<string, string[]>];
}>();

const { t } = useI18n();
const selected = reactive<Record<string, string[]>>({});
const freeform = reactive<Record<string, string>>({});
const currentIndex = ref(0);

watch(
  () => props.request.request_id,
  () => {
    currentIndex.value = 0;
    for (const key of Object.keys(selected)) delete selected[key];
    for (const key of Object.keys(freeform)) delete freeform[key];
  },
  { immediate: true },
);

function toggle(questionId: string, label: string, multi: boolean) {
  freeform[questionId] = "";
  if (!multi) {
    selected[questionId] = selected[questionId]?.includes(label) ? [] : [label];
    return;
  }
  const values = selected[questionId] ?? [];
  selected[questionId] = values.includes(label)
    ? values.filter((value) => value !== label)
    : [...values, label];
}

function updateFreeform(questionId: string, event: Event) {
  const value = (event.target as HTMLInputElement).value;
  freeform[questionId] = value;
  if (value) selected[questionId] = [];
}

function answersFor(questionId: string): string[] {
  const custom = freeform[questionId]?.trim();
  return custom ? [custom] : (selected[questionId] ?? []);
}

function isAnswered(questionId: string): boolean {
  return answersFor(questionId).length > 0;
}

const currentQuestion = computed(() => props.request.questions[currentIndex.value]);
const isLastQuestion = computed(() => currentIndex.value === props.request.questions.length - 1);
const unansweredCount = computed(
  () => props.request.questions.filter((question) => !isAnswered(question.id)).length,
);
// Navigation is deliberately free in both directions: a long questionnaire on a
// phone is unreadable if the only way forward is answering the step you are
// looking at. Only the final submit still insists on a complete set.
const canSubmit = computed(
  () => props.isConnected && !props.submitting && unansweredCount.value === 0,
);

function goTo(index: number) {
  if (props.submitting) return;
  if (index < 0 || index >= props.request.questions.length) return;
  currentIndex.value = index;
}

function previous() {
  goTo(currentIndex.value - 1);
}

function next() {
  goTo(currentIndex.value + 1);
}

function continueOrSubmit() {
  if (props.submitting) return;
  if (!isLastQuestion.value) {
    next();
    return;
  }
  if (!canSubmit.value) return;

  const answers: Record<string, string[]> = {};
  for (const question of props.request.questions) answers[question.id] = answersFor(question.id);
  emit("answer", props.request.request_id, answers);
}

// Horizontal swipe moves between questions. The card sits inside the vertically
// scrolling transcript, so a gesture only counts as a swipe once it is both far
// enough and more horizontal than vertical — otherwise every attempt to scroll
// past the card would skip a question.
const swipeThreshold = 48;
let swipeStartX = 0;
let swipeStartY = 0;
let swipeTracking = false;

function onSwipeStart(event: TouchEvent) {
  swipeTracking = event.touches.length === 1;
  if (!swipeTracking) return;
  swipeStartX = event.touches[0].clientX;
  swipeStartY = event.touches[0].clientY;
}

function onSwipeEnd(event: TouchEvent) {
  if (!swipeTracking) return;
  swipeTracking = false;
  const touch = event.changedTouches[0];
  if (!touch) return;
  const deltaX = touch.clientX - swipeStartX;
  const deltaY = touch.clientY - swipeStartY;
  if (Math.abs(deltaX) < swipeThreshold || Math.abs(deltaX) <= Math.abs(deltaY)) return;
  if (deltaX < 0) next();
  else previous();
}
</script>

<template>
  <section
    class="mx-3 mb-2 flex min-h-0 flex-col overflow-hidden rounded-xl border border-primary/25 bg-card shadow-sm"
    data-testid="ask-user-question"
  >
    <div
      class="flex shrink-0 items-center gap-2 border-b border-border/70 bg-primary/[0.04] px-4 py-2.5"
    >
      <span class="flex size-7 items-center justify-center rounded-full bg-primary/10 text-primary">
        <CircleHelp :size="16" />
      </span>
      <div class="min-w-0">
        <p class="text-xs font-semibold uppercase tracking-[0.12em] text-primary">
          {{ t("chat.question.title") }}
        </p>
        <p class="text-xs text-muted-foreground">{{ t("chat.question.subtitle") }}</p>
      </div>
    </div>

    <form
      class="min-h-0 space-y-4 overflow-y-auto overscroll-y-contain px-4 py-3 touch-pan-y"
      data-testid="question-form"
      @submit.prevent="continueOrSubmit"
    >
      <div v-if="request.questions.length > 1" class="flex items-center gap-3">
        <span class="shrink-0 text-[11px] font-medium text-muted-foreground">
          {{
            t("chat.question.progress", {
              current: currentIndex + 1,
              total: request.questions.length,
            })
          }}
        </span>
        <div class="flex flex-1 gap-1">
          <button
            v-for="(question, index) in request.questions"
            :key="question.id"
            type="button"
            class="flex h-6 flex-1 items-center"
            :aria-label="t('chat.question.jump', { index: index + 1 })"
            :aria-current="index === currentIndex ? 'step' : undefined"
            data-testid="question-step"
            @click="goTo(index)"
          >
            <span
              class="h-1 w-full rounded-full transition-colors"
              :class="
                index === currentIndex
                  ? 'bg-primary'
                  : isAnswered(question.id)
                    ? 'bg-primary/40'
                    : 'bg-muted'
              "
            />
          </button>
        </div>
      </div>

      <fieldset
        v-if="currentQuestion"
        :key="currentQuestion.id"
        class="min-w-0"
        data-testid="question-step-body"
        @touchstart.passive="onSwipeStart"
        @touchend.passive="onSwipeEnd"
      >
        <legend class="mb-2 w-full">
          <span class="text-[11px] font-medium text-muted-foreground">
            {{ currentQuestion.header }}
          </span>
          <span class="mt-0.5 block text-sm font-medium leading-5 text-foreground">
            {{ currentQuestion.question }}
          </span>
          <span
            v-if="currentQuestion.multi_select"
            class="mt-0.5 block text-[11px] text-muted-foreground"
          >
            {{ t("chat.question.multiple") }}
          </span>
        </legend>

        <div v-if="currentQuestion.options?.length" class="grid gap-1.5 sm:grid-cols-2">
          <button
            v-for="option in currentQuestion.options"
            :key="option.label"
            type="button"
            data-testid="question-option"
            class="group flex min-h-11 items-start gap-2 rounded-lg border px-3 py-2 text-left transition-colors"
            :class="
              selected[currentQuestion.id]?.includes(option.label)
                ? 'border-primary bg-primary/[0.07] text-foreground'
                : 'border-border bg-background text-foreground hover:border-primary/45 hover:bg-muted/40'
            "
            :aria-pressed="selected[currentQuestion.id]?.includes(option.label)"
            @click="toggle(currentQuestion.id, option.label, !!currentQuestion.multi_select)"
          >
            <span
              class="mt-0.5 flex size-4 shrink-0 items-center justify-center rounded-full border"
              :class="
                selected[currentQuestion.id]?.includes(option.label)
                  ? 'border-primary bg-primary text-primary-foreground'
                  : 'border-muted-foreground/40'
              "
            >
              <Check v-if="selected[currentQuestion.id]?.includes(option.label)" :size="11" />
            </span>
            <span class="min-w-0">
              <span class="block text-xs font-medium">{{ option.label }}</span>
              <span
                v-if="option.description"
                class="mt-0.5 block text-[11px] leading-4 text-muted-foreground"
              >
                {{ option.description }}
              </span>
            </span>
          </button>
        </div>

        <label
          v-if="currentQuestion.allow_other || !currentQuestion.options?.length"
          class="mt-2 block"
        >
          <span class="sr-only">{{ t("chat.question.other") }}</span>
          <input
            :type="currentQuestion.secret ? 'password' : 'text'"
            :value="freeform[currentQuestion.id] ?? ''"
            class="h-10 w-full rounded-lg border border-border bg-background px-3 text-sm text-foreground outline-none transition-colors placeholder:text-muted-foreground focus:border-primary focus:ring-2 focus:ring-primary/15"
            :placeholder="t('chat.question.other')"
            autocomplete="off"
            @input="updateFreeform(currentQuestion.id, $event)"
          />
        </label>
      </fieldset>

      <div
        class="sticky bottom-0 z-10 -mx-4 flex items-center justify-between border-t border-border/70 bg-card px-4 pb-1 pt-3"
        data-testid="question-actions"
      >
        <button
          v-if="currentIndex > 0"
          type="button"
          class="h-9 rounded-lg px-3 text-xs font-medium text-muted-foreground transition-colors hover:bg-muted hover:text-foreground disabled:cursor-not-allowed disabled:opacity-45"
          :disabled="submitting"
          data-testid="previous-question"
          @click="previous"
        >
          {{ t("chat.question.previous") }}
        </button>
        <span v-else />
        <div class="flex items-center gap-3">
          <span
            v-if="isLastQuestion && unansweredCount > 0"
            class="text-[11px] text-muted-foreground"
            data-testid="question-unanswered"
          >
            {{ t("chat.question.unanswered", { count: unansweredCount }) }}
          </span>
          <button
            type="submit"
            class="h-9 rounded-lg bg-primary px-4 text-xs font-semibold text-primary-foreground transition-opacity disabled:cursor-not-allowed disabled:opacity-45"
            :disabled="submitting || (isLastQuestion && !canSubmit)"
          >
            {{
              submitting
                ? t("chat.question.submitting")
                : isLastQuestion
                  ? t("chat.question.submit")
                  : t("chat.question.next")
            }}
          </button>
        </div>
      </div>
    </form>
  </section>
</template>
