import {currentLocaleEntry, localizeMessage, t} from "./i18n.mjs";
import {displayNodeName} from "./node-controller.mjs";
import {$, node, date, dialog} from "./ui.mjs";

const stateKeys = {
  queued: "web.task.state.queued",
  running: "web.task.state.running",
  waiting_input: "web.task.state.waiting_input",
  cancel_requested: "web.task.state.cancel_requested",
  cancelled: "web.task.state.cancelled",
  succeeded: "web.task.state.succeeded",
  completed_unverified: "web.task.state.completed_unverified",
  failed: "web.task.state.failed",
  rejected: "web.task.state.rejected",
  reconciliation_required: "web.task.state.reconciliation_required",
};
const tones = {
  queued: "wait", running: "run", waiting_input: "wait", cancel_requested: "wait", cancelled: "muted",
  succeeded: "ok", completed_unverified: "ok", failed: "error", rejected: "error", reconciliation_required: "error",
};
const outcomeKeys = {
  not_started: "web.task.outcome.not_started",
  not_run: "web.task.outcome.not_run",
  running: "web.task.outcome.running",
  succeeded: "web.task.outcome.succeeded",
  failed: "web.task.outcome.failed",
  cancelled: "web.task.outcome.cancelled",
  completed_unverified: "web.task.outcome.completed_unverified",
  unknown_manually_closed: "web.task.outcome.unknown_manually_closed",
  passed: "web.task.outcome.passed",
  not_applicable: "web.task.outcome.not_applicable",
  pending: "web.task.outcome.pending",
};
const size = bytes => bytes < 1024 ? `${bytes} B` : bytes < 1048576 ? `${(bytes / 1024).toFixed(1)} KB` : `${(bytes / 1048576).toFixed(1)} MB`;
const time = value => value ? new Date(value).toLocaleString(currentLocaleEntry().htmlLang, {month:"numeric", day:"numeric", hour:"2-digit", minute:"2-digit", hour12:false}) : "";
const refreshClock = dateObj => dateObj.toLocaleTimeString(currentLocaleEntry().htmlLang, {hour:"2-digit", minute:"2-digit", hour12:false});

function stateText(state) {
  return stateKeys[state] ? t(stateKeys[state]) : state;
}

function outcomeText(outcome) {
  return outcomeKeys[outcome] ? t(outcomeKeys[outcome]) : outcome;
}

