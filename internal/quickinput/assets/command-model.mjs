export function commandQuery(text) {
  return typeof text === 'string' && text.startsWith('/') && !/[\r\n]/u.test(text) ? text.slice(1).trim() : null;
}

export function matchScore(name, query) {
  const value = name.toLocaleLowerCase('en-US'), term = query.toLocaleLowerCase('en-US');
  if (!term || value === term) return 0;
  if (value.startsWith(term)) return 1;
  if (value.includes(term)) return 2;
  let offset = 0;
  for (const character of term) {
    const index = value.indexOf(character, offset);
    if (index < 0) return Infinity;
    offset = index + character.length;
  }
  return 3;
}

export function filterCommands(entries, query) {
  return entries.map(entry => {
    const names = [entry.name, ...(entry.aliases ?? [])];
    const scores = names.map(name => matchScore(name, query));
    const score = Math.min(...scores);
    return {...entry, score, matchedName: names[scores.indexOf(score)]};
  }).filter(entry => Number.isFinite(entry.score)).sort((a, b) =>
    a.score - b.score || a.name.toLowerCase().localeCompare(b.name.toLowerCase(), 'en-US') || a.id.localeCompare(b.id));
}

export function quotedCommand(text) {
  const fences = text.match(/`+/g) ?? [];
  const fence = '`'.repeat(Math.max(3, ...fences.map(value => value.length + 1)));
  return `${fence}text\n${text}\n${fence}`;
}
