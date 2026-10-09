// pathRelative computes a POSIX-style relative path from `from` to `to`,
// inserting `..` segments for parts of `from` that aren't shared with `to`.
// Both inputs must be absolute, slash-separated paths. The result is "."
// when the two paths refer to the same directory.
export function pathRelative(from: string, to: string): string {
  const f = stripTrailing(from).split("/").filter(Boolean);
  const t = stripTrailing(to).split("/").filter(Boolean);
  let i = 0;
  while (i < f.length && i < t.length && f[i] === t[i]) i++;
  const parts = [...Array(f.length - i).fill(".."), ...t.slice(i)];
  return parts.length === 0 ? "." : parts.join("/");
}

function stripTrailing(p: string): string {
  return p.replace(/\/+$/, "");
}
