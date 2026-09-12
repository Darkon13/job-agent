function storedAccount() { try { return window.localStorage.getItem("job-agent-account") || ""; } catch { return ""; } }
const state = {
  account: storedAccount(),
  applicationOffset: 0, applicationTotal: 0, applicationGroups: {}, applicationRequest: 0, applicationLoading: false,
  summary: null, selectedConversation: null, selectedMessages: [], jobs: [], applicationObjects: [],
  applicationFilter: "", applicationQuery: "", applicationSort: "updated_desc", selectedApplications: new Set(), applicationActionBusy: false, applicationActionMessage: "",
  conversationQuery: "", conversationFilter: "", conversationSort: "updated_desc", conversationReadBusy: new Set(), markAllReadBusy: false, conversationAnswerBusy: "",
  profileResources: [], profilePlans: new Map(), profileEditors: new Map(), profileRevisions: new Map(), profileMessages: new Map(), profileBusy: new Set(), taskBusy: new Set(), jobBusy: new Set(),
  reviewSessions: [], reviewSelected: null, reviewDetail: null, reviewBusy: false, reviewMessage: "",
};
const elements = Object.fromEntries([
  "application-prev", "application-next", "application-filters", "application-items", "application-filter-state", "application-search", "application-sort", "application-reset", "application-select-all", "application-selection-state", "application-bulk-action", "application-run-action", "tasks", "jobs", "campaigns", "failed-tasks", "activity", "activity-observations", "stats", "conversations", "conversation-search", "conversation-filter", "conversation-sort", "messages", "chat-title", "chat-meta", "chat-vacancy-link",
  "connection-dot", "connection-state", "runtime-version", "updated-at", "refresh", "mark-all-read", "conversation-bulk-state", "reply-form", "account-switcher",
  "reply", "send", "action-state",
  "profile-resources", "profile-state-state",
  "review-state", "review-filter", "review-refresh", "review-sessions", "review-session-title", "review-session-meta", "review-prompt",
].map((id) => [id.replace(/-([a-z])/g, (_, letter) => letter.toUpperCase()), document.querySelector(`#${id}`)]));
const taskTypeLabels = {
  "vacancy.search_page": "Получить страницу вакансий", "application.campaign": "Запустить рассылку откликов", "application.submit": "Отправить отклик", "application.remove": "Убрать отклик", "application.retention": "Очистить устаревшие отклики",
  "questionnaire.answer": "Ответить на анкету", "test.complete": "Пройти тест", "test.capture": "Сохранить вопросы теста", "review.answer": "Сохранить проверенный ответ",
  "conversation.reply": "Ответить в чате", "conversation.send": "Отправить сообщение", "conversation.follow_up": "Отправить напоминание", "conversation.follow_up.select": "Выбрать чат для напоминания",
  "conversation.discover": "Обновить список чатов", "conversation.mark_read": "Пометить чат прочитанным", "conversation.sync": "Загрузить сообщения чата", "vacancy.inspect": "Открыть и изучить вакансию",
  "resume.publish": "Опубликовать резюме", "resume.touch": "Поднять резюме", "resume.update": "Обновить резюме", "profile.activity.observe": "Обновить активность резюме",
  "profile.bootstrap": "Заполнить профиль", "profile_state.reconcile": "Сверить профиль с конфигурацией", "profile_state.apply": "Применить изменения профиля", "skill_verification.start": "Запустить проверку навыка",
  "calendar.find_slots": "Найти свободное время", "calendar.create_event": "Создать событие", "challenge.respond": "Ответить на проверку", "notification.deliver": "Доставить уведомление",
};
const applicationGroupLabels = {
  queued: "В очереди", sent: "Отправлено", needs_input: "Нужно участие", waiting_invitation: "Ожидает приглашения",
  invited: "Приглашение", rejected: "Отказ", state_unknown: "Состояние не синхронизировано", hidden: "Скрыт", not_sent: "Не отправлено",
};
const queuedApplicationStatuses = new Set(["new", "preparing", "ready", "submitting", "pending_reconciliation"]);
const inputDecisionCodes = new Set(["questionnaire_required", "vacancy_test_required", "platform_validation_required"]);
const taskStatusLabels = { new: "Ожидает", processing: "Выполняется", waiting_confirmation: "Нужно решение", retry_scheduled: "Повтор запланирован", completed: "Завершена", failed: "Ошибка", dismissed: "Закрыта" };
const campaignStatusLabels = { running: "Выполняется", target_reached: "Цель достигнута", exhausted: "Вакансии закончились", paused_budget: "Пауза: лимит", paused_rate_limit: "Пауза: rate limit", failed: "Ошибка" };
const conversationStatusLabels = { active: "Активный", closed: "Закрыт", rejected: "Отказ", archived: "Архив" };
const reviewStatusLabels = { pending: "Подготовка", waiting_answer: "Ждёт ответа", answer_recorded: "Ответ записан", completed: "Завершена", cancelled: "Отменена", unsupported: "Не поддерживается", expired: "Истекла" };
const activityKindLabels = { "vacancy.inspected": "Просмотрена вакансия", "application.submitted": "Отправлен отклик", "conversation.message_sent": "Отправлено сообщение", "resume.touched": "Поднято резюме" };
const decisionLabels = { qualified: "Проверки пройдены", resume_not_suitable: "HH не предлагает доступного резюме", questionnaire_required: "Нужно заполнить анкету", vacancy_test_required: "Нужно пройти тест", platform_validation_required: "Платформа запросила дополнительные данные", cover_letter_required: "Не удалось подготовить обязательное сопроводительное", vacancy_closed: "Вакансия закрыта", already_applied: "Отклик уже существует" };
const failureLabels = { temporary_failure: "Временная ошибка — будет повтор", rate_limited: "Платформа ограничила частоту запросов", quota_exceeded: "Исчерпан дневной лимит", unauthorized: "Нужно обновить авторизацию", validation_required: "Платформа запросила дополнительные данные", permanent_failure: "Платформа отклонила операцию", ambiguous_result: "Результат отправки нужно сверить" };

function text(tag, value, className = "") { const node = document.createElement(tag); node.textContent = value; if (className) node.className = className; return node; }
function statusCell(value, className = "") { const cell = document.createElement("td"); cell.append(text("span", value, `status ${className}`.trim())); return cell; }
function formatDate(value) { return value ? new Intl.DateTimeFormat("ru-RU", { dateStyle: "short", timeStyle: "medium" }).format(new Date(value)) : "—"; }
function taskTypeLabel(value) { return taskTypeLabels[value] || value; }
function taskStatusLabel(value) { return taskStatusLabels[value] || value || "—"; }
function conversationLabel(item) { return item.vacancy_title || `Диалог ${item.platform}`; }
function total(items, predicate = () => true) { return items.filter(predicate).reduce((sum, item) => sum + Number(item.count || 0), 0); }
function safeExternalURL(value) { try { const url = new URL(value); return ["http:", "https:"].includes(url.protocol) ? url.href : ""; } catch (_) { return ""; } }
function compactID(value) { const id = String(value || ""); return id.length > 20 ? `${id.slice(0, 8)}…${id.slice(-6)}` : id || "—"; }
function applicationGroup(item) {
  if (item.status === "submitted" || item.decision_code === "already_applied") {
    if (Object.prototype.hasOwnProperty.call(item, "count")) return "sent";
    switch (item.disposition) {
    case "pending": return "waiting_invitation";
    case "invited": return "invited";
    case "rejected": return "rejected";
    case "hidden": return "hidden";
    default: return "state_unknown";
    }
  }
  if (item.status === "waiting_validation" && inputDecisionCodes.has(item.decision_code)) return "needs_input";
  if (queuedApplicationStatuses.has(item.status)) return "queued";
  return "not_sent";
}
function applicationIsSent(item) { return ["waiting_invitation", "invited", "rejected", "state_unknown", "hidden"].includes(applicationGroup(item)); }
function applicationReason(item) {
  if (item.disposition === "invited") return "HH перевёл отклик в «Приглашение» — автоматическая очистка запрещена";
  if (item.disposition === "rejected") return "HH подтвердил отказ — объект подходит для автоматической очистки";
  if (item.disposition === "pending") return item.viewed_by_opponent === true ? "Работодатель посмотрел отклик, но приглашения нет" : "Приглашения ещё нет";
  if ((item.status === "submitted" || item.decision_code === "already_applied") && !item.disposition) return "Состояние отклика ещё не синхронизировано с HH";
  if (item.decision_code && item.decision_code !== "qualified") return decisionLabels[item.decision_code] || item.decision_code;
  if (item.failure_category) return failureLabels[item.failure_category] || `Ошибка: ${item.failure_category}`;
  switch (item.status) {
  case "new": return "Ожидает обработки";
  case "preparing": return "Подготавливается отклик";
  case "ready": return "Подготовлен, ожидает доступного лимита";
  case "submitting": return "Отправляется на платформу";
  case "pending_reconciliation": return "Проверяется результат отправки";
  case "submitted": return "Отправка подтверждена платформой";
  case "dry_run": return "Старый проверочный запуск — отклик не отправлялся";
  case "waiting_approval": return "Старый режим ручного подтверждения";
  case "skipped": return "Платформа не позволила отправить отклик";
  case "failed": return "Не удалось выполнить отправку";
  default: return decisionLabels[item.decision_code] || "—";
  }
}

