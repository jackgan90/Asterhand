// Transient per-command feedback. Filtering, focus and catalog refresh do not
// reset it; only the latest intentional launch may update a command's result.
export function commandResults() {
  const versions = new Map(), errors = new Map();
  let sequence = 0;
  return {
    begin(id) { const version = ++sequence; versions.set(id, version); return version; },
    finish(id, version, error = '') {
      if (versions.get(id) !== version) return false;
      if (error) errors.set(id, error); else errors.delete(id);
      return true;
    },
    error(id) { return errors.get(id) ?? ''; },
    retain(ids) {
      const present = new Set(ids);
      for (const id of versions.keys()) if (!present.has(id)) { versions.delete(id); errors.delete(id); }
    },
  };
}
