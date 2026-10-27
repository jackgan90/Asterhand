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

import {inputKeyAction} from '../internal/quickinput/assets/input-model.mjs';

test('Enter sends, Shift+Enter inserts a line, IME confirmation and held keys never send', () => {
  const key = extra => ({key:'Enter', ...extra});
  const action = (event, composing=false, end=-Infinity, now=1000) => inputKeyAction(event, composing, end, now);
  assert.equal(action(key()), 'send');
  assert.equal(action(key({ctrlKey:true})), 'send');
  assert.equal(action(key({shiftKey:true})), '');
  assert.equal(action(key({shiftKey:true,ctrlKey:true})), '');
  assert.equal(action(key({altKey:true})), '');
  assert.equal(action(key({metaKey:true})), '');
  assert.equal(action(key(), true), '');
  assert.equal(action(key({isComposing:true})), '');
  assert.equal(action(key({keyCode:229})), '');
  assert.equal(action(key(), false, 980), 'consume');
  assert.equal(action(key(), false, 900), 'send');
  assert.equal(action(key({repeat:true})), 'consume');
  assert.equal(action({key:'Escape'}), 'hide');
  assert.equal(action({key:'Escape'}, true), '');
});