function renderStats(summary = {}) {
  const applications = summary.applications || [];
  const metrics = [
    { value: total(applications, (item) => item.status === "submitted" || item.decision_code === "already_applied"), label: "Отклики отправлены", filter: "sent" },
    { value: total(applications, (item) => applicationGroup(item) === "queued"), label: "Ожидают отправки", filter: "queued" },
    { value: total(applications, (item) => applicationGroup(item) === "needs_input"), label: "Нужно участие", filter: "needs_input" },
    { value: (summary.conversations || []).filter((item) => item.status === "active").length, label: "Активные диалоги", target: "conversations-title" },
  ];
  elements.stats.replaceChildren(...metrics.map((metric) => {
    const card = document.createElement(metric.filter || metric.target ? "button" : "article"); card.className = "stat-card";
    card.append(text("strong", String(metric.value)), text("span", metric.label));
    if (metric.filter) card.addEventListener("click", () => setApplicationFilter(metric.filter));
    if (metric.target) card.addEventListener("click", () => document.querySelector(`#${metric.target}`)?.scrollIntoView({ behavior: "smooth" }));
    return card;
  }));
}

function applicationMatchesFilter(item) {
  if (state.applicationFilter === "sent" && !applicationIsSent(item)) return false;
  if (state.applicationFilter && state.applicationFilter !== "sent" && applicationGroup(item) !== state.applicationFilter) return false;
  const query = state.applicationQuery.trim().toLocaleLowerCase("ru");
  return !query || [item.vacancy_title, item.employer, item.profile_id, item.decision_code, applicationReason(item)].some((value) => String(value || "").toLocaleLowerCase("ru").includes(query));
}
function setApplicationFilter(value) {
  state.applicationFilter = state.applicationFilter === value ? "" : value;
  state.applicationOffset = 0; state.selectedApplications.clear(); refreshApplications();
  document.querySelector("#applications-section")?.scrollIntoView({ behavior: "smooth", block: "start" });
}
function renderApplicationFilters(items = []) {
  const grouped = new Map(Object.entries(state.applicationGroups));
  const filters = [["", "Все", grouped.get("") || 0], ...["queued", "needs_input", "waiting_invitation", "invited", "rejected", "state_unknown", "hidden", "not_sent"].filter((group) => grouped.get(group)).map((group) => [group, applicationGroupLabels[group], grouped.get(group)])];
  elements.applicationFilters.replaceChildren(...filters.map(([value, label, count]) => {
    const button = document.createElement("button"); button.type = "button"; button.className = `filter-card${state.applicationFilter === value ? " active" : ""}`;
    button.append(text("strong", String(count)), text("span", label)); button.addEventListener("click", () => setApplicationFilter(value)); return button;
  }));
}
function visibleApplicationObjects() { return state.applicationObjects; }
function applicationCanRemove(item) { return ["waiting_validation", "waiting_approval", "submitted", "dry_run", "skipped", "failed"].includes(item.status); }

