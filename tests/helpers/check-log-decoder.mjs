import assert from 'node:assert/strict';
import {logDecoder} from '../../internal/quickinput/assets/log-model.mjs';
let input = '';
for await (const chunk of process.stdin) input += chunk;
const frames = JSON.parse(input);
for (const encoding of [process.argv[2]]) {
  const continuous = logDecoder(encoding);
  for (const [index, frame] of frames.entries()) {
    const page = logDecoder(encoding);
    page.seed(frame.context?.[encoding] ?? []);
    const expected = continuous.decode(frame.chunks[0]);
    const actual = page.decode(frame.chunks[0]);
    assert.equal(actual.text, expected.text, encoding + ' frame ' + index);
  }
}
