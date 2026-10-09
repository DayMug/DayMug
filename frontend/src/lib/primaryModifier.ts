export type ModifierEvent = Pick<KeyboardEvent | MouseEvent | PointerEvent, "ctrlKey" | "metaKey">;

export function isMacLike(nav: Navigator = navigator): boolean {
  const platform = nav.platform || "";
  const userAgent = nav.userAgent || "";
  return /Mac|iPhone|iPad|iPod/.test(platform) || /Mac|iPhone|iPad|iPod/.test(userAgent);
}

export function isPrimaryModifier(event: ModifierEvent, nav: Navigator = navigator): boolean {
  return isMacLike(nav) ? event.metaKey : event.ctrlKey;
}

export function primaryModifierLabel(nav: Navigator = navigator): string {
  return isMacLike(nav) ? "⌘" : "Ctrl";
}
