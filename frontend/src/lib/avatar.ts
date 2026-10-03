// Deterministic avatar coloring — name → stable hue in [0, 360).
// Two callers compute the same hue for the same name, so an agent's avatar
// stays the same color across sidebar, session list, login overview, etc.
//
// The lightness/chroma values follow the Iris agent tiles: a barely-tinted
// surface with a saturated mid-tone glyph, so the hue lives in the letter
// rather than in a heavy block of colour.

const FALLBACK_HUE = 220;

export function avatarHue(name: string | null | undefined): number {
  const normalized = (name ?? "").trim().normalize();
  if (!normalized) return FALLBACK_HUE;

  // Hash every Unicode code point, then avalanche the bits so similar names
  // (especially names sharing an initial or prefix) still spread across hues.
  let hash = 0x811c9dc5;
  for (const character of normalized) {
    hash ^= character.codePointAt(0) ?? 0;
    hash = Math.imul(hash, 0x01000193);
  }
  hash ^= hash >>> 16;
  hash = Math.imul(hash, 0x85ebca6b);
  hash ^= hash >>> 13;
  hash = Math.imul(hash, 0xc2b2ae35);
  hash ^= hash >>> 16;

  // Keep millidegree precision instead of collapsing every name into only
  // 360 integer buckets, which makes unrelated full-name hashes collide.
  return Math.floor(((hash >>> 0) / 0x100000000) * 360_000) / 1000;
}

export function avatarColors(name: string | null | undefined): {
  bg: string;
  fg: string;
} {
  const hue = avatarHue(name);
  return {
    bg: `oklch(0.94 0.035 ${hue})`,
    fg: `oklch(0.52 0.1 ${hue})`,
  };
}

export function avatarInitials(name: string | null | undefined): string {
  const trimmed = (name ?? "").trim();
  if (!trimmed) return "?";
  const parts = trimmed.split(/\s+/).filter(Boolean);
  const letters = parts
    .slice(0, 2)
    .map((p) => p[0])
    .join("");
  return letters.toUpperCase() || "?";
}
