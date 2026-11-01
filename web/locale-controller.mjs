import {
  applyI18n,
  defaultFallback,
  getLocale,
  locales,
  localizeError,
  matchLocale,
  onLocaleChange,
  setLocale,
  t,
  validLocale,
  validPreference,
} from "./i18n.mjs";
import {$, node} from "./ui.mjs";

const localeHintKey = "asterhand-locale";
const localePrefHintKey = "asterhand-locale-pref";
const loginLocaleKey = "asterhand-login-locale";

function browserCandidates(nav = globalThis.navigator) {
  if (Array.isArray(nav?.languages) && nav.languages.length) return nav.languages;
  if (typeof nav?.language === "string" && nav.language.trim()) return [nav.language];
  return [];
}

export function resolveWebLocale({localeMode, effectiveLocale, systemDetected, browserLanguages = browserCandidates()} = {}) {
  if (localeMode && localeMode !== "auto" && validLocale(localeMode)) {
    return localeMode;
  }
  if (systemDetected === true && validLocale(effectiveLocale)) {
    return effectiveLocale;
  }
  if ((!localeMode || localeMode === "auto") && !systemDetected && browserLanguages.length) {
    return matchLocale(browserLanguages);
  }
  if (validLocale(effectiveLocale)) {
    return effectiveLocale;
  }
  return defaultFallback;
}

function safeGet(storage, key) {
  try { return storage?.getItem?.(key) || ""; } catch { return ""; }
}

function safeSet(storage, key, value) {
  try {
    if (value) storage?.setItem?.(key, value);
    else storage?.removeItem?.(key);
  } catch { /* storage unavailable */ }
}