export function taskView({guarded, callbacks, submissionChanged}) {
  let editing = false, answeringCount = 0, context, lastRefreshAt = null, eventsRoot;
  let detailState = null;
  let cardRelocalizers = [];
  const answerDrafts = new Map();
  const questionInputs = new Map();

  function syncDraftValuesFromDOM() {
    for (const [draftKey, input] of questionInputs) {
      if (input.value) answerDrafts.set(draftKey, input.value);
    }
  }

  function paintLastRefresh() {
    if (!lastRefreshAt) return;
    $("#last-refresh").textContent = t("web.tasks.last_refresh", {time: refreshClock(lastRefreshAt)});
  }

  function paintCards(views, selection) {
    context = selection;
    questionInputs.clear();
    cardRelocalizers = [];
    const root = $("#tasks");
    root.replaceChildren();
    for (const view of views) root.append(taskCard(view.job, view.out, view.goal));
    if (!root.children.length) {
      const emptyEl = node("div", t("web.tasks.empty_hint"), "empty");
      cardRelocalizers.push(() => { emptyEl.textContent = t("web.tasks.empty_hint"); });
      root.append(emptyEl);
    }
    paintLastRefresh();
  }

  function render(views, selection) {
    syncDraftValuesFromDOM();
    lastRefreshAt = new Date();
    paintCards(views, selection);
  }

  function badgeLabel(job, out) {
    const rejected = out?.state === "rejected", lost = !rejected && out?.error;
    return rejected ? stateText("rejected") : lost ? t("web.task.state.connection_lost") : stateText(job.state);
  }

  function badge(job, out) {
    const rejected = out?.state === "rejected", lost = !rejected && out?.error;
    const element = node("span", badgeLabel(job, out),
      `badge ${["failed", "rejected", "reconciliation_required"].includes(job.state) ? "error" : ""}`);
    element.dataset.tone = lost ? "wait" : tones[rejected ? "rejected" : job.state] || "muted";
    return element;
  }

  function ownerName(ownerID) {
    if (!context?.info) return ownerID;
    if (ownerID === context.info.node_id) return displayNodeName(context.info, context.info.name);
    const peer = context.peers?.find((p) => p.id === ownerID);
    return peer ? displayNodeName(peer.info, peer.name) : ownerID;
  }

  function formatCardMeta(job, out) {
    const owner = ownerName(job.owner);
    const updated = out?.updated || job.updated;
    return updated ? `${owner} · ${time(updated)}` : owner;
  }

  function formatCardError(job, out) {
    return localizeMessage(job.error_key, job.error_args, job.error || "") ||
      localizeMessage(out?.error_key, out?.error_args, out?.error || "");
  }

  function taskCard(job, out, goal = job.request.goal) {
    const card = node("article", null, "task"),
      head = node("div", null, "task-head"),
      badgeEl = badge(job, out);
    head.append(node("div", goal, "task-title"), badgeEl);
    card.append(head);
    const metaEl = node("div", formatCardMeta(job, out), "metadata");
    card.append(metaEl);
    let outboxStateEl = null, cancellingEl = null, errEl = null, questionBox = null;
    if (out && !out.job) {
      outboxStateEl = node("p", out.state === "rejected" ? t("web.task.outbox.rejected") : t("web.task.outbox.delivering"), "result");
      card.append(outboxStateEl);
    }
    if (
      out?.cancel && out.state !== "rejected" &&
      !["succeeded", "failed", "cancelled", "completed_unverified"].includes(job.state)
    ) {
      cancellingEl = node("p", t("web.task.outbox.cancelling"), "result");
      card.append(cancellingEl);
    }
    if (job.result) card.append(node("div", job.result, "result"));
    const initialErr = formatCardError(job, out);
    if (initialErr) {
      errEl = node("div", initialErr, "error-text");
      card.append(errEl);
    }
    const artifactRefs = [];
    for (const a of job.artifacts || []) {
      const block = node("div", null, "artifact"),
        artTitle = node("strong", t("web.task.artifact_title")),
        meta = node("small", size(a.size));
      meta.title = t("web.task.artifact_sha", {sha: a.sha256});
      artifactRefs.push({artTitle, meta, sha256: a.sha256});
      block.append(artTitle, node("div", a.path), node("pre", a.preview), meta);
      card.append(block);
    }
    if (job.interaction?.state === "pending" && job.state === "waiting_input") {
      questionBox = question(job, out);
      card.append(questionBox.root);
    }
    const actions = node("div", null, "task-actions");
    const detailBtn = node("button", t("web.task.detail_btn"), "quiet");
    detailBtn.addEventListener("click", () => guarded(() => callbacks.detail(job, out)));
    actions.append(detailBtn);
    let cancelBtn = null, resolveBtn = null;
    if (
      ![
        "succeeded",
        "failed",
        "rejected",
        "cancelled",
        "completed_unverified",
        "reconciliation_required",
      ].includes(job.state)
    ) {
      cancelBtn = node("button", t("web.task.cancel_btn"), "quiet");
      cancelBtn.addEventListener("click", () =>
        guarded(async () => {
          await callbacks.cancel(job, out);
        }),
      );
      actions.append(cancelBtn);
    }
    if (job.state === "reconciliation_required" && !out) {
      resolveBtn = node("button", t("web.task.resolve_btn"), "secondary");
      resolveBtn.addEventListener("click", () => guarded(() => callbacks.resolve(job)));
      actions.append(resolveBtn);
    }
    card.append(actions);
    cardRelocalizers.push(() => {
      badgeEl.textContent = badgeLabel(job, out);
      metaEl.textContent = formatCardMeta(job, out);
      if (outboxStateEl) outboxStateEl.textContent = out.state === "rejected" ? t("web.task.outbox.rejected") : t("web.task.outbox.delivering");
      if (cancellingEl) cancellingEl.textContent = t("web.task.outbox.cancelling");
      if (errEl) errEl.textContent = formatCardError(job, out);
      for (const ref of artifactRefs) {
        ref.artTitle.textContent = t("web.task.artifact_title");
        ref.meta.title = t("web.task.artifact_sha", {sha: ref.sha256});
      }
      questionBox?.relocalize();
      detailBtn.textContent = t("web.task.detail_btn");
      if (cancelBtn) cancelBtn.textContent = t("web.task.cancel_btn");
      if (resolveBtn) resolveBtn.textContent = t("web.task.resolve_btn");
    });
    return card;
  }

  function question(job, out) {
    const draftKey = `${job.id}:${job.interaction.id}`;
    const root = node("div", null, "question");
    root.append(node("strong", job.interaction.question));
    const form = node("form"),
      input = node("input"),
      button = node("button", t("web.task.answer_btn"));
    input.value = answerDrafts.get(draftKey) ?? "";
    questionInputs.set(draftKey, input);
    input.addEventListener("input", () => {
      if (input.value) answerDrafts.set(draftKey, input.value);
      else answerDrafts.delete(draftKey);
    });
    input.addEventListener("focus", () => { editing = true; });
    input.addEventListener("blur", () => { editing = false; });
    input.required = true;
    input.maxLength = 4000;
    input.setAttribute("aria-label", t("web.task.answer_aria"));
    form.append(input, button);
    form.addEventListener("submit", (event) => {
      event.preventDefault();
      if (button.disabled) return;
      guarded(async () => {
        button.disabled = true;
        answeringCount++;
        let answered = false;
        try {
          answered = Boolean(await callbacks.answer(job, out, input.value));
        } finally {
          answeringCount--;
          button.disabled = false;
        }
        if (answered) {
          answerDrafts.delete(draftKey);
          input.blur();
          await callbacks.refresh();
        }
      });
    });
    root.append(form);
    return {
      root,
      relocalize: () => {
        input.setAttribute("aria-label", t("web.task.answer_aria"));
        button.textContent = t("web.task.answer_btn");
      },
    };
  }

  function buildSummaryItems(summary, job) {
    const item = (label, value) => {
      const cell = node("div");
      cell.append(node("span", label), node("strong", value));
      return cell;
    };
    summary.replaceChildren(
      item(t("web.task.detail.status"), stateText(job.state)),
      item(t("web.task.detail.execution"), outcomeText(job.execution_outcome || "not_run")),
      item(t("web.task.detail.verification"), outcomeText(job.verification_outcome || "not_run")),
      item(t("web.task.detail.acceptance"), outcomeText(job.user_acceptance || "pending")),
    );
  }

  function formatTechMeta(job) {
    return t("web.task.detail.tech_meta", {
      id: job.id,
      attempt: job.attempt || 0,
      created: date(job.created),
    });
  }

  // Readable outcome first; identifiers and raw events stay in a disclosure.
  function detail(job) {
    const root = $("#detail-content"), summary = node("div", null, "detail-summary");
    buildSummaryItems(summary, job);
    const technical = node("details", null, "advanced");
    const techSummary = node("summary", t("web.task.detail.tech_summary"));
    const techMeta = node("div", formatTechMeta(job), "metadata");
    technical.append(techSummary, techMeta);
    eventsRoot = node("div");
    technical.append(eventsRoot);
    detailState = {job, summary, techSummary, techMeta, eventRows: []};
    root.replaceChildren(node("div", job.request?.goal || "", "task-title"), summary, technical);
    dialog("#detail");
  }

  function events(items) {
    for (const event of items) {
      const row = node("div", null, "event");
      const header = node("strong", `#${event.seq} ${event.type} · ${date(event.time)}`);
      row.append(
        header,
        node("pre", JSON.stringify(event.data, null, 2)),
      );
      detailState?.eventRows.push({header, event});
      eventsRoot?.append(row);
    }
  }

  function relocalize() {
    for (const update of cardRelocalizers) update();
    paintLastRefresh();
    if (detailState && $("#detail")?.open) {
      buildSummaryItems(detailState.summary, detailState.job);
      detailState.techSummary.textContent = t("web.task.detail.tech_summary");
      detailState.techMeta.textContent = formatTechMeta(detailState.job);
      for (const entry of detailState.eventRows) {
        entry.header.textContent = `#${entry.event.seq} ${entry.event.type} · ${date(entry.event.time)}`;
      }
    }
  }

  return {
    render,
    detail,
    events,
    relocalize,
    detailOpen:() => $("#detail").open,
    editing:() => editing || answeringCount > 0,
    submissionChanged,
    submitted:goal => { if ($("#goal").value.trim() === goal) $("#goal").value = ""; },
  };
}