async function retryApplication(item, button) {
  state.applicationActionBusy = true; button.disabled = true; state.applicationActionMessage = "Ставлю повторную подготовку…"; renderApplicationObjects();
  try {
    const task = await enqueue(`/api/v1/applications/${encodeURIComponent(item.id)}/retry`);
    state.applicationActionMessage = `Отклик ${item.id}: задача ${task.task_id}`;
    await refreshApplications();
  } catch (error) { state.applicationActionMessage = error.message; }
  state.applicationActionBusy = false; renderApplicationObjects();
}
function applicationListURL() {
  const parameters = {limit: "200", offset: String(state.applicationOffset), q: state.applicationQuery, sort: state.applicationSort, group: state.applicationFilter};
  if (state.account) parameters.profile_id = state.account;
  return "/api/v1/applications?" + new URLSearchParams(parameters);
}
async function refreshApplications() {
  const generation = ++state.applicationRequest;
  state.applicationLoading = true; updateApplicationSelection();
  try {
    const data = await request(applicationListURL());
    if (generation !== state.applicationRequest) return;
    state.applicationObjects = data.items || []; state.applicationTotal = data.total || 0; state.applicationGroups = data.groups || {};
    if (state.applicationOffset >= state.applicationTotal && state.applicationOffset > 0) { state.applicationOffset = 0; return refreshApplications(); }
    renderApplicationFilters(); renderApplicationObjects();
  } catch (error) { if (generation === state.applicationRequest) state.applicationActionMessage = error.message; }
  finally {
    if (generation === state.applicationRequest) {
      state.applicationLoading = false; renderApplicationObjects();
      elements.applicationPrev.disabled = state.applicationOffset === 0;
      elements.applicationNext.disabled = state.applicationOffset + 200 >= state.applicationTotal;
    }
  }
}
function changeApplicationQuery() {
  state.applicationOffset = 0; state.selectedApplications.clear(); state.applicationActionMessage = ""; return refreshApplications();
}
function updateApplicationSelection(items = visibleApplicationObjects()) {
  const visibleIDs = items.filter(applicationCanRemove).map((item) => item.id);
  elements.applicationSelectAll.disabled = state.applicationLoading || state.applicationActionBusy || !visibleIDs.length;
  const selectedVisible = visibleIDs.filter((id) => state.selectedApplications.has(id)).length;
  elements.applicationSelectAll.checked = visibleIDs.length > 0 && selectedVisible === visibleIDs.length;
  elements.applicationSelectAll.indeterminate = selectedVisible > 0 && selectedVisible < visibleIDs.length;
  elements.applicationSelectionState.textContent = state.applicationActionMessage || (state.selectedApplications.size ? `Выбрано: ${state.selectedApplications.size}` : "Ничего не выбрано");
  elements.applicationBulkAction.disabled = state.selectedApplications.size === 0 || state.applicationActionBusy || state.applicationLoading;
  elements.applicationRunAction.disabled = state.selectedApplications.size === 0 || !elements.applicationBulkAction.value || state.applicationActionBusy || state.applicationLoading;
}
function renderApplicationObjects() {
  const existingIDs = new Set(state.applicationObjects.map((item) => item.id));
  for (const id of state.selectedApplications) if (!existingIDs.has(id)) state.selectedApplications.delete(id);
  const items = visibleApplicationObjects();
  elements.applicationFilterState.textContent = `${state.applicationTotal ? state.applicationOffset + 1 : 0}–${state.applicationOffset + items.length} из ${state.applicationTotal} по фильтру`;
  if (!items.length) { const row = document.createElement("tr"); const cell = text("td", "Под этот фильтр откликов нет"); cell.colSpan = 9; row.append(cell); elements.applicationItems.replaceChildren(row); updateApplicationSelection(items); return; }
  elements.applicationItems.replaceChildren(...items.map((item) => {
    const row = document.createElement("tr");
    const selection = document.createElement("td"); const checkbox = document.createElement("input"); checkbox.type = "checkbox"; checkbox.className = "application-select"; checkbox.disabled = !applicationCanRemove(item) || state.applicationActionBusy || state.applicationLoading; checkbox.checked = state.selectedApplications.has(item.id); checkbox.setAttribute("aria-label", `Выбрать ${item.vacancy_title || item.id}`);
    checkbox.addEventListener("change", () => { if (checkbox.checked) state.selectedApplications.add(item.id); else state.selectedApplications.delete(item.id); updateApplicationSelection(items); }); selection.append(checkbox);
    const vacancy = document.createElement("td"); vacancy.append(text("strong", item.vacancy_title || "Без названия"));
    const action = document.createElement("td"); const url = safeExternalURL(item.vacancy_url);
    if (url) { const link = text("a", "Открыть ↗", "table-link"); link.href = url; link.target = "_blank"; link.rel = "noopener noreferrer"; action.append(link); }
    if (["waiting_validation", "failed"].includes(item.status)) {
      if (action.childNodes.length) action.append(document.createTextNode(" "));
      const retry = text("button", "Повторить", "secondary compact"); retry.type = "button"; retry.disabled = state.applicationActionBusy;
      retry.addEventListener("click", () => retryApplication(item, retry)); action.append(retry);
    }
    if (!action.childNodes.length) action.textContent = "—";
    const group = applicationGroup(item);
    row.append(selection, vacancy, text("td", item.employer || "—"), text("td", item.profile_id), statusCell(applicationGroupLabels[group], `status-${group}`), tailoringCell(item), text("td", applicationReason(item)), text("td", formatDate(item.updated_at)), action);
    return row;
  }));
  updateApplicationSelection(items);
}
const tailoringStatusLabels = { planned: "Готовится", applying: "Применяется", applied: "Применено", submitting: "Перед отправкой", restoring: "Восстанавливается", restored: "Восстановлено", recovery_required: "Нужно восстановление" };
function tailoringCell(item) {
  const tailoring = item.tailoring;
  if (!tailoring) return text("td", "—");
  const cell = document.createElement("td");
  const recovery = tailoring.status === "recovery_required";
  const badge = text("span", tailoringStatusLabels[tailoring.status] || tailoring.status, recovery ? "tailoring-status tailoring-recovery" : "tailoring-status");
  const changed = (tailoring.changes || []).flatMap((change) => (change.added || []).map((skill) => `+${skill}`).concat((change.removed || []).map((skill) => `-${skill}`)));
  if (changed.length) badge.title = changed.join(", ");
  cell.append(badge);
  if (tailoring.recovery_reason) cell.append(text("span", tailoring.recovery_reason, "tailoring-reason"));
  return cell;
}
function renderTasks(items = []) {
  const queued = items.filter((item) => !["completed", "dismissed"].includes(item.status));
  if (!queued.length) { const row = document.createElement("tr"); const cell = text("td", "Очередь пуста"); cell.colSpan = 4; row.append(cell); elements.tasks.replaceChildren(row); return; }
  elements.tasks.replaceChildren(...queued.map((item) => {
    const row = document.createElement("tr"); row.append(text("td", taskTypeLabel(item.type)), statusCell(taskStatusLabel(item.status), `task-${item.status}`), text("td", String(item.priority)), text("td", String(item.count))); return row;
  }));
}
function renderJobs(items = []) {
  items = state.account ? items.filter((item) => item.profile_id === state.account) : items;
  if (!items.length) { const row = document.createElement("tr"); const cell = text("td", "Нет доступных jobs: проверьте enabled, авторизацию и capabilities профиля"); cell.colSpan = 7; row.append(cell); elements.jobs.replaceChildren(row); return; }
  elements.jobs.replaceChildren(...items.map((item) => {
    const row = document.createElement("tr");
    const flow = item.task_type === "application.campaign" ? "Поиск → очередь откликов → отправка → результат" : item.task_type === "application.retention" ? "Синхронизация → отбор по сроку/отказу → повторная проверка → локальная очистка" : `Очередь → ${taskTypeLabel(item.task_type)} → результат`;
    const action = document.createElement("td");
    const run = text("button", "Запустить", "secondary compact"); run.type = "button"; run.disabled = state.jobBusy.has(item.tag);
    run.addEventListener("click", () => runJob(item)); action.append(run);
    row.append(text("td", item.tag), text("td", taskTypeLabel(item.task_type)), text("td", item.platform), text("td", item.profile_id), text("td", String(item.priority)), text("td", flow), action);
    return row;
  }));
}
function renderFailedTasks(items = []) {
  items = state.account ? items.filter((item) => item.profile_id === state.account) : items;
  if (!items.length) { const row = document.createElement("tr"); const cell = text("td", "Неразрешённых ошибок нет"); cell.colSpan = 7; row.append(cell); elements.failedTasks.replaceChildren(row); return; }
  elements.failedTasks.replaceChildren(...items.map((item) => {
    const row = document.createElement("tr");
    const error = `${item.failure?.category || "unknown"}${item.failure?.message ? `: ${item.failure.message}` : ""}`;
    const actions = document.createElement("td"); actions.className = "task-actions";
    const retry = text("button", "Retry", "secondary compact"); retry.type = "button"; retry.disabled = state.taskBusy.has(item.id);
    retry.addEventListener("click", () => controlFailedTask(item, "retry"));
    const dismiss = text("button", "Dismiss", "secondary compact"); dismiss.type = "button"; dismiss.disabled = state.taskBusy.has(item.id);
    dismiss.addEventListener("click", () => controlFailedTask(item, "dismiss"));
    actions.append(retry, dismiss);
    row.append(text("td", item.id, "task-id"), text("td", taskTypeLabel(item.type)), text("td", item.profile_id || "—"), text("td", String(item.attempts)), text("td", error, "task-error"), text("td", formatDate(item.updated_at)), actions);
    return row;
  }));
}
function renderCampaigns(items = []) {
  if (!items.length) { const row = document.createElement("tr"); const cell = text("td", "Запусков пока нет"); cell.colSpan = 6; row.append(cell); elements.campaigns.replaceChildren(row); return; }
  elements.campaigns.replaceChildren(...items.map((item) => {
    const row = document.createElement("tr");
    const grouped = new Map();
    for (const entry of item.applications || []) { const group = applicationGroup(entry); grouped.set(group, (grouped.get(group) || 0) + Number(entry.count || 0)); }
    const outcomes = [...grouped.entries()].map(([group, count]) => `${applicationGroupLabels[group]}: ${count}`).join(" · ") || "нет откликов";
    const status = item.stop_reason ? `${campaignStatusLabels[item.status] || item.status}: ${item.stop_reason}` : campaignStatusLabels[item.status] || item.status;
    row.append(text("td", item.id, "task-id"), text("td", item.job_tag), text("td", status), text("td", String(item.target_successful)), text("td", outcomes), text("td", formatDate(item.updated_at)));
    return row;
  }));
}
function renderActivity(items = []) {
  items = state.account ? items.filter((item) => item.profile_id === state.account) : items;
  if (!items.length) { const row = document.createElement("tr"); const cell = text("td", "Подтверждённых действий пока нет"); cell.colSpan = 5; row.append(cell); elements.activity.replaceChildren(row); return; }
  elements.activity.replaceChildren(...items.map((item) => {
    const row = document.createElement("tr");
    row.append(text("td", item.profile_id), text("td", item.platform), text("td", activityKindLabels[item.kind] || item.kind), text("td", String(item.count)), text("td", formatDate(item.last_occurred_at)));
    return row;
  }));
}
function counter(value) { return value === null || value === undefined ? "—" : String(value); }
function renderActivityObservations(items = []) {
  items = state.account ? items.filter((item) => item.profile_id === state.account) : items;
  const latest = [];
  const seen = new Set();
  for (const item of items) {
    const key = `${item.platform}\u0000${item.profile_id}`;
    if (seen.has(key)) continue;
    seen.add(key); latest.push(item);
  }
  if (!latest.length) { elements.activityObservations.replaceChildren(text("p", "Показатели ещё не снимались. Запустите job «Обновить активность резюме».", "empty panel")); return; }
  elements.activityObservations.replaceChildren(...latest.map((item) => {
    const card = document.createElement("article"); card.className = "panel activity-card";
    const heading = document.createElement("div"); heading.className = "panel-heading";
    const identity = document.createElement("div"); const resume = text("p", `${item.platform} · резюме ${compactID(item.resume_id)}`, "muted"); resume.title = item.resume_id; identity.append(text("h2", item.profile_id), resume);
    heading.append(identity, text("span", item.period_days === undefined ? "период не указан" : `${item.period_days} дней`, "tag")); card.append(heading);
    const metrics = document.createElement("div"); metrics.className = "activity-metrics";
    const score = item.score === null || item.score === undefined ? "—" : `${item.score}%`;
    [["Активность", score, ""], ["Показы в поиске", counter(item.search_shows), ""], ["Просмотры", counter(item.views), item.new_views ? `+${item.new_views}` : ""], ["Приглашения", counter(item.invitations), item.new_invitations ? `+${item.new_invitations}` : ""]].forEach(([label, value, delta]) => {
      const metric = document.createElement("div"); metric.append(text("span", label), text("strong", value), delta ? text("small", delta) : document.createTextNode("")); metrics.append(metric);
    });
    const scoreState = item.score === null || item.score === undefined ? " · точный процент не найден в ответе HH" : "";
    card.append(metrics, text("p", `Снято ${formatDate(item.observed_at)}${item.score_hidden ? " · шкала скрыта экспериментом HH" : scoreState}`, "muted")); return card;
  }));
}
function visibleConversations(items = []) {
  const query = state.conversationQuery.trim().toLocaleLowerCase("ru");
  const filtered = items.filter((item) => {
    if (state.account && item.profile_id !== state.account) return false;
    if (state.conversationFilter === "unread" && !item.unread_count) return false;
    if (state.conversationFilter && state.conversationFilter !== "unread" && item.status !== state.conversationFilter) return false;
    return !query || [item.vacancy_title, item.employer, item.profile_id, conversationStatusLabels[item.status]].some((value) => String(value || "").toLocaleLowerCase("ru").includes(query));
  });
  const stringCompare = (left, right) => String(left || "").localeCompare(String(right || ""), "ru", { sensitivity: "base" });
  return filtered.sort((left, right) => {
    switch (state.conversationSort) {
    case "updated_asc": return new Date(left.updated_at) - new Date(right.updated_at);
    case "unread_desc": return Number(right.unread_count || 0) - Number(left.unread_count || 0) || new Date(right.updated_at) - new Date(left.updated_at);
    case "employer_asc": return stringCompare(left.employer, right.employer) || new Date(right.updated_at) - new Date(left.updated_at);
    default: return new Date(right.updated_at) - new Date(left.updated_at);
    }
  });
}
function renderConversations(items = []) {
  updateMarkAllRead(items);
  const visible = visibleConversations(items);
  if (!visible.length) { elements.conversations.replaceChildren(text("p", items.length ? "Под этот фильтр диалогов нет" : "Диалогов пока нет", "empty")); return; }
  elements.conversations.replaceChildren(...visible.map((item) => {
    const button = document.createElement("button"); button.type = "button"; button.className = `conversation${state.selectedConversation?.id === item.id ? " active" : ""}`;
    const heading = document.createElement("span"); heading.className = "conversation-heading"; heading.append(text("strong", conversationLabel(item)));
    if (item.unread_count) heading.append(text("span", String(item.unread_count), "unread-badge"));
    button.append(heading, text("span", item.employer || "Компания не определена", "conversation-employer"), text("small", `${item.profile_id} · ${conversationStatusLabels[item.status] || item.status} · ${formatDate(item.updated_at)}`));
    button.addEventListener("click", () => selectConversation(item)); return button;
  }));
}
function updateMarkAllRead(items = []) {
  const unread = items.reduce((sum, item) => sum + Number(item.unread_count || 0), 0);
  const pending = (state.summary?.tasks || []).some((item) => item.type === "conversation.mark_read" && ["new", "processing", "retry_scheduled", "waiting_confirmation"].includes(item.status));
  elements.markAllRead.textContent = unread ? `Прочитать все (${unread})` : "Все прочитано";
  elements.markAllRead.disabled = state.markAllReadBusy || pending || unread === 0;
  if (pending) elements.conversationBulkState.textContent = "Прочтение уже выполняется";
}
function renderMessages(items = []) {
  if (!items.length) { elements.messages.replaceChildren(text("p", "В этом диалоге сообщений пока нет.", "empty")); return; }
  elements.messages.replaceChildren(...items.map((item) => {
    const article = document.createElement("article"); article.className = `message ${item.direction || ""}`;
    article.append(text("p", item.text || `[${item.kind}]`));
    if ((item.options || []).length) {
      const options = document.createElement("div"); options.className = "message-options";
      for (const option of item.options) {
        const button = text("button", option.text, "message-option");
        button.type = "button";
        button.disabled = item.direction === "outgoing" || item.status === "queued" || state.conversationAnswerBusy === `${item.id}:${option.id}`;
        button.addEventListener("click", () => sendQuestionnaireOption(item, option));
        options.append(button);
      }
      article.append(options);
    }
    const meta = document.createElement("div"); meta.className = "message-meta";
    meta.append(text("span", item.direction === "outgoing" ? "Вы" : "Собеседник"), text("span", item.status === "queued" ? "В очереди" : formatDate(item.occurred_at)));
    article.append(meta); return article;
  })); elements.messages.scrollTop = elements.messages.scrollHeight;
}

