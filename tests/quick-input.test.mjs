import test from "node:test";
import assert from "node:assert/strict";
import {validDraft, insertedText, normalizeNewlines} from "../internal/quickinput/assets/input-model.mjs";

test("draft boundaries count UTF-8 bytes and preserve whitespace", () => {
  for (const text of ["a".repeat(16000), "中".repeat(5333), "😀".repeat(4000), " \t\n"]) assert.equal(validDraft(text), true);
  for (const text of ["a".repeat(16001), "中".repeat(5334), "😀".repeat(4001), "\uD800", "a\rb"]) assert.equal(validDraft(text), false);
  assert.equal(normalizeNewlines(" \r\n\t\r"), " \n\t\n");
});
test("paste evaluates replacement selection without truncation", () => {
  assert.equal(insertedText("ab中文cd", 2, 4, "😀\r\nx"), "ab😀\nxcd");
  assert.equal(validDraft(insertedText("a".repeat(16000), 0, 2, "中")), false);
  assert.equal(validDraft(insertedText("a".repeat(16000), 0, 3, "中")), true);
});