export function localeController({desktop, listen, notice, onRelocalize, fetchHealth = globalThis.fetch}) {
  const topSelect = $("#locale-select");
  const settingsSelect = $("#settings-locale");
  let authenticated = false;
  let preference = "auto";
  let lastHealth = null;
  let healthClock = 0, latestHealthClock = 0;
  const autoOptions = [];

  function buildSelect(selectEl) {
    if (!selectEl?.replaceChildren) return;
    const autoOpt = node("option", t("common.locale_auto"));
    autoOpt.value = "auto";
    autoOptions.push(autoOpt);
    const opts = [autoOpt];
    for (const entry of locales) {
      const opt = node("option", entry.label);
      opt.value = entry.id;
      opts.push(opt);
    }
    selectEl.replaceChildren(...opts);
    selectEl.value = preference;
  }

  function syncSelectValues() {
    for (const opt of autoOptions) opt.textContent = t("common.locale_auto");
    for (const el of [topSelect, settingsSelect]) {
      if (el) el.value = preference;
    }
  }

  function persistHints(pref, effective) {
    safeSet(globalThis.localStorage, localePrefHintKey, pref);
    safeSet(globalThis.localStorage, localeHintKey, effective);
  }

  function resolveLocalPreview(pref) {
    if (pref !== "auto" && validLocale(pref)) return pref;
    if (lastHealth) {
      return resolveWebLocale({
        localeMode: "auto",
        effectiveLocale: lastHealth.locale,
        systemDetected: Boolean(lastHealth.system_locale_detected),
      });
    }
    const candidates = browserCandidates();
    return candidates.length ? matchLocale(candidates) : getLocale();
  }

  function syncDesktopStatus(status) {
    if (!status?.desired && !status?.effective_locale) return;
    const mode = validPreference(status.desired?.locale) ? status.desired.locale : "auto";
    preference = mode;
    const effective = resolveWebLocale({
      localeMode: mode,
      effectiveLocale: status.effective_locale,
      systemDetected: Boolean(status.system_locale_detected),
    });
    persistHints(mode, effective);
    syncSelectValues();
    setLocale(effective);
  }

  function restoreInitialHint() {
    const loginOverride = safeGet(globalThis.sessionStorage, loginLocaleKey);
    if (validPreference(loginOverride)) {
      preference = loginOverride;
      if (validLocale(loginOverride)) setLocale(loginOverride);
      return;
    }
    const savedPref = safeGet(globalThis.localStorage, localePrefHintKey);
    if (validPreference(savedPref)) preference = savedPref;
    const savedLocale = safeGet(globalThis.localStorage, localeHintKey);
    if (validLocale(savedLocale)) {
      setLocale(savedLocale);
      return;
    }
    const candidates = browserCandidates();
    if (candidates.length) setLocale(matchLocale(candidates));
  }

  async function calibrateUnauthenticated(isActive = () => true) {
    authenticated = false;
    if (typeof fetchHealth !== "function") return;
    const stamp = ++healthClock;
    try {
      const response = await fetchHealth("/health", {headers: {"Accept": "application/json"}});
      if (!response?.ok || !isActive() || stamp < latestHealthClock) return;
      const data = await response.json();
      if (!isActive() || authenticated || stamp < latestHealthClock) return;
      if (!data?.locale && !data?.locale_mode) return;
      latestHealthClock = stamp;
      lastHealth = data;
      const loginOverride = safeGet(globalThis.sessionStorage, loginLocaleKey);
      if (validPreference(loginOverride)) {
        preference = loginOverride;
        syncSelectValues();
        setLocale(resolveLocalPreview(loginOverride));
        return;
      }
      const mode = validPreference(data.locale_mode) ? data.locale_mode : "auto";
      preference = mode;
      const effective = resolveWebLocale({
        localeMode: mode,
        effectiveLocale: data.locale,
        systemDetected: Boolean(data.system_locale_detected),
      });
      syncSelectValues();
      setLocale(effective);
    } catch { /* health calibration is best-effort */ }
  }

  function markAuthenticated() {
    authenticated = true;
    latestHealthClock = ++healthClock;
    safeSet(globalThis.sessionStorage, loginLocaleKey, "");
  }

  async function handleSelectChange(nextPref) {
    if (!validPreference(nextPref)) return;
    if (!authenticated) {
      preference = nextPref;
      safeSet(globalThis.sessionStorage, loginLocaleKey, nextPref);
      syncSelectValues();
      setLocale(resolveLocalPreview(nextPref));
      return;
    }
    preference = nextPref;
    syncSelectValues();
    setLocale(resolveLocalPreview(nextPref));
    const res = await desktop.setLocale(nextPref);
    if (res.stale) return;
    if (res.status) syncDesktopStatus(res.status);
    if (!res.ok && res.error) notice(localizeError(res.error));
  }

  restoreInitialHint();
  buildSelect(topSelect);
  buildSelect(settingsSelect);
  applyI18n(globalThis.document);

  const unsubscribe = onLocaleChange(() => {
    applyI18n(globalThis.document);
    syncSelectValues();
    onRelocalize?.();
  });

  for (const el of [topSelect, settingsSelect]) {
    if (el?.addEventListener) {
      listen(el, "change", () => { void handleSelectChange(el.value); });
    }
  }

  if (globalThis.window?.addEventListener) {
    listen(globalThis.window, "storage", (event) => {
      if (event?.key !== localePrefHintKey && event?.key !== localeHintKey) return;
      if (authenticated) {
        if (typeof desktop?.reconcileLocale === "function") {
          void desktop.reconcileLocale().then((rec) => {
            if (authenticated && rec?.outcome !== "stale" && rec?.status) {
              syncDesktopStatus(rec.status);
            }
          }, () => {});
          return;
        }
      } else if (validPreference(safeGet(globalThis.sessionStorage, loginLocaleKey))) {
        return;
      }
      if (event?.key === localePrefHintKey && validPreference(event.newValue)) {
        preference = event.newValue;
        syncSelectValues();
      }
      if (event?.key === localeHintKey && validLocale(event.newValue)) {
        setLocale(event.newValue);
      }
    });
  }

  return {
    markAuthenticated,
    syncDesktopStatus,
    calibrateUnauthenticated,
    dispose: unsubscribe,
  };
}
