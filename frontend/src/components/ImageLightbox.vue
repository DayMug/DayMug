<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import { ArrowDownToLine, X } from "lucide-vue-next";

// A full-viewport image viewer. Mounted with v-if rather than an `open` prop so
// every open starts from a clean zoom/pan state — there is no "restore where I
// left off" behaviour to preserve, and unmounting is what releases the scroll
// lock and the key listener.
const props = defineProps<{ src: string; alt?: string }>();
const emit = defineEmits<{ close: [] }>();

const { t } = useI18n();

const MIN_SCALE = 1;
// Zoom headroom past whichever is larger: the fitted view or the image's own
// pixels. A huge screenshot fitted at 10% still needs room beyond 1:1.
const MAX_ZOOM = 8;
// What a double-click (or the zoom-in button from rest) jumps to. Enough to
// read UI text in a screenshot without needing a second gesture.
const STEP_SCALE = 2.5;

const scale = ref(1);
const tx = ref(0);
const ty = ref(0);
const stage = ref<HTMLElement | null>(null);
const image = ref<HTMLImageElement | null>(null);

// `scale` is relative to the fitted layout size, but the percentage the user
// sees is relative to the image's own pixels: 100% means one image pixel per
// CSS pixel. fitRatio converts between the two; it is below 1 only when the
// picture is larger than the viewport and the max-w/max-h caps shrank it.
const fitRatio = ref(1);
const oversized = computed(() => fitRatio.value < 1);
// The scale at which the image shows at its natural size.
const actualScale = computed(() => 1 / fitRatio.value);
const maxScale = computed(() => Math.max(MAX_ZOOM, actualScale.value * 4));

const transform = computed(() => `translate(${tx.value}px, ${ty.value}px) scale(${scale.value})`);
const zoomed = computed(() => scale.value > MIN_SCALE);
// The zoom-in cursor is the "+" that tells the user an oversized picture can
// be clicked open at 100%; images already at full size get no click cursor.
const cursorClass = computed(() => {
  if (oversized.value) return zoomed.value ? "cursor-zoom-out" : "cursor-zoom-in";
  return zoomed.value ? "cursor-grab" : "";
});
const zoomLabel = computed(() => `${Math.round(scale.value * fitRatio.value * 100)}%`);

// offsetWidth is the laid-out size and ignores the transform, so it measures
// the fitted size whatever the current zoom is.
function measure() {
  const el = image.value;
  if (!el || !el.naturalWidth || !el.offsetWidth) return;
  fitRatio.value = Math.min(1, el.offsetWidth / el.naturalWidth);
}

function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value));
}

// Zoom about a viewport point, so whatever sits under the cursor (or between
// two pinching fingers) stays under it. Without the anchor the picture slides
// away from what the user was aiming at and every zoom needs a corrective pan.
function zoomAt(clientX: number, clientY: number, next: number) {
  const el = stage.value;
  if (!el) return;
  const target = clamp(next, MIN_SCALE, maxScale.value);
  const rect = el.getBoundingClientRect();
  const px = clientX - rect.left - rect.width / 2;
  const py = clientY - rect.top - rect.height / 2;
  const ratio = target / scale.value;
  tx.value = px - ratio * (px - tx.value);
  ty.value = py - ratio * (py - ty.value);
  scale.value = target;
  // Back at rest the image is centred by layout, so a leftover offset would
  // strand it off to one side with no way to tell the view is not "reset".
  if (target === MIN_SCALE) reset();
}

function reset() {
  scale.value = MIN_SCALE;
  tx.value = 0;
  ty.value = 0;
}

// The buttons have no cursor to anchor on, so they zoom about the centre.
function zoomBy(factor: number) {
  const el = stage.value;
  if (!el) return;
  const rect = el.getBoundingClientRect();
  zoomAt(
    rect.left + rect.width / 2,
    rect.top + rect.height / 2,
    scale.value === MIN_SCALE && factor > 1 ? STEP_SCALE : scale.value * factor,
  );
}

function zoomTo(target: number) {
  const el = stage.value;
  if (!el) return;
  const rect = el.getBoundingClientRect();
  zoomAt(rect.left + rect.width / 2, rect.top + rect.height / 2, target);
}

function onWheel(e: WheelEvent) {
  e.preventDefault();
  // Exponential so one notch feels the same at 1x and at 6x; the sign is
  // inverted because a downward scroll means "away", i.e. zoom out.
  zoomAt(e.clientX, e.clientY, scale.value * Math.exp(-e.deltaY * 0.0015));
}

// A picture too big for the viewport is shown fitted with a zoom-in cursor;
// one click opens it at 100% around the clicked point, the next goes back to
// fitted. Pictures that already show at full size keep double-click zoom.
function onImageClick(e: MouseEvent) {
  if (dragged || !oversized.value) return;
  if (zoomed.value) reset();
  else zoomAt(e.clientX, e.clientY, actualScale.value);
}

function onDoubleClick(e: MouseEvent) {
  if (oversized.value) return;
  if (zoomed.value) reset();
  else zoomAt(e.clientX, e.clientY, STEP_SCALE);
}

// Pointer bookkeeping drives both gestures: one pointer pans, two pinch. Using
// pointer events rather than separate mouse/touch handlers keeps trackpad,
// mouse and finger on the same code path.
const pointers = new Map<number, { x: number; y: number }>();
let pinchDistance = 0;
let pinchScale = 1;
// Set once a gesture travels far enough to be a drag. The backdrop's click
// handler consults it so releasing a pan over empty space does not read as
// "clicked outside" and close the viewer.
let dragged = false;

function pointerCentre(): { x: number; y: number; distance: number } {
  const [a, b] = [...pointers.values()];
  return {
    x: (a.x + b.x) / 2,
    y: (a.y + b.y) / 2,
    distance: Math.hypot(a.x - b.x, a.y - b.y),
  };
}