async function sendQuestionnaireOption(message, option) {
  const conversation = state.selectedConversation;
  if (!conversation || !option?.text) return;
  if (!globalThis.confirm(`Отправить вариант «${option.text}»?`)) return;
  const key = `dashboard-answer:${conversation.id}:${message.id}:${option.id}`;
  state.conversationAnswerBusy = `${message.id}:${option.id}`;
  elements.actionState.textContent = `Отправляю «${option.text}»…`;
  renderMessages(state.selectedMessages);
  try {
    const result = await enqueue(`/api/v1/conversations/${encodeURIComponent(conversation.id)}/messages`, {
      content: { text: option.text },
    }, key);
    elements.actionState.textContent = result.created ? `Задача ${result.task_id} поставлена в очередь` : `Ответ уже поставлен ранее (${result.task_id})`;
    if (state.selectedConversation?.id === conversation.id) {
      state.selectedMessages = [...state.selectedMessages, { id: `queued:${result.task_id}`, direction: "outgoing", kind: "text", status: "queued", text: option.text, occurred_at: new Date().toISOString() }];
      renderMessages(state.selectedMessages);
    }
    await refreshSummary();
  } catch (error) {
    elements.actionState.textContent = error.message;
  }
  state.conversationAnswerBusy = "";
  renderMessages(state.selectedMessages);
}

