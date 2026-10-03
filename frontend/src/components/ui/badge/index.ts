import type { VariantProps } from "class-variance-authority";
import { cva } from "class-variance-authority";

export { default as Badge } from "./Badge.vue";

// New tonal variants — aligned with the UI design's `Tag` atom (ok / warn /
// err / accent / mono). The classic shadcn variants stay in place so existing
// callers don't break.
//
// "soft-*" tones use a tinted background + saturated text for chip-style
// labels (e.g. allow/deny lists, file modified/added markers, "preview"
// pills). The fully-saturated tonal variants are for solid status pills.
export const badgeVariants = cva(
  "inline-flex items-center justify-center rounded-full border px-2 py-0.5 text-xs font-medium w-fit whitespace-nowrap shrink-0 [&>svg]:size-3 gap-1 [&>svg]:pointer-events-none focus-visible:border-ring focus-visible:ring-ring/50 focus-visible:ring-[3px] aria-invalid:ring-destructive/20 dark:aria-invalid:ring-destructive/40 aria-invalid:border-destructive transition-[color,box-shadow] overflow-hidden",
  {
    variants: {
      variant: {
        default: "border-transparent bg-primary text-primary-foreground [a&]:hover:bg-primary/90",
        secondary:
          "border-transparent bg-secondary text-secondary-foreground [a&]:hover:bg-secondary/90",
        destructive:
          "border-transparent bg-destructive text-white [a&]:hover:bg-destructive/90 focus-visible:ring-destructive/20 dark:focus-visible:ring-destructive/40 dark:bg-destructive/60",
        outline: "text-foreground [a&]:hover:bg-accent [a&]:hover:text-accent-foreground",
        // Emphasis stays monochrome; semantic status variants below retain
        // color because they communicate success, warning, or failure.
        accent: "border-transparent bg-[var(--accent-soft)] text-foreground",
        success:
          "border-transparent bg-[oklch(0.95_0.06_150)] text-[oklch(0.40_0.16_150)] dark:bg-[oklch(0.30_0.08_150)] dark:text-[oklch(0.85_0.14_150)]",
        warning:
          "border-transparent bg-[oklch(0.95_0.07_70)] text-[oklch(0.42_0.16_70)] dark:bg-[oklch(0.32_0.08_70)] dark:text-[oklch(0.86_0.14_70)]",
        info: "border-transparent bg-[oklch(0.95_0.05_240)] text-[oklch(0.42_0.16_240)] dark:bg-[oklch(0.30_0.08_240)] dark:text-[oklch(0.85_0.14_240)]",
        // Mono code-style chip (path tags, IDs)
        mono: "border-[var(--edge-soft)] bg-[var(--paper-alt)] text-[var(--ink-3)] font-mono px-1.5",
      },
    },
    defaultVariants: {
      variant: "default",
    },
  },
);
export type BadgeVariants = VariantProps<typeof badgeVariants>;
