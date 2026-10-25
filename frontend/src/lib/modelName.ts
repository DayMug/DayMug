// displayModelName turns a provider model id into the short name a person
// would say: "claude-opus-5-5[1m]" → "Opus 5.5 · 1M",
// "claude-sonnet-4-5-20250929" → "Sonnet 4.5". It is only for the composer's
// compact trigger; the raw id stays in the menu and the trigger's title, which
// is where it is needed for telling two snapshots apart. Ids it does not
// recognise come back unchanged.
export function displayModelName(id: string): string {
  const suffix = /\[(\w+)\]$/.exec(id)?.[1];
  const base = suffix ? id.slice(0, -suffix.length - 2) : id;
  const claude = /^claude-(opus|sonnet|haiku|fable)-(\d+)(?:-(\d{1,2}))?(?:-\d{8})?$/.exec(base);
  if (!claude) return id;
  const [, family, major, minor] = claude;
  const name = `${family[0].toUpperCase()}${family.slice(1)} ${minor ? `${major}.${minor}` : major}`;
  return suffix ? `${name} · ${suffix.toUpperCase()}` : name;
}