function renderProfileResources() {
  if (!state.profileResources.length) { elements.profileResources.replaceChildren(text("p", "Desired-state ресурсов пока нет.", "empty panel")); return; }
  elements.profileResources.replaceChildren(...state.profileResources.map((resource) => {
    const card = document.createElement("article"); card.className = "panel resource-card";
    const heading = document.createElement("div"); heading.className = "panel-heading";
    const identity = document.createElement("div"); identity.append(text("h2", resource.tag), text("p", `${resource.profile_id} · ${resource.ownership}`, "muted"));
    const capabilities = text("span", `${resource.readable ? "read" : "no read"} · ${resource.writable ? "write" : "no write"}`, `tag ${resource.readable && resource.writable ? "ready" : ""}`);
    heading.append(identity, capabilities); card.append(heading);
    const paths = document.createElement("ul"); paths.className = "path-list";
    for (const path of resource.paths || []) paths.append(text("li", path));
    card.append(paths);
    const editor = state.profileEditors.get(resource.tag);
    if (editor) {
      const form = document.createElement("div"); form.className = "resource-editor";
      form.append(text("p", "Одноразовое изменение: конфигурационный resource останется источником истины.", "muted"));
      for (const field of editor.fields) {
        const label = text("label", field.path);
        const input = document.createElement("textarea"); input.rows = 6; input.value = field.draftValue; input.setAttribute("aria-label", field.path);
        input.addEventListener("input", () => { field.draftValue = input.value; });
        label.append(input); form.append(label);
      }
      const editorActions = document.createElement("div"); editorActions.className = "editor-actions";
      const cancel = text("button", "Отмена", "secondary"); cancel.type = "button"; cancel.disabled = state.profileBusy.has(resource.tag);
      cancel.addEventListener("click", () => { state.profileEditors.delete(resource.tag); renderProfileResources(); });
      const build = text("button", "Построить план изменения"); build.type = "button"; build.disabled = state.profileBusy.has(resource.tag);
      build.addEventListener("click", () => planProfileState(resource, build, editor));
      editorActions.append(cancel, build); form.append(editorActions); card.append(form);
    }
    const plan = state.profilePlans.get(resource.tag);
    if (plan) {
      const result = document.createElement("div"); result.className = "plan-result";
      result.append(text("strong", plan.status === "no_changes" ? "Изменений нет" : `Изменений: ${(plan.changes || []).length}`));
      for (const change of plan.changes || []) result.append(text("code", `${change.operation} ${change.path}`));
      card.append(result);
    }
    const revisions = state.profileRevisions.get(resource.tag);
    if (revisions) {
      const history = document.createElement("div"); history.className = "revision-history";
      history.append(text("strong", `История ревизий: ${revisions.length}`));
      if (!revisions.length) history.append(text("p", "Применённых ревизий пока нет.", "muted"));
      for (const revision of revisions) {
        const item = document.createElement("div"); item.className = "revision-item";
        item.append(text("span", `${formatDate(revision.applied_at)} · ${revision.source}`, "muted"));
        for (const change of revision.changes || []) item.append(text("code", `${change.operation} ${change.path}`));
        history.append(item);
      }
      card.append(history);
    }
    const actions = document.createElement("div"); actions.className = "resource-actions";
    const status = text("span", state.profileMessages.get(resource.tag) || "", "muted");
    const planButton = text("button", "Построить план"); planButton.type = "button"; planButton.disabled = !resource.readable || state.profileBusy.has(resource.tag);
    planButton.addEventListener("click", () => planProfileState(resource, planButton)); actions.append(status, planButton);
    const historyButton = text("button", revisions ? "Обновить историю" : "История ревизий", "secondary"); historyButton.type = "button"; historyButton.disabled = state.profileBusy.has(resource.tag);
    historyButton.addEventListener("click", () => loadProfileRevisions(resource)); actions.append(historyButton);
    if ((resource.editable_paths || []).length) {
      const editButton = text("button", editor ? "Редактор открыт" : "Изменить desired", "secondary"); editButton.type = "button"; editButton.disabled = !resource.readable || Boolean(editor) || state.profileBusy.has(resource.tag);
      editButton.addEventListener("click", () => loadProfileEditor(resource)); actions.append(editButton);
    }
    if (resource.reconcilable) {
      const reconcileButton = text("button", "Сверить и применить", "secondary"); reconcileButton.type = "button"; reconcileButton.disabled = state.profileBusy.has(resource.tag);
      reconcileButton.addEventListener("click", () => reconcileProfileState(resource)); actions.append(reconcileButton);
    }
    if (plan?.status === "planned") {
      const applyButton = text("button", "Применить", "secondary"); applyButton.type = "button"; applyButton.disabled = !resource.writable || state.profileBusy.has(resource.tag);
      applyButton.addEventListener("click", () => applyProfileState(resource, plan, applyButton)); actions.append(applyButton);
    }
    card.append(actions); return card;
  }));
}

async function refreshProfileResources() {
  try {
    const resources = await request("/api/v1/profile-state/resources");
    for (const resource of resources) {
      const plan = state.profilePlans.get(resource.tag);
      if (plan && (plan.source_manifest_digest || plan.manifest_digest) !== resource.manifest_digest) {
        state.profilePlans.delete(resource.tag); state.profileMessages.set(resource.tag, "Resource изменился — постройте новый план");
      }
      const editor = state.profileEditors.get(resource.tag);
      if (editor && editor.manifest_digest !== resource.manifest_digest) {
        state.profileEditors.delete(resource.tag); state.profileMessages.set(resource.tag, "Resource изменился — откройте редактор заново");
      }
    }
    state.profileResources = resources;
    elements.profileStateState.textContent = `${state.profileResources.length} ресурсов`;
    renderProfileResources();
  } catch (error) {
    elements.profileStateState.textContent = error.message;
    elements.profileResources.replaceChildren(text("p", "Не удалось загрузить desired state.", "empty panel"));
  }
}

async function loadProfileEditor(resource) {
  state.profileBusy.add(resource.tag); state.profileMessages.set(resource.tag, "Загружаю desired state…"); renderProfileResources();
  try {
    const editor = await request(`/api/v1/profile-state/resources/${encodeURIComponent(resource.tag)}/editor`);
    editor.fields = (editor.fields || []).map((field) => ({ ...field, draftValue: field.value ?? "" }));
    state.profileEditors.set(resource.tag, editor); state.profileMessages.set(resource.tag, "Редактируется одноразовый override");
  } catch (error) { state.profileMessages.set(resource.tag, error.message); }
  state.profileBusy.delete(resource.tag); renderProfileResources();
}

async function loadProfileRevisions(resource) {
  state.profileBusy.add(resource.tag); state.profileMessages.set(resource.tag, "Загружаю историю ревизий…"); renderProfileResources();
  try {
    const result = await request(`/api/v1/profile-state/revisions?resource_tag=${encodeURIComponent(resource.tag)}&limit=20`);
    const revisions = result.items || [];
    state.profileRevisions.set(resource.tag, revisions);
    state.profileMessages.set(resource.tag, revisions.length ? `Ревизий: ${revisions.length}` : "Применённых ревизий пока нет");
  } catch (error) { state.profileMessages.set(resource.tag, error.message); }
  state.profileBusy.delete(resource.tag); renderProfileResources();
}

async function planProfileState(resource, button, editor = null) {
  button.disabled = true; state.profileBusy.add(resource.tag); state.profileMessages.set(resource.tag, "Читаю текущее состояние…"); renderProfileResources();
  try {
    const options = { method: "POST" };
    if (editor) {
      options.headers = { "Content-Type": "application/json" };
      options.body = JSON.stringify({
        base_manifest_digest: editor.manifest_digest,
        overrides: editor.fields.map((field) => ({ path: field.path, value: field.draftValue.trim() === "" ? null : field.draftValue })),
      });
    }
    const proposal = await request(`/api/v1/profile-state/resources/${encodeURIComponent(resource.tag)}/plans`, options);
    proposal.source_manifest_digest = resource.manifest_digest;
    state.profilePlans.set(resource.tag, proposal); state.profileMessages.set(resource.tag, `План ${proposal.id}`);
    if (editor) state.profileEditors.delete(resource.tag);
  } catch (error) { state.profileMessages.set(resource.tag, error.message); }
  state.profileBusy.delete(resource.tag);
  renderProfileResources();
}

async function applyProfileState(resource, proposal, button) {
  button.disabled = true; state.profileBusy.add(resource.tag); state.profileMessages.set(resource.tag, "Ставлю применение в очередь…"); renderProfileResources();
  try {
    const task = await request(`/api/v1/profile-state/proposals/${encodeURIComponent(proposal.id)}/apply`, { method: "POST" });
    state.profileMessages.set(resource.tag, `Задача ${task.id}: ${task.status}`); await refreshSummary();
  } catch (error) { state.profileMessages.set(resource.tag, error.message); }
  state.profileBusy.delete(resource.tag);
  renderProfileResources();
}
async function reconcileProfileState(resource) {
  state.profileBusy.add(resource.tag); state.profileMessages.set(resource.tag, "Ставлю reconcile в очередь…"); renderProfileResources();
  try {
    const task = await enqueue(`/api/v1/profile-state/resources/${encodeURIComponent(resource.tag)}/reconcile`);
    state.profileMessages.set(resource.tag, `Reconcile ${task.id}: ${task.status}`); await refreshSummary();
  } catch (error) { state.profileMessages.set(resource.tag, error.message); }
  state.profileBusy.delete(resource.tag); renderProfileResources();
}

function renderAccountSwitcher(profiles = []) {
  const values = new Set(profiles);
  for (const item of state.summary?.conversations || []) values.add(item.profile_id);
  for (const item of state.summary?.activity || []) values.add(item.profile_id);
  for (const item of state.failedTasks || []) values.add(item.profile_id);
  for (const item of state.jobs || []) values.add(item.profile_id);
  for (const item of state.reviewSessions || []) values.add(item.profile_id);
  for (const item of state.applicationObjects || []) values.add(item.profile_id);
  values.delete(""); values.delete(undefined); values.delete(null);
  const options = [...values].sort();
  if (state.account && !options.includes(state.account)) state.account = "";
  const select = elements.accountSwitcher;
  const all = document.createElement("option"); all.value = ""; all.textContent = "Все аккаунты";
  select.replaceChildren(all, ...options.map((value) => {
    const option = document.createElement("option"); option.value = value; option.textContent = value; return option;
  }));
  select.value = state.account;
}

