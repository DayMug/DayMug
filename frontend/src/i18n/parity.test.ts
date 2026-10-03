import { describe, expect, it } from "vitest";

import { checkLocales, flatten, placeholders, type LocaleTree } from "./parity";
import en from "./locales/en";
import zh from "./locales/zh";

describe("i18n locale parity", () => {
  // The real guard: every shipped locale must stay in lock-step. If this fails,
  // a key was added/removed/renamed in one locale but not the other.
  it("keeps en and zh in full key + placeholder parity", () => {
    const problems = checkLocales({ en, zh } as Record<string, LocaleTree>).filter(
      (p) => p.severity === "error",
    );
    expect(
      problems,
      problems.map((p) => `${p.kind} ${p.locale}:${p.key} — ${p.detail}`).join("\n"),
    ).toEqual([]);
  });
});

describe("checkLocales", () => {
  it("flags a key missing from one locale", () => {
    const problems = checkLocales({
      en: { greeting: "Hi", farewell: "Bye" },
      zh: { greeting: "你好" },
    });
    expect(problems).toContainEqual(
      expect.objectContaining({ kind: "missing-key", locale: "zh", key: "farewell" }),
    );
  });

  it("flags placeholder mismatches", () => {
    const problems = checkLocales({
      en: { msg: "Hello {name}" },
      zh: { msg: "你好" },
    });
    expect(problems).toContainEqual(
      expect.objectContaining({ kind: "placeholder-mismatch", locale: "zh", key: "msg" }),
    );
  });

  it("flags empty values", () => {
    const problems = checkLocales({ en: { a: "A" }, zh: { a: "  " } });
    expect(problems).toContainEqual(
      expect.objectContaining({ kind: "empty-value", locale: "zh", key: "a" }),
    );
  });

  it("flags a shape mismatch (object vs string)", () => {
    const problems = checkLocales({
      en: { nav: { home: "Home" } },
      zh: { nav: "导航" },
    });
    expect(problems.some((p) => p.kind === "shape-mismatch" || p.kind === "missing-key")).toBe(
      true,
    );
  });

  // A bare "@" or "|" makes vue-i18n throw while compiling the message, which
  // unmounts the app and leaves a blank page — worth catching in the locale
  // file rather than in a bug report.
  it.each([
    ["@", { en: { help: "mention the bot" }, zh: { help: "先 @ 一次机器人" } }],
    ["|", { en: { help: "a or b" }, zh: { help: "甲 | 乙" } }],
  ])('flags a bare "%s" as message syntax', (char, locales) => {
    expect(checkLocales(locales)).toContainEqual(
      expect.objectContaining({ kind: "unescaped-syntax", locale: "zh", key: "help" }),
    );
    expect(checkLocales(locales).every((p) => p.detail.includes(char))).toBe(true);
  });

  it("accepts the escaped form and does not read it as a placeholder", () => {
    expect(
      checkLocales({
        en: { help: "from {'@'}BotFather" },
        zh: { help: "来自 {'@'}BotFather" },
      }),
    ).toEqual([]);
    // en has no "@" at all here, so the escape must not count as a token.
    expect(
      checkLocales({ en: { help: "mention the bot" }, zh: { help: "先 {'@'} 一次机器人" } }),
    ).toEqual([]);
  });

  it("warns on likely-untranslated identical values only when asked", () => {
    const locales = { en: { title: "Settings" }, zh: { title: "Settings" } };
    expect(checkLocales(locales)).toEqual([]);
    expect(checkLocales(locales, { checkUntranslated: true })).toContainEqual(
      expect.objectContaining({ kind: "untranslated", locale: "zh", key: "title" }),
    );
  });

  it("does not warn on brand names / acronyms left identical", () => {
    const problems = checkLocales(
      { en: { brand: "DayMug", proto: "HTTP" }, zh: { brand: "DayMug", proto: "HTTP" } },
      { checkUntranslated: true },
    );
    expect(problems).toEqual([]);
  });
});

describe("flatten / placeholders helpers", () => {
  it("flattens nested keys to dot paths", () => {
    const keys = [...flatten({ a: { b: "x" }, c: "y" }).keys()];
    expect(keys).toContain("a.b");
    expect(keys).toContain("c");
  });

  it("extracts and sorts interpolation tokens", () => {
    expect(placeholders("{b} and {a}")).toEqual(["{a}", "{b}"]);
    expect(placeholders(42 as unknown as string)).toEqual([]);
  });

  it("does not treat a literal escape as an interpolation token", () => {
    expect(placeholders("ping {'@'}{name}")).toEqual(["{name}"]);
  });
});