function onPointerDown(e: PointerEvent) {
  pointers.set(e.pointerId, { x: e.clientX, y: e.clientY });
  dragged = false;
  if (pointers.size === 2) {
    const centre = pointerCentre();
    pinchDistance = centre.distance;
    pinchScale = scale.value;
  }
  (e.target as Element).setPointerCapture?.(e.pointerId);
}

function onPointerMove(e: PointerEvent) {
  const previous = pointers.get(e.pointerId);
  if (!previous) return;
  pointers.set(e.pointerId, { x: e.clientX, y: e.clientY });

  if (pointers.size >= 2) {
    const centre = pointerCentre();
    if (pinchDistance > 0) {
      dragged = true;
      zoomAt(centre.x, centre.y, (pinchScale * centre.distance) / pinchDistance);
    }
    return;
  }
  // Panning a picture that already fits the viewport would just drag it off
  // screen, so a single pointer only moves things once zoomed in.
  if (!zoomed.value) return;
  const dx = e.clientX - previous.x;
  const dy = e.clientY - previous.y;
  if (Math.abs(dx) > 0 || Math.abs(dy) > 0) dragged = true;
  tx.value += dx;
  ty.value += dy;
}

function onPointerUp(e: PointerEvent) {
  pointers.delete(e.pointerId);
  if (pointers.size < 2) pinchDistance = 0;
}

function onBackdropClick(e: MouseEvent) {
  if (dragged) {
    dragged = false;
    return;
  }
  // Only a click that landed on the backdrop itself closes: the image and the
  // controls sit inside it and must keep their own clicks.
  if (e.target === e.currentTarget) emit("close");
}

function onKeydown(e: KeyboardEvent) {
  if (e.key === "Escape") {
    emit("close");
  } else if (e.key === "+" || e.key === "=") {
    zoomBy(1.4);
  } else if (e.key === "-" || e.key === "_") {
    zoomBy(1 / 1.4);
  } else if (e.key === "0") {
    reset();
  } else if (e.key === "1") {
    zoomTo(actualScale.value);
  }
}

let previousOverflow = "";
onMounted(() => {
  window.addEventListener("keydown", onKeydown);
  window.addEventListener("resize", measure);
  // A cached image can already be decoded by now; measure() is a no-op until
  // the natural size is known, so the load handler covers the other case.
  measure();
  previousOverflow = document.body.style.overflow;
  document.body.style.overflow = "hidden";
});
onBeforeUnmount(() => {
  window.removeEventListener("keydown", onKeydown);
  window.removeEventListener("resize", measure);
  document.body.style.overflow = previousOverflow;
});
</script>

<template>
  <Teleport to="body">
    <div
      ref="stage"
      role="dialog"
      aria-modal="true"
      :aria-label="t('chat.imageViewer.label')"
      class="fixed inset-0 z-[60] flex items-center justify-center overflow-hidden bg-black/80 backdrop-blur-sm"
      data-testid="image-lightbox"
      @click="onBackdropClick"
      @wheel="onWheel"
      @pointerdown="onPointerDown"
      @pointermove="onPointerMove"
      @pointerup="onPointerUp"
      @pointercancel="onPointerUp"
    >
      <img
        ref="image"
        :src="props.src"
        :alt="props.alt"
        draggable="false"
        class="max-w-[92vw] max-h-[92vh] select-none object-contain shadow-2xl"
        :class="cursorClass"
        :style="{ transform, touchAction: 'none' }"
        data-testid="image-lightbox-image"
        @load="measure"
        @click="onImageClick"
        @dblclick="onDoubleClick"
      />

      <a
        :href="props.src"
        :download="props.alt || ''"
        class="absolute top-3 right-14 rounded-full p-2 text-white/80 transition hover:bg-white/10 hover:text-white"
        :aria-label="t('chat.imageViewer.download')"
        :title="t('chat.imageViewer.download')"
        data-testid="image-lightbox-download"
      >
        <ArrowDownToLine class="size-5" />
      </a>

      <button
        class="absolute top-3 right-3 rounded-full p-2 text-white/80 transition hover:bg-white/10 hover:text-white"
        :aria-label="t('chat.imageViewer.close')"
        data-testid="image-lightbox-close"
        @click="emit('close')"
      >
        <X class="size-5" />
      </button>

      <div
        class="absolute bottom-4 left-1/2 flex -translate-x-1/2 items-center gap-1 rounded-full bg-black/60 px-1.5 py-1 text-white/85 ring-1 ring-white/15"
      >
        <button
          class="rounded-full px-2.5 py-0.5 text-base leading-none transition hover:bg-white/10 disabled:opacity-40 disabled:hover:bg-transparent"
          :aria-label="t('chat.imageViewer.zoomOut')"
          :disabled="scale <= MIN_SCALE"
          data-testid="image-lightbox-zoom-out"
          @click="zoomBy(1 / 1.4)"
        >
          −
        </button>
        <button
          class="min-w-[3.25rem] rounded-full px-1 py-0.5 text-xs tabular-nums transition hover:bg-white/10"
          :title="t('chat.imageViewer.resetZoom')"
          data-testid="image-lightbox-reset"
          @click="reset"
        >
          {{ zoomLabel }}
        </button>
        <button
          class="rounded-full px-2.5 py-0.5 text-base leading-none transition hover:bg-white/10 disabled:opacity-40 disabled:hover:bg-transparent"
          :aria-label="t('chat.imageViewer.zoomIn')"
          :disabled="scale >= maxScale"
          data-testid="image-lightbox-zoom-in"
          @click="zoomBy(1.4)"
        >
          +
        </button>
      </div>
    </div>
  </Teleport>
</template>