function reviewStatusLabel(value) { return reviewStatusLabels[value] || value || "—"; }

async function refreshReviewSessions() {
  try {
    const parameters = new URLSearchParams({limit: "50"});
    if (elements.reviewFilter.value) parameters.set("status", elements.reviewFilter.value);
    if (state.account) parameters.set("profile_id", state.account);
    const query = `?${parameters}`;
    const result = await request(`/api/v1/review-sessions${query}`);
    state.reviewSessions = result.items || [];
    elements.reviewState.textContent = state.reviewSessions.length ? `Сессий: ${state.reviewSessions.length}` : "Нет сессий";
    renderReviewSessions();
  } catch (error) {
    elements.reviewState.textContent = error.message;
    elements.reviewSessions.replaceChildren(text("p", "Не удалось загрузить проверки.", "empty panel"));
  }
}

function renderReviewSessions() {
  const layout = elements.reviewSessions.closest(".review-layout");
  if (layout) layout.classList.toggle("empty", !state.reviewSessions.length);
  if (!state.reviewSessions.length) { elements.reviewSessions.replaceChildren(text("p", "Проверок нет.", "empty")); return; }
  if (!state.reviewSelected || !state.reviewSessions.some((item) => item.id === state.reviewSelected.id)) {
    selectReviewSession(state.reviewSessions[0]);
    return;
  }
  elements.reviewSessions.replaceChildren(...state.reviewSessions.map((session) => {
    const button = document.createElement("button"); button.type = "button";
    button.className = `review-session${state.reviewSelected?.id === session.id ? " active" : ""}`;
    const vacancy = session.vacancy || {};
    button.append(text("strong", vacancy.title || session.question || `Проверка ${compactID(session.id)}`));
    const details = vacancy.title
      ? [vacancy.employer || "Компания не определена", session.question, reviewStatusLabel(session.status), formatDate(session.updated_at)]
      : [reviewStatusLabel(session.status), session.platform, session.profile_id, formatDate(session.updated_at)];
    button.append(text("small", details.filter(Boolean).join(" · ")));
    button.addEventListener("click", () => selectReviewSession(session));
    return button;
  }));
}

async function selectReviewSession(session) {
  state.reviewSelected = session; state.reviewMessage = ""; renderReviewSessions();
  const vacancy = session.vacancy || {};
  elements.reviewSessionTitle.textContent = vacancy.title || `Проверка ${compactID(session.id)}`;
  const meta = vacancy.title
    ? [vacancy.employer || "Компания не определена", reviewStatusLabel(session.status), `профиль ${session.profile_id}`]
    : [reviewStatusLabel(session.status), session.platform, `профиль ${session.profile_id}`, `revision ${session.revision}`];
  elements.reviewSessionMeta.textContent = meta.join(" · ");
  if (vacancy.url) {
    const link = document.createElement("a"); link.href = safeExternalURL(vacancy.url) || vacancy.url; link.target = "_blank"; link.rel = "noopener noreferrer";
    link.textContent = " Открыть вакансию ↗"; link.className = "table-link";
    elements.reviewSessionMeta.append(link);
  }
  elements.reviewPrompt.replaceChildren(text("p", "Загрузка…", "empty"));
  try {
    state.reviewDetail = await request(`/api/v1/review-sessions/${encodeURIComponent(session.id)}`);
    renderReviewPrompt();
  } catch (error) {
    elements.reviewPrompt.replaceChildren(text("p", error.message, "empty"));
  }
}

function renderReviewPrompt() {
  const detail = state.reviewDetail;
  if (detail?.questions?.length && detail.status === "waiting_answer") {
    renderReviewBatch(detail);
    return;
  }
  if (!detail?.prompt) {
    elements.reviewPrompt.replaceChildren(text("p", "Для этой сессии нет ожидающего вопроса.", "empty"));
    return;
  }
  const prompt = detail.prompt;
  const kind = prompt.question.kind;
  if (kind !== "single" && kind !== "multiple" && kind !== "text") {
    elements.reviewPrompt.replaceChildren(text("p", `Тип вопроса «${kind}» пока не поддерживается интерактивно.`, "empty"));
    return;
  }
  const form = document.createElement("form"); form.className = "review-form";
  form.append(text("p", prompt.question.text, "review-question"));
  let input;
  if (kind === "text") {
    input = document.createElement("textarea"); input.rows = 5; input.placeholder = "Ответ"; input.required = true;
  } else {
    input = document.createElement("div"); input.className = "review-options";
    for (const option of prompt.question.options || []) {
      const label = document.createElement("label"); label.className = "review-option";
      const control = document.createElement("input");
      control.type = kind === "single" ? "radio" : "checkbox";
      control.name = "review-option"; control.value = option.text;
      label.append(control, text("span", option.text));
      input.append(label);
    }
  }
  form.append(input);
  const footer = document.createElement("div"); footer.className = "review-actions";
  const submit = text("button", "Записать ответ"); submit.type = "submit"; submit.disabled = state.reviewBusy;
  footer.append(text("span", state.reviewMessage, "muted"), submit); form.append(footer);
  form.addEventListener("submit", (event) => { event.preventDefault(); submitReviewAnswer(prompt, form, input, kind); });
  elements.reviewPrompt.replaceChildren(form);
}

function reviewControlName(questionID) { return `review-answer-${questionID}`; }

function renderReviewBatch(detail) {
  const form = document.createElement("form"); form.className = "review-form";
  const fields = [];
  let unsupported = false;
  for (const question of detail.questions) {
    const block = document.createElement("div"); block.className = "review-question-block";
    block.append(text("p", question.text, "review-question"));
    let input;
    if (question.kind === "text") {
      input = document.createElement("textarea"); input.rows = 4; input.placeholder = "Ответ"; input.required = true;
    } else if (question.kind === "single" || question.kind === "multiple") {
      input = document.createElement("div"); input.className = "review-options";
      for (const option of question.options || []) {
        const label = document.createElement("label"); label.className = "review-option";
        const control = document.createElement("input");
        control.type = question.kind === "single" ? "radio" : "checkbox";
        control.name = reviewControlName(question.id); control.value = option.text;
        label.append(control, text("span", option.text));
        input.append(label);
      }
    } else {
      unsupported = true;
      input = text("p", `Тип вопроса «${question.kind}» пока не поддерживается интерактивно.`, "empty");
    }
    block.append(input); form.append(block);
    fields.push({ question, input });
  }
  const footer = document.createElement("div"); footer.className = "review-actions";
  const submit = text("button", "Сохранить все ответы"); submit.type = "submit";
  submit.disabled = state.reviewBusy || unsupported;
  footer.append(text("span", state.reviewMessage, "muted"), submit); form.append(footer);
  form.addEventListener("submit", (event) => { event.preventDefault(); submitReviewBatch(detail, fields); });
  elements.reviewPrompt.replaceChildren(form);
}

async function submitReviewBatch(detail, fields) {
  if (state.reviewBusy) return;
  const answers = [];
  for (const { question, input } of fields) {
    if (question.kind === "text") {
      const value = input.value.trim();
      if (!value) { state.reviewMessage = `Заполните: ${question.text}`; renderReviewPrompt(); return; }
      answers.push({ question_id: question.id, text: value });
      continue;
    }
    if (question.kind === "single" || question.kind === "multiple") {
      const selected = [...input.querySelectorAll("input:checked")].map((control) => control.value);
      if (question.kind === "single" && selected.length !== 1) { state.reviewMessage = `Выберите один вариант: ${question.text}`; renderReviewPrompt(); return; }
      if (question.kind === "multiple" && selected.length === 0) { state.reviewMessage = `Выберите хотя бы один вариант: ${question.text}`; renderReviewPrompt(); return; }
      answers.push({ question_id: question.id, selected_options: selected });
      continue;
    }
    state.reviewMessage = `Вопрос «${question.text}» не поддерживается`; renderReviewPrompt(); return;
  }
  state.reviewBusy = true; state.reviewMessage = "Отправляю…"; renderReviewPrompt();
  try {
    await enqueue(`/api/v1/review-sessions/${encodeURIComponent(detail.id)}/answers`, {
      expected_revision: detail.revision, source: "dashboard", answers,
    });
    state.reviewMessage = "Ответы записаны, задача на отправку поставлена в очередь";
    await refreshSummary();
    globalThis.setTimeout(async () => {
      await refreshReviewSessions();
      if (state.reviewSelected) await selectReviewSession(state.reviewSelected);
    }, 1500);
  } catch (error) {
    state.reviewMessage = error.message;
  }
  state.reviewBusy = false; renderReviewPrompt();
}

