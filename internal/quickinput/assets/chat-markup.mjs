// Tokenize code and math before Markdown can interpret their punctuation.
// Tokens keep source text so incomplete/unsupported formulas remain copyable.
function escaped(text, index) {
  let slashes = 0;
  while (index > 0 && text[--index] === '\\') slashes++;
  return slashes % 2 === 1;
}

function formulaEnd(text, start, delimiter) {
  let at = start;
  while ((at = text.indexOf(delimiter, at)) !== -1) {
    if (!escaped(text, at) && (delimiter !== '$' ||
      text[at - 1] !== '$' && text[at + 1] !== '$' &&
      !/\s/.test(text[at - 1]) && !/[0-9A-Za-z]/.test(text[at + 1] ?? ''))) return at;
    at += delimiter.length;
  }
  return -1;
}

function proseTokens(text) {
  const tokens = [];
  let plain = 0, at = 0;
  const push = (token, end) => {
    if (at > plain) tokens.push({kind: 'text', text: text.slice(plain, at)});
    tokens.push(token);
    plain = at = end;
  };
  while (at < text.length) {
    if (text[at] === '`' && !escaped(text, at)) {
      const ticks = /^`+/.exec(text.slice(at))[0];
      const end = text.indexOf(ticks, at + ticks.length);
      if (end !== -1 && !text.slice(at + ticks.length, end).includes('\n')) {
        push({kind: 'code', text: text.slice(at + ticks.length, end)}, end + ticks.length);
        continue;
      }
    }
    let open = '', close = '', display = false;
    if (!escaped(text, at)) {
      const pair = text.slice(at, at + 2);
      if (pair === '\\(' || pair === '\\[') {
        open = pair; close = pair === '\\(' ? '\\)' : '\\]'; display = pair === '\\[';
      } else if (pair === '$$') {
        open = close = '$$'; display = true;
      } else if (text[at] === '$' && text[at + 1] && !/\s/.test(text[at + 1]) &&
        !/[0-9A-Za-z$]/.test(text[at - 1] ?? '')) open = close = '$';
    }
    if (open) {
      const end = formulaEnd(text, at + open.length, close);
      // Inline dollar formulas cannot span lines or use whitespace at either edge.
      const body = end < 0 ? '' : text.slice(at + open.length, end);
      if (end >= 0 && (open !== '$' || !body.includes('\n'))) {
        push({kind: 'math', text: body, raw: text.slice(at, end + close.length), display}, end + close.length);
        continue;
      }
      if (open !== '$') {
        push({kind: 'source', text: text.slice(at), display}, text.length);
        continue;
      }
      // A streaming inline formula is literal until its closing dollar arrives.
      // Stop at the line boundary so later Markdown blocks remain independent.
      const newline = text.indexOf('\n', at);
      const stop = newline < 0 ? text.length : newline;
      push({kind: 'source', text: text.slice(at, stop), display: false}, stop);
      continue;
    }
    at++;
  }
  if (plain < text.length) tokens.push({kind: 'text', text: text.slice(plain)});
  return tokens;
}

export function markupTokens(text) {
  const tokens = [];
  const fences = /^ {0,3}```([\w+-]*)[ \t]*$/gm;
  let start = 0, match;
  while ((match = fences.exec(text))) {
    tokens.push(...proseTokens(text.slice(start, match.index)));
    const bodyStart = fences.lastIndex + (text[fences.lastIndex] === '\n' ? 1 : 0);
    const closing = /^ {0,3}```[ \t]*$/gm;
    closing.lastIndex = bodyStart;
    const end = closing.exec(text);
    const code = text.slice(bodyStart, end ? end.index : text.length).replace(/\n$/, '');
    tokens.push({kind: 'code-block', text: code, language: match[1]});
    start = end ? closing.lastIndex : text.length;
    fences.lastIndex = start;
  }
  tokens.push(...proseTokens(text.slice(start)));
  return tokens;
}
