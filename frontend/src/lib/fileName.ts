/**
 * Splits a file name into the part that may be truncated and the extension
 * that must survive truncation.
 *
 * Tail-truncating `LongRunningTerminalHandler.test.ts` down to
 * `LongRunningTermin…` throws away the one token that says what the file *is*.
 * Callers render `head` in a shrinking box and `tail` in a fixed one, so the
 * ellipsis lands in the middle and the extension always stays on screen.
 */
export interface FileNameParts {
  head: string;
  /** Includes the leading dot, or empty when there is no usable extension. */
  tail: string;
}

/**
 * Extensions that read as a single unit. Without these, `bundle.tar.gz` would
 * keep only `.gz` and truncate away the `tar` that explains the format.
 */
const COMPOUND_EXTENSIONS = ["tar.gz", "tar.bz2", "tar.xz", "tar.zst", "d.ts"];

/**
 * Past this many characters a trailing dot-segment is almost certainly part of
 * the name rather than an extension (`v1.2.3-alpha.experimental`), and pinning
 * it would leave no room for the prefix.
 */
const MAX_EXTENSION_LENGTH = 10;

export function splitFileName(name: string): FileNameParts {
  const lower = name.toLowerCase();

  for (const ext of COMPOUND_EXTENSIONS) {
    // Guard against a name that is *only* the extension, e.g. ".d.ts".
    if (lower.endsWith("." + ext) && name.length > ext.length + 1) {
      return { head: name.slice(0, -(ext.length + 1)), tail: name.slice(-(ext.length + 1)) };
    }
  }

  const dot = name.lastIndexOf(".");

  // `dot === 0` is a dotfile (`.gitignore`) — the dot starts the name, it does
  // not introduce an extension.
  if (dot <= 0) return { head: name, tail: "" };

  const ext = name.slice(dot + 1);
  if (ext.length === 0 || ext.length > MAX_EXTENSION_LENGTH) return { head: name, tail: "" };

  return { head: name.slice(0, dot), tail: name.slice(dot) };
}