async function submitReviewAnswer(prompt, form, input, kind) {
  if (state.reviewBusy) return;
  const selected = [];
  let answerText = "";
  if (kind === "text") {
    answerText = input.value.trim();
    if (!answerText) { state.reviewMessage = "Введите ответ"; renderReviewPrompt(); return; }
  } else {
    for (const control of form.querySelectorAll('input[name="review-option"]:checked')) selected.push(control.value);
    if (kind === "single" && selected.length !== 1) { state.reviewMessage = "Выберите один вариант"; renderReviewPrompt(); return; }
    if (kind === "multiple" && selected.length === 0) { state.reviewMessage = "Выберите хотя бы один вариант"; renderReviewPrompt(); return; }
  }
  state.reviewBusy = true; state.reviewMessage = "Отправляю…"; renderReviewPrompt();
  try {
    await enqueue(`/api/v1/review-sessions/${encodeURIComponent(state.reviewDetail.id)}/answers`, {
      prompt_id: prompt.id, expected_revision: prompt.revision,
      selected_options: selected, text: answerText, source: "dashboard",
    });
    state.reviewMessage = "Ответ записан, задача поставлена в очередь";
    await refreshSummary();
    globalThis.setTimeout(async () => {
      await refreshReviewSessions();
      if (state.reviewSelected) await selectReviewSession(state.reviewSelected);
    }, 1500);
  } catch (error) {
    state.reviewMessage = error.message;
  }
  state.reviewBusy = false; renderReviewPrompt();
}

async function request(path, options = {}) { const response = await fetch(path, { cache: "no-store", ...options }); let body = {}; try { body = await response.json(); } catch (_) {} if (!response.ok) throw new Error(body.error || `HTTP ${response.status}`); return body; }

async function controlFailedTask(task, action) {
  if (action === "retry" && !globalThis.confirm(`Повторить «${taskTypeLabel(task.type)}» (${task.id})? Действие может обратиться к внешней платформе.`)) return;
  state.taskBusy.add(task.id); renderFailedTasks(state.failedTasks || []);
  try {
    await request(`/api/v1/tasks/${encodeURIComponent(task.id)}/${action}`, { method: "POST" });
    await refreshSummary();
  } catch (error) {
    elements.connectionState.textContent = error.message;
  } finally {
    state.taskBusy.delete(task.id); renderFailedTasks(state.failedTasks || []);
  }
}

async function runJob(job) {
  state.jobBusy.add(job.tag); renderJobs(state.jobs);
  try {
    const result = await enqueue(`/api/v1/jobs/${encodeURIComponent(job.tag)}/runs`);
    elements.connectionState.textContent = `Job ${job.tag}: задача ${result.task_id} поставлена в очередь`;
    await refreshSummary();
  } catch (error) {
    elements.connectionState.textContent = error.message;
  } finally {
    state.jobBusy.delete(job.tag); renderJobs(state.jobs);
  }
}

async function refreshSummary() {
  elements.refresh.disabled = true; elements.connectionState.textContent = "Обновление…"; elements.connectionDot.className = "dot pending";
  try {
    const [summary, failures, jobs] = await Promise.all([request("/api/v1/dashboard/summary"), request("/api/v1/tasks/failed"), request("/api/v1/jobs"), refreshApplications()]);
    state.summary = summary; state.failedTasks = failures.items || []; state.jobs = jobs.items || [];
    if (state.selectedConversation) state.selectedConversation = (summary.conversations || []).find((item) => item.id === state.selectedConversation.id) || null;
    renderAccountSwitcher(summary.profiles || []);
    renderStats(summary); renderApplicationFilters(state.applicationObjects); renderApplicationObjects(); renderTasks(summary.tasks || []); renderJobs(state.jobs); renderCampaigns(summary.campaigns || []); renderFailedTasks(state.failedTasks); renderActivity(summary.activity || []); renderActivityObservations(summary.activity_snapshots || []); renderConversations(summary.conversations || []);
    elements.updatedAt.textContent = `Обновлено ${formatDate(summary.generated_at)}`; elements.connectionState.textContent = "Backend доступен"; elements.connectionDot.className = "dot ok";
  } catch (error) { elements.connectionState.textContent = error.message; elements.connectionDot.className = "dot error"; }
  finally { elements.refresh.disabled = false; }
}
async function refreshVersion() {
  try {
    const info = await request("/api/v1/version");
    elements.runtimeVersion.textContent = `v${info.version} · API ${info.api_version}`;
    elements.runtimeVersion.title = `commit ${info.commit} · build ${info.build_time} · modified ${info.modified}`;
  } catch (error) { elements.runtimeVersion.textContent = "версия недоступна"; }
}
async function selectConversation(conversation) {
  state.selectedConversation = conversation; renderConversations(state.summary?.conversations || []); elements.chatTitle.textContent = conversationLabel(conversation); elements.chatMeta.textContent = `${conversation.employer || "Компания не определена"} · профиль ${conversation.profile_id} · ${conversationStatusLabels[conversation.status] || conversation.status}`;
  const vacancyURL = safeExternalURL(conversation.vacancy_url); elements.chatVacancyLink.classList.toggle("hidden", !vacancyURL); if (vacancyURL) elements.chatVacancyLink.href = vacancyURL; else elements.chatVacancyLink.removeAttribute("href");
  elements.reply.disabled = false; elements.send.disabled = false; elements.messages.replaceChildren(text("p", "Загрузка…", "empty"));
  try {
    const result = await request(`/api/v1/conversations/${encodeURIComponent(conversation.id)}/messages`);
    if (state.selectedConversation?.id !== conversation.id) return;
    state.selectedMessages = result.items || []; renderMessages(state.selectedMessages);
  } catch (error) { elements.messages.replaceChildren(text("p", error.message, "empty")); }
  if (conversation.unread_count && !state.conversationReadBusy.has(conversation.id)) {
    state.conversationReadBusy.add(conversation.id); elements.actionState.textContent = "Помечаю открытый диалог прочитанным…";
    try {
      const key = `dashboard-open:${conversation.id}:${conversation.revision}`;
      await enqueue(`/api/v1/conversations/${encodeURIComponent(conversation.id)}/mark-read`, undefined, key);
      elements.actionState.textContent = "Диалог будет помечен прочитанным";
      await refreshSummary();
    } catch (error) { elements.actionState.textContent = error.message; }
    finally { state.conversationReadBusy.delete(conversation.id); }
  }
}
function newIdempotencyKey() {
  if (globalThis.crypto?.randomUUID) return globalThis.crypto.randomUUID();
  const suffix = Math.random().toString(36).slice(2);
  return `dashboard-${Date.now()}-${suffix}`;
}
async function enqueue(path, body, idempotencyKey = newIdempotencyKey()) { const headers = { "Idempotency-Key": idempotencyKey }; if (body !== undefined) headers["Content-Type"] = "application/json"; return request(path, { method: "POST", headers, body: body === undefined ? undefined : JSON.stringify(body) }); }

