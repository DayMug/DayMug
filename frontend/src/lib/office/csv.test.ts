import { describe, expect, it } from "vitest";

import { decodeTableBytes, parseDelimited, sniffDelimiter } from "./csv";

const utf8 = (s: string) => new TextEncoder().encode(s);

function withBom(s: string): Uint8Array {
  const body = utf8(s);
  const out = new Uint8Array(body.length + 3);
  out.set([0xef, 0xbb, 0xbf]);
  out.set(body, 3);
  return out;
}

/**
 * GBK bytes for `姓名,年龄\n张三,30`. Node's TextDecoder reads gbk but its
 * TextEncoder only writes UTF-8, so the sample is spelled out byte by byte.
 */
const GBK_SAMPLE = new Uint8Array([
  0xd0, 0xd5, 0xc3, 0xfb, 0x2c, 0xc4, 0xea, 0xc1, 0xe4, 0x0a, 0xd5, 0xc5, 0xc8, 0xfd, 0x2c, 0x33,
  0x30,
]);

describe("decodeTableBytes", () => {
  it("swallows a UTF-8 BOM instead of making it part of the first header", () => {
    const { text, encoding } = decodeTableBytes(withBom("姓名,年龄\n张三,30"));
    expect(encoding).toBe("utf-8");
    expect(text.startsWith("姓名")).toBe(true);
  });

  it("reads BOM-less UTF-8", () => {
    expect(decodeTableBytes(utf8("月份,金额")).text).toBe("月份,金额");
  });

  it("falls back to GBK when strict UTF-8 refuses the bytes", () => {
    const { text, encoding } = decodeTableBytes(GBK_SAMPLE);
    expect(encoding).toBe("gbk");
    expect(text).toBe("姓名,年龄\n张三,30");
  });

  it("recognises a UTF-16LE BOM", () => {
    const body = "a,b";
    const bytes = new Uint8Array(2 + body.length * 2);
    bytes.set([0xff, 0xfe]);
    for (let i = 0; i < body.length; i++) bytes[2 + i * 2] = body.charCodeAt(i);
    expect(decodeTableBytes(bytes).encoding).toBe("utf-16le");
    expect(decodeTableBytes(bytes).text).toBe("a,b");
  });
});

describe("sniffDelimiter", () => {
  it("finds a comma", () => expect(sniffDelimiter("a,b,c\n1,2,3")).toBe(","));
  it("finds a semicolon", () => expect(sniffDelimiter("a;b;c\n1;2;3")).toBe(";"));
  it("finds a tab", () => expect(sniffDelimiter("a\tb\tc\n1\t2\t3")).toBe("\t"));

  it("trusts a .tsv extension over the content", () => {
    expect(sniffDelimiter("a,b\tc,d", "report.tsv")).toBe("\t");
  });

  it("ignores commas in prose because their count per line varies", () => {
    expect(sniffDelimiter("说明;值\n一,二,三;1\n四;2")).toBe(";");
  });

  it("falls back to a comma for a single-column file", () => {
    expect(sniffDelimiter("Title\nrow one\nrow two")).toBe(",");
  });
});

describe("parseDelimited (RFC 4180)", () => {
  it("splits rows and columns", () => {
    expect(parseDelimited("a,b\n1,2", ",")).toEqual([
      ["a", "b"],
      ["1", "2"],
    ]);
  });

  it("does not split on a delimiter inside quotes", () => {
    expect(parseDelimited('a,"b,c",d', ",")).toEqual([["a", "b,c", "d"]]);
  });

  it("does not end a row on a newline inside quotes", () => {
    expect(parseDelimited('a,"line one\nline two",c', ",")).toEqual([
      ["a", "line one\nline two", "c"],
    ]);
  });

  it("reads a doubled quote as one literal quote", () => {
    expect(parseDelimited('a,"he said ""yes""",c', ",")).toEqual([["a", 'he said "yes"', "c"]]);
  });

  it("accepts CRLF, LF and a bare CR as row separators", () => {
    expect(parseDelimited("a\r\nb\rc\nd", ",")).toEqual([["a"], ["b"], ["c"], ["d"]]);
  });

  it("keeps the empty column a trailing delimiter implies", () => {
    expect(parseDelimited("a,b,\n1,2,", ",")).toEqual([
      ["a", "b", ""],
      ["1", "2", ""],
    ]);
  });

  it("does not turn the trailing newline into an empty row", () => {
    expect(parseDelimited("a,b\n1,2\n", ",")).toEqual([
      ["a", "b"],
      ["1", "2"],
    ]);
  });

  it("skips blank lines in the middle", () => {
    expect(parseDelimited("a\n\nb", ",")).toEqual([["a"], ["b"]]);
  });

  it("yields content rather than throwing on an unterminated quote", () => {
    expect(parseDelimited('a,"unclosed\nnext line', ",")).toEqual([["a", "unclosed\nnext line"]]);
  });

  it("treats junk after a closing quote as the same field, keeping row counts sane", () => {
    expect(parseDelimited('"a"b,c\n1,2', ",")).toEqual([
      ["ab", "c"],
      ["1", "2"],
    ]);
  });

  it("parses empty input as an empty grid", () => expect(parseDelimited("", ",")).toEqual([]));
});