elements.replyForm.addEventListener("submit", async (event) => {
  event.preventDefault(); const value = elements.reply.value.trim(); if (!state.selectedConversation || !value) return; const conversationID = state.selectedConversation.id; elements.send.disabled = true; elements.actionState.textContent = "Создаю задачу…";
  try {
    const result = await enqueue(`/api/v1/conversations/${encodeURIComponent(conversationID)}/messages`, { content: { text: value } });
    elements.reply.value = ""; elements.actionState.textContent = `Задача ${result.task_id} поставлена в очередь`;
    if (state.selectedConversation?.id === conversationID) {
      state.selectedMessages = [...state.selectedMessages, { id: `queued:${result.task_id}`, direction: "outgoing", kind: "text", status: "queued", text: value, occurred_at: new Date().toISOString() }];
      renderMessages(state.selectedMessages);
    }
    await refreshSummary();
  } catch (error) { elements.actionState.textContent = error.message; } finally { elements.send.disabled = false; }
});
elements.markAllRead.addEventListener("click", async () => {
  state.markAllReadBusy = true; updateMarkAllRead(state.summary?.conversations || []); elements.conversationBulkState.textContent = "Ставлю задачи в очередь…";
  try {
    const result = await enqueue("/api/v1/conversations/mark-read");
    elements.conversationBulkState.textContent = result.created ? `Непрочитанных диалогов: ${result.created}` : "Новых задач не потребовалось";
    await refreshSummary();
  } catch (error) { elements.conversationBulkState.textContent = error.message; }
  finally { state.markAllReadBusy = false; updateMarkAllRead(state.summary?.conversations || []); }
});
let applicationSearchTimer;
elements.applicationSearch.addEventListener("input", () => { clearTimeout(applicationSearchTimer); state.applicationQuery = elements.applicationSearch.value; state.applicationRequest++; state.applicationLoading = true; updateApplicationSelection(); applicationSearchTimer = setTimeout(changeApplicationQuery, 250); });
elements.applicationSort.addEventListener("change", () => { state.applicationSort = elements.applicationSort.value; changeApplicationQuery(); });
elements.applicationSelectAll.addEventListener("change", () => { for (const item of visibleApplicationObjects().filter(applicationCanRemove)) { if (elements.applicationSelectAll.checked) state.selectedApplications.add(item.id); else state.selectedApplications.delete(item.id); } renderApplicationObjects(); });
elements.applicationBulkAction.addEventListener("change", () => updateApplicationSelection());
elements.applicationRunAction.addEventListener("click", async () => {
  const ids = [...state.selectedApplications];
  if (!ids.length || elements.applicationBulkAction.value !== "remove" || state.applicationActionBusy) return;
  state.applicationActionBusy = true; updateApplicationSelection();
  elements.applicationSelectionState.textContent = `Создаю задачи: ${ids.length}…`;
  try {
    const result = await enqueue("/api/v1/applications/remove", { application_ids: ids });
    state.selectedApplications.clear(); elements.applicationBulkAction.value = "";
    const failures = (result.results || []).filter((item) => item.error);
    state.applicationActionMessage = `Создано задач: ${result.created}. Уже в очереди: ${(result.tasks || []).length - result.created}.${failures.length ? ` Не поставлены: ${failures.length} — объекты недоступны или ещё обрабатываются.` : ""}`;
    await refreshSummary();
  } catch (error) { state.applicationActionMessage = error.message; }
  finally { state.applicationActionBusy = false; updateApplicationSelection(); }
});
elements.applicationReset.addEventListener("click", () => { state.applicationFilter = ""; state.applicationQuery = ""; state.applicationSort = "updated_desc"; state.selectedApplications.clear(); state.applicationActionMessage = ""; elements.applicationSearch.value = ""; elements.applicationSort.value = state.applicationSort; elements.applicationBulkAction.value = ""; changeApplicationQuery(); });
elements.applicationPrev.addEventListener("click", () => { state.applicationOffset = Math.max(0, state.applicationOffset - 200); state.selectedApplications.clear(); refreshApplications(); });
elements.applicationNext.addEventListener("click", () => { state.applicationOffset += 200; state.selectedApplications.clear(); refreshApplications(); });
elements.conversationSearch.addEventListener("input", () => { state.conversationQuery = elements.conversationSearch.value; renderConversations(state.summary?.conversations || []); });
elements.conversationFilter.addEventListener("change", () => { state.conversationFilter = elements.conversationFilter.value; renderConversations(state.summary?.conversations || []); });
elements.conversationSort.addEventListener("change", () => { state.conversationSort = elements.conversationSort.value; renderConversations(state.summary?.conversations || []); });
elements.refresh.addEventListener("click", () => { refreshSummary(); refreshProfileResources(); refreshReviewSessions(); });
elements.reviewRefresh.addEventListener("click", () => refreshReviewSessions());
elements.accountSwitcher.addEventListener("change", () => {
  state.account = elements.accountSwitcher.value;
  try { window.localStorage.setItem("job-agent-account", state.account); } catch {}
  state.selectedApplications.clear(); state.applicationOffset = 0; state.applicationTotal = 0;
  state.reviewSelected = null; state.reviewDetail = null;
  refreshSummary(); refreshReviewSessions();
});
elements.reviewFilter.addEventListener("change", () => { state.reviewSelected = null; refreshReviewSessions(); });
refreshVersion(); refreshSummary(); refreshProfileResources(); refreshReviewSessions(); setInterval(() => { refreshSummary(); refreshProfileResources(); refreshReviewSessions(); }, 30_000);

(() => {
  const startButton = document.getElementById("auth-start");
  if (!startButton) {
    return;
  }
  const profileInput = document.getElementById("auth-profile");
  const valueInput = document.getElementById("auth-value");
  const submitButton = document.getElementById("auth-submit");
  const cancelButton = document.getElementById("auth-cancel");
  const state = document.getElementById("auth-state");
  const captcha = document.getElementById("auth-captcha");
  const kindByStatus = {
    waiting_identifier: "identifier",
    waiting_otp: "otp",
    waiting_password: "password",
    waiting_captcha: "captcha",
  };
  const terminal = ["completed", "expired", "cancelled", "failed"];
  let sessionId = "";
  let stream = null;

  const setState = (text) => { state.textContent = text; };
  const valueRow = document.getElementById("auth-value-row");

  const renderSession = (session) => {
    sessionId = session.id;
    let summary = `сессия ${session.id}: ${session.status} (rev ${session.revision})`;
    if (session.failure_message) {
      summary += ` — ${session.failure_message}`;
    }
    setState(summary);
    if (session.status === "waiting_captcha") {
      captcha.hidden = false;
      captcha.src = `/api/v1/auth/sessions/${encodeURIComponent(session.id)}/challenge?ts=${Date.now()}`;
    } else {
      captcha.hidden = true;
      captcha.removeAttribute("src");
    }
    const active = kindByStatus[session.status] !== undefined;
    const finished = terminal.includes(session.status);
    valueRow.hidden = !active;
    submitButton.disabled = !active;
    valueInput.disabled = !active;
    cancelButton.hidden = finished || !active && !sessionId;
    profileInput.disabled = !finished && Boolean(sessionId);
    if (finished) sessionId = "";
  };

  const subscribe = (id) => {
    if (stream) {
      stream.close();
    }
    stream = new EventSource(`/api/v1/auth/sessions/${encodeURIComponent(id)}/events`);
    stream.addEventListener("session", (message) => {
      const session = JSON.parse(message.data);
      renderSession(session);
      if (terminal.includes(session.status)) {
        stream.close();
        stream = null;
      }
    });
    stream.addEventListener("error", () => setState("поток прерван, обновите статус"));
  };

  startButton.addEventListener("click", async () => {
    const profile = profileInput.value.trim();
    if (!profile) {
      setState("укажите профиль");
      return;
    }
    setState("создание сессии…");
    const response = await fetch("/api/v1/auth/sessions", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ platform: "hh", profile_id: profile }),
    });
    if (!response.ok) {
      setState(`ошибка создания сессии: ${response.status}`);
      return;
    }
    const session = await response.json();
    renderSession(session);
    subscribe(session.id);
  });

  submitButton.addEventListener("click", async () => {
    if (!sessionId) {
      setState("сначала начните вход");
      return;
    }
    const value = valueInput.value.trim();
    if (!value) {
      return;
    }
    const response = await fetch(`/api/v1/auth/sessions/${encodeURIComponent(sessionId)}`);
    if (!response.ok) {
      setState(`ошибка статуса: ${response.status}`);
      return;
    }
    const current = await response.json();
    const kind = kindByStatus[current.status];
    if (!kind) {
      setState(`сессия не ждёт ввода: ${current.status}`);
      return;
    }
    const submitted = await fetch(`/api/v1/auth/sessions/${encodeURIComponent(sessionId)}/inputs`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ kind, value }),
    });
    if (!submitted.ok) {
      setState(`ошибка ввода: ${submitted.status}`);
      return;
    }
    valueInput.value = "";
    renderSession(await submitted.json());
    if (!stream) {
      subscribe(sessionId);
    }
  });

  cancelButton.addEventListener("click", async () => {
    if (!sessionId) {
      return;
    }
    const response = await fetch(`/api/v1/auth/sessions/${encodeURIComponent(sessionId)}/cancel`, { method: "POST" });
    if (!response.ok) {
      setState(`ошибка отмены: ${response.status}`);
      return;
    }
    renderSession(await response.json());
  });
})();
