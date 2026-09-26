function storedAccount() { try { return window.localStorage.getItem("job-agent-account") || ""; } catch { return ""; } }
const state = {
  account: storedAccount(),
  applicationOffset: 0, applicationTotal: 0, applicationGroups: {}, applicationRequest: 0, applicationLoading: false,
  summary: null, selectedConversation: null, selectedMessages: [], jobs: [], applicationObjects: [],
  applicationFilter: "", applicationQuery: "", applicationSort: "updated_desc", selectedApplications: new Set(), applicationActionBusy: false, applicationActionMessage: "",
  conversationQuery: "", conversationFilter: "", conversationSort: "updated_desc", conversationReadBusy: new Set(), markAllReadBusy: false, conversationAnswerBusy: "",
  conversationItems: [], conversationTotal: 0, conversationUnreadTotal: 0, conversationLoading: false, conversationSearchTimer: 0, conversationPinnedIndex: 0,
  reviewSendProfiles: new Set(),
  cache: { summary: null, applications: null, conversations: new Map(), reviews: null },
  localReads: new Map(),
  profileResources: [], profilePlans: new Map(), profileEditors: new Map(), profileRevisions: new Map(), profileMessages: new Map(), profileBusy: new Set(), taskBusy: new Set(), jobBusy: new Set(),
  reviewSessions: [], reviewSelected: null, reviewDetail: null, reviewBusy: false, reviewMessage: "",
  reviewQuery: "", reviewHasMore: false,
  browserCheck: null, captchaCheckRemaining: [],
};
const elements = Object.fromEntries([
  "application-filters", "application-items", "application-filter-state", "application-search", "application-sort", "application-reset", "application-select-all", "application-selection-state", "application-bulk-action", "application-run-action", "tasks", "jobs", "campaigns", "failed-tasks", "activity", "activity-observations", "stats", "conversations", "conversation-search", "conversation-filter", "conversation-sort", "messages", "chat-title", "chat-meta", "chat-vacancy-link",
  "connection-dot", "connection-state", "runtime-version", "updated-at", "refresh", "mark-all-read", "conversation-bulk-state", "reply-form", "account-switcher",
  "reply", "send", "action-state",
  "profile-resources", "profile-state-state",
  "review-state", "review-filter", "review-search", "review-more", "review-refresh", "review-sessions", "review-session-title", "review-session-meta", "review-prompt", "review-send",
  "browser-check", "browser-check-state", "browser-check-image", "browser-check-answer", "browser-check-submit", "browser-check-refresh-image", "browser-check-cancel",
  "conversation-page-state", "account-captcha", "account-captcha-button", "account-captcha-label", "browser-check-controls",
].map((id) => [id.replace(/-([a-z])/g, (_, letter) => letter.toUpperCase()), document.querySelector(`#${id}`)]));
const taskTypeLabels = {
  "vacancy.search_page": "Получить страницу вакансий", "application.campaign": "Запустить рассылку откликов", "application.submit": "Отправить отклик", "application.remove": "Убрать отклик", "application.retention": "Очистка устаревших и отказов",
  "questionnaire.answer": "Ответить на анкету", "test.complete": "Пройти тест", "test.capture": "Сохранить вопросы теста", "review.answer": "Сохранить проверенный ответ",
  "conversation.reply": "Ответить в чате", "conversation.send": "Отправить сообщение", "conversation.follow_up": "Отправить напоминание", "conversation.follow_up.select": "Отправить напоминания в чатах",
  "conversation.discover": "Обновить список чатов", "conversation.mark_read": "Пометить чат прочитанным", "conversation.sync": "Загрузить сообщения чата", "vacancy.inspect": "Открыть и изучить вакансию",
  "resume.publish": "Опубликовать резюме", "resume.touch": "Поднять резюме", "resume.update": "Обновить резюме", "profile.activity.observe": "Обновить метрики резюме", "profile.session_refresh": "Обновить сессию профиля", "application.state.sync": "Синхронизировать состояния откликов",
  "profile.activity.maintain": "Просмотр вакансий для активности", "application.answer_covered": "Ответить на анкеты по банку ответов", "profile.bootstrap": "Заполнить профиль", "profile_state.reconcile": "Сверить профиль с конфигурацией", "profile_state.apply": "Применить изменения профиля", "skill_verification.start": "Запустить проверку навыка",
  "calendar.find_slots": "Найти свободное время", "calendar.create_event": "Создать событие", "challenge.respond": "Ответить на проверку", "notification.deliver": "Доставить уведомление",
};
const applicationGroupLabels = {
  queued: "В очереди", sent: "Отправлено", needs_input: "Нужно участие", waiting_invitation: "Ожидает приглашения",
  invited: "Приглашение", rejected: "Отказ", state_unknown: "Состояние не синхронизировано", hidden: "Скрыт", not_sent: "Не отправлено",
  imported: "Импортировано (appltool)",
};
const queuedApplicationStatuses = new Set(["new", "preparing", "ready", "submitting", "pending_reconciliation"]);
const inputDecisionCodes = new Set(["questionnaire_required", "vacancy_test_required", "platform_validation_required", "unsupported_response_flow", "captcha_required"]);
const taskStatusLabels = { new: "Ожидает", processing: "Выполняется", waiting_confirmation: "Нужно решение", retry_scheduled: "Повтор запланирован", completed: "Завершена", failed: "Ошибка", dismissed: "Закрыта" };
const campaignStatusLabels = { running: "Выполняется", target_reached: "Цель достигнута", exhausted: "Вакансии закончились", paused_budget: "Пауза: лимит", paused_rate_limit: "Пауза: rate limit", failed: "Ошибка" };
const conversationStatusLabels = { active: "Активный", closed: "Закрыт", rejected: "Отказ", archived: "Архив" };
const reviewStatusLabels = { pending: "Подготовка", waiting_answer: "Ждёт ответа", answer_recorded: "Ответ записан", completed: "Завершена", cancelled: "Отменена", unsupported: "Не поддерживается", expired: "Истекла" };
const activityKindLabels = { "vacancy.inspected": "Просмотрена вакансия", "application.submitted": "Отправлен отклик", "conversation.message_sent": "Отправлено сообщение", "resume.touched": "Поднято резюме" };
const decisionLabels = { qualified: "Проверки пройдены", response_impossible: "HH сейчас не разрешает отклик по этой вакансии", resume_not_suitable: "HH не предлагает доступного резюме", questionnaire_required: "Нужно заполнить анкету", vacancy_test_required: "Нужно пройти тест", platform_validation_required: "Платформа запросила дополнительные данные", captcha_required: "HH просит пройти капчу", unsupported_response_flow: "Платформа вернула неподдерживаемый сценарий отклика", cover_letter_required: "Не удалось подготовить обязательное сопроводительное", vacancy_closed: "Вакансия закрыта", already_applied: "Отклик уже существует" };
const failureLabels = { temporary_failure: "Временная ошибка — будет повтор", rate_limited: "Платформа ограничила частоту запросов", quota_exceeded: "Исчерпан дневной лимит", unauthorized: "Нужно обновить авторизацию", validation_required: "Платформа запросила дополнительные данные", permanent_failure: "Платформа отклонила операцию", ambiguous_result: "Результат отправки нужно сверить" };

function text(tag, value, className = "") { const node = document.createElement(tag); node.textContent = value; if (className) node.className = className; return node; }
function plainText(value) { return String(value ?? "").replace(/<[^>]*>/g, " ").replace(/&nbsp;/g, " ").replace(/\s+/g, " ").trim(); }
function statusCell(value, className = "") { const cell = document.createElement("td"); cell.append(text("span", value, `status ${className}`.trim())); return cell; }
function formatDate(value) { return value ? new Intl.DateTimeFormat("ru-RU", { dateStyle: "short", timeStyle: "medium" }).format(new Date(value)) : "—"; }
function taskTypeLabel(value) { return taskTypeLabels[value] || value; }
function taskStatusLabel(value) { return taskStatusLabels[value] || value || "—"; }
function conversationLabel(item) { return item.vacancy_title || `Диалог ${item.platform}`; }
function total(items, predicate = () => true) { return items.filter(predicate).reduce((sum, item) => sum + Number(item.count || 0), 0); }
function safeExternalURL(value) { try { const url = new URL(value); return ["http:", "https:"].includes(url.protocol) ? url.href : ""; } catch (_) { return ""; } }
function compactID(value) { const id = String(value || ""); return id.length > 20 ? `${id.slice(0, 8)}…${id.slice(-6)}` : id || "—"; }
function applicationGroup(item) {
  if (item.decision_code === "imported_appltool") return "imported";
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
  if (inputDecisionCodes.has(item.decision_code) && (item.status === "waiting_validation" || item.status === "skipped")) return "needs_input";
  if (queuedApplicationStatuses.has(item.status)) return "queued";
  return "not_sent";
}
function applicationIsSent(item) { return ["waiting_invitation", "invited", "rejected", "state_unknown", "hidden"].includes(applicationGroup(item)); }
function applicationReason(item) {
  if (item.disposition === "invited") return "HH перевёл отклик в «Приглашение» — автоматическая очистка запрещена";
  if (item.disposition === "rejected") return "HH подтвердил отказ — объект подходит для автоматической очистки";
  if (item.disposition === "pending") return item.viewed_by_opponent === true ? "Работодатель посмотрел отклик, но приглашения нет" : "Приглашения ещё нет";
  if ((item.status === "submitted" || item.decision_code === "already_applied") && !item.disposition) return "Состояние отклика ещё не синхронизировано с HH";
  if (item.decision_code && item.decision_code !== "qualified") return item.decision_reason || decisionLabels[item.decision_code] || item.decision_code;
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
    { value: total(applications, (item) => (item.status === "submitted" || item.decision_code === "already_applied") && item.decision_code !== "imported_appltool"), label: "Отклики отправлены", filter: "sent" },
    { value: total(applications, (item) => applicationGroup(item) === "queued"), label: "Ожидают отправки", filter: "queued" },
    { value: total(applications, (item) => applicationGroup(item) === "needs_input"), label: "Нужно участие", filter: "needs_input" },
    { value: summary.conversation_stats?.active || 0, label: "Активные диалоги", target: "conversations-title" },
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

async function captureQuestionnaire(item, button) {
  button.disabled = true;
  state.applicationActionMessage = "Запрашиваю анкету у HH…";
  updateApplicationSelection(state.applicationObjects);
  try {
    const key = globalThis.crypto?.randomUUID ? globalThis.crypto.randomUUID() : `questionnaire-${Date.now()}`;
    const response = await fetch(`/api/v1/applications/${encodeURIComponent(item.id)}/questionnaire`, {
      method: "POST", headers: { "Idempotency-Key": key },
    });
    if (!response.ok) throw new Error(String(response.status));
    elements.reviewState.textContent = "Анкета захвачена — ответьте ниже и отправьте.";
    refreshReviewSessions();
    document.getElementById("review-title")?.scrollIntoView({ behavior: "smooth", block: "start" });
  } catch (error) {
    state.applicationActionMessage = `Не удалось запросить анкету: ${error.message}`;
  }
  button.disabled = false;
  updateApplicationSelection(state.applicationObjects);
}
function browserCheckImageURL(session) {
  return `/api/v1/applications/${encodeURIComponent(session.application_id)}/browser-check/${encodeURIComponent(session.session_id)}/image?ts=${Date.now()}`;
}
const browserCheckStateLabels = { waiting_captcha: "HH просит ввести символы с картинки", done: "Отклик отправлен из браузера", review: "Нужен ручной просмотр страницы" };
function renderBrowserCheck() {
  const session = state.browserCheck;
  if (!session) { elements.browserCheck.hidden = true; return; }
  elements.browserCheck.hidden = false;
  elements.browserCheckState.textContent = `${browserCheckStateLabels[session.state] || session.state}${session.message ? ` · ${session.message}` : ""}`;
  const waiting = session.state === "waiting_captcha";
  elements.browserCheckControls.hidden = !waiting;
  elements.browserCheckCancel.textContent = waiting ? "Отмена" : "Закрыть";
  elements.browserCheckAnswer.disabled = !waiting;
  elements.browserCheckSubmit.disabled = !waiting;
  elements.browserCheckRefreshImage.hidden = !waiting || !session.has_image;
  if (session.has_image) {
    elements.browserCheckImage.hidden = false;
    elements.browserCheckImage.src = browserCheckImageURL(session);
  } else {
    elements.browserCheckImage.hidden = true;
    elements.browserCheckImage.removeAttribute("src");
  }
}
// captchaWaitingByProfile counts applications that need a human captcha per
// account. The summary carries counts, so the header can warn without loading
// the whole blocked list.
function captchaWaitingByProfile() {
  const counts = new Map();
  for (const row of state.summary?.applications || []) {
    if (row.decision_code !== "captcha_required") continue;
    const profile = row.profile_id || "";
    counts.set(profile, (counts.get(profile) || 0) + Number(row.count || 0));
  }
  return counts;
}
function updateCaptchaWarning() {
  const counts = captchaWaitingByProfile();
  let target = state.account;
  if (!counts.get(target)) target = "";
  if (!target && counts.size) {
    target = [...counts.keys()].sort((left, right) => counts.get(right) - counts.get(left))[0];
  }
  const count = target ? counts.get(target) || 0 : 0;
  elements.accountCaptcha.hidden = count === 0;
  if (count === 0) return;
  elements.accountCaptcha.dataset.profile = target;
  elements.accountCaptchaLabel.textContent = `Нужно ввести капчу: ${profileDisplayName(target)}${count > 1 ? ` (${count})` : ""}`;
  elements.accountCaptchaButton.textContent = "Ввести капчу";
}
// startCaptchaCheck runs one browser check for the profile and then retries
// the remaining parked applications: the platform guard is per account, so the
// operator enters the captcha once instead of per application.
async function startCaptchaCheck(profileID = "") {
  state.applicationActionBusy = true;
  elements.accountCaptchaButton.disabled = true; elements.accountCaptchaButton.textContent = "Проверяю…";
  state.applicationActionMessage = "Готовлю проверку HH…"; renderApplicationObjects();
  try {
    const params = new URLSearchParams({ status: "waiting_validation", decision_code: "captcha_required", limit: "200" });
    if (profileID) params.set("profile_id", profileID);
    const listing = await request(`/api/v1/applications?${params}`);
    const parked = (listing.items || []).filter((item) => item.decision_code === "captcha_required");
    if (!parked.length) { state.applicationActionMessage = "Нет откликов, ожидающих проверку HH"; return; }
    state.captchaCheckRemaining = parked.slice(1).map((item) => item.id);
    const session = await request(`/api/v1/applications/${encodeURIComponent(parked[0].id)}/browser-check`, { method: "POST" });
    state.browserCheck = session;
    state.applicationActionMessage = `Проверка HH: ${browserCheckStateLabels[session.state] || session.state}`;
    renderBrowserCheck();
    elements.browserCheck.scrollIntoView({ behavior: "smooth", block: "center" });
    if (session.state === "done") { await retryRemainingAfterCheck(); refreshApplications(); refreshSummary(); }
  } catch (error) {
    state.applicationActionMessage = `Проверка не запустилась: ${error.message}`;
  } finally {
    state.applicationActionBusy = false; updateCaptchaWarning(); renderApplicationObjects();
  }
}
// retryRemainingAfterCheck releases the parked applications behind one manual
// check; the durable retry pipeline confirms each platform result.
async function retryRemainingAfterCheck() {
  const ids = (state.captchaCheckRemaining || []).slice(0, 200);
  state.captchaCheckRemaining = [];
  if (!ids.length) return;
  try {
    const result = await enqueue("/api/v1/applications/retry", { application_ids: ids });
    state.applicationActionMessage = `${state.applicationActionMessage} · повторно поставлено: ${result.created || 0}`;
  } catch (error) {
    state.applicationActionMessage = `${state.applicationActionMessage} · не удалось повторить остальные: ${error.message}`;
  }
}

async function submitBrowserCheckAnswer() {
  const session = state.browserCheck;
  if (!session) return;
  const value = elements.browserCheckAnswer.value.trim();
  if (!value) return;
  elements.browserCheckSubmit.disabled = true;
  try {
    const response = await fetch(`/api/v1/applications/${encodeURIComponent(session.application_id)}/browser-check/${encodeURIComponent(session.session_id)}/answer`, {
      method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ value }),
    });
    const body = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(body.error || `HTTP ${response.status}`);
    state.browserCheck = body;
    elements.browserCheckAnswer.value = "";
    renderBrowserCheck();
    if (body.state === "done") { state.applicationActionMessage = body.message || "Отклик отправлен из браузера"; await retryRemainingAfterCheck(); refreshApplications(); refreshSummary(); }
  } catch (error) {
    state.applicationActionMessage = `Ответ не принят: ${error.message}`;
    elements.browserCheckSubmit.disabled = false;
  }
}
async function cancelBrowserCheck() {
  const session = state.browserCheck;
  state.browserCheck = null; renderBrowserCheck();
  if (!session) return;
  try {
    await fetch(`/api/v1/applications/${encodeURIComponent(session.application_id)}/browser-check/${encodeURIComponent(session.session_id)}`, { method: "DELETE" });
  } catch (_) { /* the session expires on its own */ }
}

async function retryApplication(item, button) {
  state.applicationActionBusy = true; button.disabled = true; state.applicationActionMessage = "Ставлю повторную подготовку…"; renderApplicationObjects();
  try {
    const task = await enqueue(`/api/v1/applications/${encodeURIComponent(item.id)}/retry`);
    state.applicationActionMessage = `Отклик ${item.id}: задача ${task.task_id}`;
    await refreshApplications();
  } catch (error) { state.applicationActionMessage = error.message; }
  state.applicationActionBusy = false; renderApplicationObjects();
}
function applicationListURL(append = false) {
  const offset = append ? state.applicationObjects.length : state.applicationOffset;
  const parameters = {limit: "200", offset: String(offset), q: state.applicationQuery, sort: state.applicationSort, group: state.applicationFilter};
  if (state.account) parameters.profile_id = state.account;
  return "/api/v1/applications?" + new URLSearchParams(parameters);
}
// refreshApplications reloads the first page; append loads the next one for the
// infinite scroll of the applications table. The first page renders from the
// in-memory cache immediately and revalidates in the background.
async function refreshApplications({ append = false } = {}) {
  const generation = ++state.applicationRequest;
  const requestURL = applicationListURL(append);
  if (!append) {
    const cached = state.cache.applications;
    if (cached && cached.key === requestURL) {
      state.applicationObjects = cached.items; state.applicationTotal = cached.total; state.applicationGroups = cached.groups;
      renderApplicationFilters(); renderApplicationObjects();
    }
  }
  state.applicationLoading = true; updateApplicationSelection();
  try {
    const data = await request(requestURL);
    if (generation !== state.applicationRequest) return;
    const items = data.items || [];
    state.applicationObjects = append ? [...state.applicationObjects, ...items] : items;
    state.applicationTotal = data.total || 0; state.applicationGroups = data.groups || {};
    if (!append) {
      state.cache.applications = { key: requestURL, at: Date.now(), items: state.applicationObjects, total: state.applicationTotal, groups: state.applicationGroups };
    }
    renderApplicationFilters(); renderApplicationObjects();
  } catch (error) { if (generation === state.applicationRequest) state.applicationActionMessage = error.message; }
  finally {
    if (generation === state.applicationRequest) { state.applicationLoading = false; renderApplicationObjects(); }
  }
}
function changeApplicationQuery() {
  state.applicationOffset = 0; state.selectedApplications.clear(); state.applicationActionMessage = ""; return refreshApplications();
}
function updateApplicationSelection(items = visibleApplicationObjects()) {
  const visibleIDs = items.filter(applicationCanRemove).map((item) => item.id);
  elements.applicationSelectAll.disabled = state.applicationActionBusy || !visibleIDs.length;
  const selectedVisible = visibleIDs.filter((id) => state.selectedApplications.has(id)).length;
  elements.applicationSelectAll.checked = visibleIDs.length > 0 && selectedVisible === visibleIDs.length;
  elements.applicationSelectAll.indeterminate = selectedVisible > 0 && selectedVisible < visibleIDs.length;
  elements.applicationSelectionState.textContent = state.applicationActionMessage || (state.selectedApplications.size ? `Выбрано: ${state.selectedApplications.size}` : "Ничего не выбрано");
  elements.applicationBulkAction.disabled = state.applicationActionBusy;
  elements.applicationRunAction.disabled = state.selectedApplications.size === 0 || !elements.applicationBulkAction.value || state.applicationActionBusy;
}
function renderApplicationObjects() {
  const existingIDs = new Set(state.applicationObjects.map((item) => item.id));
  for (const id of state.selectedApplications) if (!existingIDs.has(id)) state.selectedApplications.delete(id);
  const items = visibleApplicationObjects();
  elements.applicationFilterState.textContent = `${state.applicationTotal ? state.applicationOffset + 1 : 0}–${state.applicationOffset + items.length} из ${state.applicationTotal} по фильтру`;
  updateApplicationProfileColumn();
  if (!items.length) { const row = document.createElement("tr"); const cell = text("td", "Под этот фильтр откликов нет"); cell.colSpan = 8; row.append(cell); elements.applicationItems.replaceChildren(row); updateApplicationSelection(items); return; }
  elements.applicationItems.replaceChildren(...items.map((item) => {
    const row = document.createElement("tr");
    const selection = document.createElement("td"); const checkbox = document.createElement("input"); checkbox.type = "checkbox"; checkbox.className = "application-select"; checkbox.disabled = !applicationCanRemove(item) || state.applicationActionBusy; checkbox.checked = state.selectedApplications.has(item.id); checkbox.setAttribute("aria-label", `Выбрать ${item.vacancy_title || item.id}`);
    checkbox.addEventListener("change", () => { if (checkbox.checked) state.selectedApplications.add(item.id); else state.selectedApplications.delete(item.id); updateApplicationSelection(items); }); selection.append(checkbox);
    const vacancy = document.createElement("td"); vacancy.append(text("strong", item.vacancy_title || "Без названия"));
    const action = document.createElement("td"); action.className = "task-actions"; const url = safeExternalURL(item.vacancy_url);
    const validationSkipped = item.status === "skipped" && ["questionnaire_required", "vacancy_test_required", "platform_validation_required"].includes(item.decision_code);
    const needsInput = validationSkipped || item.status === "waiting_validation";
    const vacancyID = (url || "").match(/\/vacancy\/(\d+)/)?.[1];
    if (needsInput && item.platform === "hh") {
      const questionnaire = text("button", "Анкета", "secondary compact"); questionnaire.type = "button";
      questionnaire.disabled = state.applicationActionBusy;
      questionnaire.addEventListener("click", () => captureQuestionnaire(item, questionnaire));
      action.append(questionnaire);
    }
    if (url) {
      if (action.childNodes.length) action.append(document.createTextNode(" "));
      const link = text("a", needsInput ? "Вакансия ↗" : "Открыть ↗", "table-link"); link.href = url; link.target = "_blank"; link.rel = "noopener noreferrer"; action.append(link);
    }
    if (["waiting_validation", "failed"].includes(item.status) || validationSkipped) {
      if (action.childNodes.length) action.append(document.createTextNode(" "));
      const retry = text("button", "Повторить", "secondary compact"); retry.type = "button"; retry.disabled = state.applicationActionBusy;
      retry.addEventListener("click", () => retryApplication(item, retry)); action.append(retry);
    }
    if (!action.childNodes.length) action.textContent = "—";
    const group = applicationGroup(item);
    const profileCell = document.createElement("td"); profileCell.className = "application-profile";
    profileCell.append(text("strong", profileDisplayName(item.profile_id)));
    const profileTag = text("small", item.profile_id, "muted"); profileTag.style.display = "block"; profileCell.append(profileTag);
    row.append(selection, vacancy, text("td", item.employer || "—"), profileCell, statusCell(applicationGroupLabels[group], `status-${group}`), text("td", applicationReason(item)), text("td", formatDate(item.updated_at)), action);
    return row;
  }));
  updateApplicationSelection(items);
}
// The profile column is redundant while one account is selected: every row
// belongs to it. It returns with the combined "all profiles" view.
function updateApplicationProfileColumn() {
  const table = document.getElementById("application-table");
  if (table) table.classList.toggle("profile-hidden", Boolean(state.account));
}
function renderTasks(items = []) {
  const queued = items.filter((item) => !["completed", "dismissed"].includes(item.status));
  if (!queued.length) { const row = document.createElement("tr"); const cell = text("td", "Очередь пуста"); cell.colSpan = 4; row.append(cell); elements.tasks.replaceChildren(row); return; }
  elements.tasks.replaceChildren(...queued.map((item) => {
    const row = document.createElement("tr"); row.append(text("td", taskTypeLabel(item.type)), statusCell(taskStatusLabel(item.status), `task-${item.status}`), text("td", String(item.priority)), text("td", String(item.count))); return row;
  }));
}
function durationParts(value) {
  const match = /^(?:(\d+)h)?(?:(\d+)m)?(?:(\d+(?:\.\d+)?)s)?$/.exec(value || "");
  if (!match) return null;
  return { hours: Number(match[1] || 0), minutes: Number(match[2] || 0), seconds: Math.round(Number(match[3] || 0)) };
}
function formatDuration(value) {
  const parts = durationParts(value);
  if (!parts) return value || "";
  const segments = [];
  if (parts.hours) segments.push(`${parts.hours} ч`);
  if (parts.minutes) segments.push(`${parts.minutes} мин`);
  if (!parts.hours && !parts.minutes && parts.seconds) segments.push(`${parts.seconds} с`);
  return segments.join(" ") || "0 с";
}
function countdownLabel(value) {
  const target = new Date(value).getTime();
  if (Number.isNaN(target)) return "—";
  const diff = target - Date.now();
  if (diff <= 0) return "вот-вот";
  const minutes = Math.floor(diff / 60000);
  if (minutes < 1) return "меньше минуты";
  const days = Math.floor(minutes / 1440);
  const hours = Math.floor((minutes % 1440) / 60);
  const rest = minutes % 60;
  if (days) return `через ${days} д ${hours} ч`;
  if (hours) return `через ${hours} ч ${rest} мин`;
  return `через ${rest} мин`;
}
function updateCountdowns() {
  for (const element of document.querySelectorAll("[data-next-run]")) {
    element.textContent = countdownLabel(element.dataset.nextRun);
    element.title = formatDate(element.dataset.nextRun);
  }
}
setInterval(updateCountdowns, 1000);
function jobProfiles(item) {
  const profiles = Array.isArray(item.profiles) && item.profiles.length ? item.profiles : [item.profile_id];
  return profiles.filter(Boolean);
}
function jobParameterLines(item) {
  const commands = Array.isArray(item.commands) ? item.commands : [];
  if (commands.length > 1) {
    return commands.map((command) => {
      const summary = jobParameterSummary({ payload: command.payload });
      return summary ? `${profileDisplayName(command.profile_id)}: ${summary}` : "";
    }).filter(Boolean);
  }
  const summary = jobParameterSummary(item);
  return summary ? [summary] : [];
}
function jobParameterSummary(item) {
  const payload = item.payload || {};
  const parts = [];
  if (Array.isArray(payload.profiles) && payload.profiles.length) parts.push(`профили: ${payload.profiles.join(", ")}`);
  if (Array.isArray(payload.routes) && payload.routes.length) parts.push(`маршруты: ${payload.routes.join(", ")}`);
  if (payload.target_successful) parts.push(`цель: ${payload.target_successful}`);
  if (payload.max_in_flight) parts.push(`в работе: ${payload.max_in_flight}`);
  if (payload.resume_id || payload.resume) parts.push(`резюме: ${payload.resume_id || payload.resume}`);
  return parts.join(" · ");
}
function jobScheduleLines(item) {
  const seen = new Set();
  const lines = [];
  for (const schedule of item.schedules || []) {
    const parts = [`${schedule.expression} · ${schedule.timezone}`];
    if (schedule.jitter_min || schedule.jitter_max) {
      const min = schedule.jitter_min ? formatDuration(schedule.jitter_min) : "0 с";
      const max = schedule.jitter_max ? formatDuration(schedule.jitter_max) : min;
      parts.push(`jitter ${min}–${max}`);
    }
    const line = parts.join(" · ");
    if (seen.has(line)) continue;
    seen.add(line);
    lines.push(line);
  }
  return lines;
}
function jobNextRunCell(item) {
  const cell = document.createElement("td");
  const runs = (item.schedules || []).map((schedule) => schedule.next_run_at).filter(Boolean).sort();
  if (!runs.length) { cell.append(text("span", "вручную", "muted")); return cell; }
  const value = text("span", "", "countdown");
  value.dataset.nextRun = runs[0];
  cell.append(value);
  const first = (item.schedules || []).find((schedule) => schedule.next_run_at === runs[0]);
  if (first && (first.jitter_min || first.jitter_max)) cell.append(text("span", " + jitter", "muted"));
  return cell;
}
function renderJobs(items = []) {
  items = state.account ? items.filter((item) => jobProfiles(item).includes(state.account)) : items;
  if (!items.length) { const row = document.createElement("tr"); const cell = text("td", "Нет доступных jobs: проверьте enabled, авторизацию и capabilities профиля"); cell.colSpan = 6; row.append(cell); elements.jobs.replaceChildren(row); return; }
  elements.jobs.replaceChildren(...items.map((item) => {
    const row = document.createElement("tr");
    const action = document.createElement("td");
    action.append(text("div", taskTypeLabel(item.task_type)));
    for (const line of jobParameterLines(item)) action.append(text("div", line, "muted"));
    const schedule = document.createElement("td");
    const lines = jobScheduleLines(item);
    if (lines.length) schedule.append(...lines.map((line) => text("div", line)));
    else schedule.append(text("span", "вручную", "muted"));
    const run = document.createElement("td");
    const button = text("button", "Запустить", "secondary compact"); button.type = "button"; button.disabled = state.jobBusy.has(item.tag);
    button.addEventListener("click", () => runJob(item)); run.append(button);
    const profileCell = document.createElement("td"); profileCell.className = "application-profile";
    const names = jobProfiles(item).map(profileDisplayName);
    if (names.length) profileCell.append(...names.map((name) => text("div", name)));
    else profileCell.append(text("span", "—", "muted"));
    const jobCell = document.createElement("td");
    if (item.description) {
      jobCell.append(text("div", item.description));
      jobCell.append(text("small", item.tag, "muted"));
    } else {
      jobCell.append(text("div", item.tag));
    }
    row.append(jobCell, action, profileCell, schedule, jobNextRunCell(item), run);
    return row;
  }));
  updateCountdowns();
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
    row.append(text("td", item.id, "task-id"), text("td", taskTypeLabel(item.type)), text("td", item.profile_id ? profileDisplayName(item.profile_id) : "—"), text("td", String(item.attempts)), text("td", error, "task-error"), text("td", formatDate(item.updated_at)), actions);
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
    row.append(text("td", profileDisplayName(item.profile_id)), text("td", item.platform), text("td", activityKindLabels[item.kind] || item.kind), text("td", String(item.count)), text("td", formatDate(item.last_occurred_at)));
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
  if (!latest.length) { elements.activityObservations.replaceChildren(text("p", "Метрики ещё не снимались. Запустите job «Обновить метрики резюме».", "empty panel")); return; }
  elements.activityObservations.replaceChildren(...latest.map((item) => {
    const card = document.createElement("article"); card.className = "panel activity-card";
    const heading = document.createElement("div"); heading.className = "panel-heading";
    const identity = document.createElement("div"); const resume = text("p", `${item.platform} · резюме ${compactID(item.resume_id)}`, "muted"); resume.title = item.resume_id; identity.append(text("h2", profileDisplayName(item.profile_id)), resume);
    heading.append(identity, text("span", item.period_days === undefined ? "период не указан" : `${item.period_days} дней`, "tag")); card.append(heading);
    const metrics = document.createElement("div"); metrics.className = "activity-metrics";
    const cards = [];
    if (typeof item.score === "number") cards.push(["Активность", `${item.score}%`, ""]);
    cards.push(["Показы в поиске", counter(item.search_shows), ""], ["Просмотры", counter(item.views), item.new_views ? `+${item.new_views}` : ""], ["Приглашения", counter(item.invitations), item.new_invitations ? `+${item.new_invitations}` : ""]);
    cards.forEach(([label, value, delta]) => {
      const metric = document.createElement("div"); metric.append(text("span", label), text("strong", value), delta ? text("small", delta) : document.createTextNode("")); metrics.append(metric);
    });
    card.append(metrics, text("p", `Снято ${formatDate(item.observed_at)}`, "muted")); return card;
  }));
}
// visibleConversations sorts the server-filtered page. The open conversation
// stays pinned at the top even when a filter no longer matches it: reading a
// chat must not yank it out of sight until the operator selects another one.
function visibleConversations(items = []) {
  let visible = [...items];
  const selected = state.selectedConversation;
  if (state.conversationFilter === "unread") {
    // Read chats leave the unread filter as soon as the read action is local,
    // even if the platform page still carries the old counter. The open chat
    // stays pinned until the operator selects another one.
    visible = visible.filter((item) => item.unread_count || item.id === selected?.id);
  }
  if (selected && !visible.some((item) => item.id === selected.id)) {
    // Reading a chat clears its counter, so the unread filter drops it from
    // the server page; the open chat stays at its previous position until the
    // operator selects another one.
    const index = Math.min(Math.max(state.conversationPinnedIndex || 0, 0), visible.length);
    visible.splice(index, 0, selected);
  }
  const stringCompare = (left, right) => String(left || "").localeCompare(String(right || ""), "ru", { sensitivity: "base" });
  return visible.sort((left, right) => {
    switch (state.conversationSort) {
    case "updated_asc": return new Date(left.updated_at) - new Date(right.updated_at);
    case "unread_desc": return Number(right.unread_count || 0) - Number(left.unread_count || 0) || new Date(right.updated_at) - new Date(left.updated_at);
    case "employer_asc": return stringCompare(left.employer, right.employer) || new Date(right.updated_at) - new Date(left.updated_at);
    default: return new Date(right.updated_at) - new Date(left.updated_at);
    }
  });
}
// Locally read chats keep their zero counter until the durable task lands,
// even if a background refresh still shows the platform value.
function markLocallyRead(id) {
  if (!id) return;
  state.localReads.set(id, Date.now() + 900_000);
}
function applyLocalReads(items = []) {
  if (!state.localReads.size) return { items, hiddenUnread: 0 };
  const now = Date.now();
  let hiddenUnread = 0;
  const result = items.map((item) => {
    const expires = state.localReads.get(item.id);
    if (expires === undefined) return item;
    if (expires <= now) { state.localReads.delete(item.id); return item; }
    if (!item.unread_count) { state.localReads.delete(item.id); return item; }
    hiddenUnread += Number(item.unread_count);
    return { ...item, unread_count: 0 };
  });
  return { items: result, hiddenUnread };
}

function renderConversations(items = state.conversationItems) {
  updateMarkAllRead(items);
  const visible = visibleConversations(items);
  if (state.selectedConversation) {
    const index = visible.findIndex((item) => item.id === state.selectedConversation.id);
    if (index >= 0) state.conversationPinnedIndex = index;
  }
  const loaded = state.conversationItems.length;
  elements.conversationPageState.textContent = state.conversationTotal ? `Показано ${loaded} из ${state.conversationTotal}` : "";
  if (!visible.length) { elements.conversations.replaceChildren(text("p", items.length ? "Под этот фильтр диалогов нет" : "Диалогов пока нет", "empty")); return; }
  elements.conversations.replaceChildren(...visible.map((item) => {
    const button = document.createElement("button"); button.type = "button"; button.className = `conversation${state.selectedConversation?.id === item.id ? " active" : ""}`;
    const heading = document.createElement("span"); heading.className = "conversation-heading";
    const title = document.createElement("span"); title.className = "conversation-title";
    title.append(text("strong", conversationLabel(item)));
    if (item.questionnaire_open) title.append(text("span", "опросник", "questionnaire-badge"));
    heading.append(title);
    if (item.unread_count) heading.append(text("span", String(item.unread_count), "unread-badge"));
    button.append(heading, text("span", item.employer || "Компания не определена", "conversation-employer"), text("small", `${profileDisplayName(item.profile_id)} · ${conversationStatusLabels[item.status] || item.status} · ${formatDate(item.updated_at)}`));
    button.addEventListener("click", () => selectConversation(item)); return button;
  }));
}
// refreshConversations loads one page of the server-filtered conversation
// list. Filters and the search run in SQL, so pagination stays correct for
// thousands of dialogs.
async function refreshConversations({ append = false } = {}) {
  if (state.conversationLoading) return;
  state.conversationLoading = true;
  const offset = append ? state.conversationItems.length : 0;
  const params = new URLSearchParams({ limit: "50", offset: String(offset) });
  if (state.account) params.set("profile_id", state.account);
  if (state.conversationFilter === "unread") params.set("unread", "1");
  else if (state.conversationFilter === "questionnaire") params.set("questionnaire", "1");
  else if (state.conversationFilter) params.set("status", state.conversationFilter);
  if (state.conversationQuery.trim()) params.set("q", state.conversationQuery.trim());
  const cacheKey = params.toString();
  if (!append) {
    const cached = state.cache.conversations.get(cacheKey);
    if (cached) {
      state.conversationItems = cached.items; state.conversationTotal = cached.total; state.conversationUnreadTotal = cached.unread;
      renderConversations(state.conversationItems);
    }
  }
  try {
    const page = await request(`/api/v1/conversations?${params}`);
    const applied = append ? { items: page.items || [], hiddenUnread: 0 } : applyLocalReads(page.items || []);
    state.conversationItems = append ? [...state.conversationItems, ...applied.items] : applied.items;
    state.conversationTotal = Number(page.total || 0);
    state.conversationUnreadTotal = Math.max(0, Number(page.unread_total || 0) - applied.hiddenUnread);
    if (!append) {
      state.cache.conversations.set(cacheKey, {
        at: Date.now(), items: state.conversationItems, total: state.conversationTotal, unread: state.conversationUnreadTotal,
      });
      if (state.cache.conversations.size > 12) {
        const oldest = [...state.cache.conversations.entries()].sort((left, right) => left[1].at - right[1].at)[0];
        if (oldest) state.cache.conversations.delete(oldest[0]);
      }
    }
    if (state.selectedConversation) {
      const fresh = state.conversationItems.find((item) => item.id === state.selectedConversation.id);
      if (fresh) state.selectedConversation = fresh;
    }
    renderConversations(state.conversationItems);
    renderAccountSwitcher(state.summary?.profiles || []);
  } catch (error) {
    state.conversationTotal = state.conversationItems.length;
    elements.conversationBulkState.textContent = error.message;
  } finally {
    state.conversationLoading = false;
  }
}

function updateMarkAllRead(items = []) {
  const unread = Number(state.conversationUnreadTotal || 0) || items.reduce((sum, item) => sum + Number(item.unread_count || 0), 0);
  const pending = (state.summary?.tasks || []).some((item) => item.type === "conversation.mark_read" && ["new", "processing", "retry_scheduled", "waiting_confirmation"].includes(item.status));
  elements.markAllRead.textContent = unread ? `Прочитать все (${unread})` : "Все прочитано";
  // Pending tasks must not lock the button: pressing it again only enqueues the
  // remaining unread chats.
  elements.markAllRead.disabled = state.markAllReadBusy || unread === 0;
  if (pending) elements.conversationBulkState.textContent = "Прочтение уже выполняется";
}
// withPendingMessages keeps optimistic bubbles visible while the durable task
// and the next sync replace them. Pending items belong to one conversation, so
// a bubble never leaks into another chat.
function withPendingMessages(items, conversationID) {
  const pending = (state.selectedMessages || []).filter((item) => String(item.id).startsWith("pending-") && item.conversation_id === conversationID);
  if (!pending.length) return items;
  if (conversationID && (!state.selectedConversation || state.selectedConversation.id !== conversationID)) return items;
  const stored = new Set((items || []).filter((item) => item.direction === "outgoing").map((item) => String(item.text || "").trim()));
  return [...(items || []), ...pending.filter((item) => !stored.has(String(item.text || "").trim()))];
}

function renderMessages(items = []) {
  if (!items.length) { elements.messages.replaceChildren(text("p", "В этом диалоге сообщений пока нет.", "empty")); return; }
  elements.messages.replaceChildren(...items.map((item) => {
    if (item.kind === "system") return text("p", plainText(item.text) || "Системное событие", "message-system");
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
    const statusLabel = item.status === "pending" ? "Отправляется…" : item.status === "queued" ? "В очереди" : formatDate(item.occurred_at);
    meta.append(text("span", item.direction === "outgoing" ? "Вы" : "Собеседник"), text("span", statusLabel));
    article.append(meta); return article;
  })); elements.messages.scrollTop = elements.messages.scrollHeight;
}

async function sendQuestionnaireOption(message, option) {
  const conversation = state.selectedConversation;
  if (!conversation || !option?.text) return;
  const key = `dashboard-answer:${conversation.id}:${message.id}:${option.id}`;
  state.conversationAnswerBusy = `${message.id}:${option.id}`;
  elements.actionState.textContent = `Отправляю «${option.text}»…`;
  // Optimistic bubble: the answer shows up immediately, the durable task and
  // the next sync replace it with the stored message.
  const pendingID = `pending-answer-${message.id}-${option.id}`;
  if (!state.selectedMessages.some((item) => item.id === pendingID)) {
    state.selectedMessages = [...state.selectedMessages, {
      id: pendingID, direction: "outgoing", kind: "text", status: "pending",
      text: option.text, conversation_id: conversation.id, occurred_at: new Date().toISOString(),
    }];
  }
  renderMessages(state.selectedMessages);
  try {
    const result = await enqueue(`/api/v1/conversations/${encodeURIComponent(conversation.id)}/messages?live=1`, {
      content: { text: option.text },
    }, key);
    if (result.closed) {
      elements.actionState.textContent = "Работодатель закрыл чат — опросник завершён";
      await refreshSummary();
    } else {
      elements.actionState.textContent = result.sent ? "Ответ отправлен" : `Задача ${result.task_id} поставлена в очередь`;
      try {
        const fresh = await request(`/api/v1/conversations/${encodeURIComponent(conversation.id)}/messages`);
        if (state.selectedConversation?.id === conversation.id) {
          state.selectedMessages = withPendingMessages(fresh.items || [], conversation.id);
          renderMessages(state.selectedMessages);
        }
      } catch (_) {}
      await refreshSummary();
    }
  } catch (error) {
    elements.actionState.textContent = error.message;
    state.selectedMessages = state.selectedMessages.filter((item) => item.id !== pendingID);
  }
  state.conversationAnswerBusy = "";
  renderMessages(state.selectedMessages);
}

function renderProfileResources() {
  if (!state.profileResources.length) { elements.profileResources.replaceChildren(text("p", "Desired-state ресурсов пока нет.", "empty panel")); return; }
  elements.profileResources.replaceChildren(...state.profileResources.map((resource) => {
    const card = document.createElement("article"); card.className = "panel resource-card";
    const heading = document.createElement("div"); heading.className = "panel-heading";
    const identity = document.createElement("div"); identity.append(text("h2", resource.tag), text("p", `${profileDisplayName(resource.profile_id)} · ${resource.ownership}`, "muted"));
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

function profileDisplayName(profileID) {
  const profiles = state.summary?.profiles || [];
  const entry = profiles.find((item) => item && item.id === profileID);
  return (entry && entry.display_name) || profileID;
}

function renderConfigState(status) {
  const element = document.getElementById("config-state");
  if (!element) return;
  if (!status || !status.digest) { element.textContent = ""; element.title = ""; return; }
  const digest = String(status.digest).slice(0, 8);
  const applied = status.applied_at ? formatDate(status.applied_at) : "";
  if (status.last_error) {
    element.textContent = `конфиг: ошибка (${digest})`;
    element.title = status.last_error;
    element.className = "muted error";
    return;
  }
  element.textContent = `конфиг: ${digest}${applied ? ` · ${applied}` : ""}`;
  element.title = `Применено определений: ${status.definitions || 0}`;
  element.className = "muted";
}

function renderAccountSwitcher(profiles = []) {
  const labels = new Map();
  for (const item of profiles) {
    if (item && item.id) labels.set(item.id, item.display_name || item.id);
  }
  for (const item of state.conversationItems || []) if (item.profile_id) labels.set(item.profile_id, labels.get(item.profile_id) || item.profile_id);
  for (const item of state.summary?.activity || []) if (item.profile_id) labels.set(item.profile_id, labels.get(item.profile_id) || item.profile_id);
  for (const item of state.failedTasks || []) if (item.profile_id) labels.set(item.profile_id, labels.get(item.profile_id) || item.profile_id);
  for (const item of state.jobs || []) if (item.profile_id) labels.set(item.profile_id, labels.get(item.profile_id) || item.profile_id);
  for (const item of state.reviewSessions || []) if (item.profile_id) labels.set(item.profile_id, labels.get(item.profile_id) || item.profile_id);
  for (const item of state.applicationObjects || []) if (item.profile_id) labels.set(item.profile_id, labels.get(item.profile_id) || item.profile_id);
  const options = [...labels.keys()].sort();
  if (state.account && !options.includes(state.account)) state.account = "";
  const select = elements.accountSwitcher;
  const all = document.createElement("option"); all.value = ""; all.textContent = "Все аккаунты";
  select.replaceChildren(all, ...options.map((value) => {
    const option = document.createElement("option"); option.value = value; option.textContent = labels.get(value) || value; return option;
  }));
  select.value = state.account;
}

function reviewStatusLabel(value) { return reviewStatusLabels[value] || value || "—"; }

const reviewPageSize = 50;

async function refreshReviewSessions(options = {}) {
  const append = options.append === true;
  const parameters = new URLSearchParams({ limit: String(reviewPageSize), offset: String(append ? state.reviewSessions.length : 0) });
  if (elements.reviewFilter.value) parameters.set("status", elements.reviewFilter.value);
  if (state.account) parameters.set("profile_id", state.account);
  if (state.reviewQuery) parameters.set("q", state.reviewQuery);
  const cacheKey = parameters.toString();
  if (!append) {
    const cached = state.cache.reviews;
    if (cached && cached.key === cacheKey) state.reviewSessions = cached.items;
  }
  try {
    const result = await request(`/api/v1/review-sessions?${parameters}`);
    const items = result.items || [];
    state.reviewSessions = append ? [...state.reviewSessions, ...items] : items;
    if (!append) state.cache.reviews = { key: cacheKey, at: Date.now(), items: state.reviewSessions };
    state.reviewHasMore = items.length === reviewPageSize;
    elements.reviewMore.hidden = !state.reviewHasMore;
    elements.reviewState.textContent = state.reviewSessions.length
      ? `Сессий: ${state.reviewSessions.length}${state.reviewHasMore ? "+" : ""}`
      : "Нет сессий";
    renderReviewSessions();
  } catch (error) {
    elements.reviewState.textContent = error.message;
    elements.reviewSessions.replaceChildren(text("p", "Не удалось загрузить проверки.", "empty panel"));
  }
}

async function cancelReviewSession(session) {
  const title = session.vacancy?.title || session.question || `проверку ${compactID(session.id)}`;
  if (!globalThis.confirm(`Убрать «${title}» из списка? Ответ не будет отправлен на платформу.`)) return;
  state.reviewBusy = true;
  renderReviewSessions();
  try {
    await enqueue(`/api/v1/review-sessions/${encodeURIComponent(session.id)}/cancel`, { expected_revision: session.revision });
    if (state.reviewSelected?.id === session.id) state.reviewSelected = null;
    state.reviewMessage = "Проверка убрана";
    await refreshReviewSessions();
  } catch (error) {
    elements.reviewState.textContent = error.message;
  } finally {
    state.reviewBusy = false;
    renderReviewSessions();
  }
}

// reviewVacancyKey groups the per-profile sessions of one vacancy into a
// single card: the questionnaire is per vacancy, the profiles only choose
// where the filled application is sent from.
function reviewVacancyID(session) {
  const url = safeExternalURL(session.vacancy?.url || "");
  return url ? (url.match(/\/vacancy\/(\d+)/)?.[1] || "") : "";
}
// sendReviewApplication enqueues the submit retry for the selected profile: a
// filled questionnaire is attached by the application pipeline itself.
function reviewSendTargets() {
  const selected = [...state.reviewSendProfiles];
  if (selected.length) return selected;
  return state.reviewSelected ? [state.reviewSelected.profile_id] : [];
}
function updateReviewSendButton() {
  const targets = reviewSendTargets();
  elements.reviewSend.textContent = targets.length > 1 ? `Отправить отклик (${targets.length})` : "Отправить отклик";
  elements.reviewSend.title = targets.length > 1 ? `Профили: ${targets.map((item) => profileDisplayName(item)).join(", ")}` : "";
}
async function sendReviewApplication() {
  const session = state.reviewSelected;
  const vacancyID = session ? reviewVacancyID(session) : "";
  if (!session || !vacancyID) {
    elements.reviewState.textContent = "У выбранной анкеты нет ссылки на вакансию";
    return;
  }
  elements.reviewSend.disabled = true; elements.reviewState.textContent = "Ищу отклики по вакансии…";
  const targets = reviewSendTargets();
  const queued = [], missing = [];
  try {
    for (const profileID of targets) {
      const params = new URLSearchParams({ profile_id: profileID, vacancy_id: vacancyID, limit: "20" });
      const listing = await request(`/api/v1/applications?${params}`);
      const items = listing.items || [];
      const application = items.find((item) => item.status === "waiting_validation") || items.find((item) => item.status === "failed") || items[0];
      if (!application) { missing.push(profileID); continue; }
      await enqueue(`/api/v1/applications/${encodeURIComponent(application.id)}/retry`);
      queued.push(profileID);
    }
    const parts = [];
    if (queued.length) parts.push(`в очереди: ${queued.map((item) => profileDisplayName(item)).join(", ")}`);
    if (missing.length) parts.push(`не найден отклик: ${missing.map((item) => profileDisplayName(item)).join(", ")}`);
    elements.reviewState.textContent = parts.join(" · ") || "Отклики не найдены";
    refreshApplications(); refreshSummary();
  } catch (error) {
    elements.reviewState.textContent = error.message;
  } finally {
    elements.reviewSend.disabled = false;
  }
}

function reviewVacancyKey(session) {
  const vacancy = session.vacancy || {};
  const url = safeExternalURL(vacancy.url || "");
  if (url) {
    const id = url.match(/\/vacancy\/(\d+)/)?.[1];
    if (id) return `vacancy:${id}`;
  }
  return `title:${vacancy.title || session.question || session.id}|${vacancy.employer || ""}`;
}
function renderReviewSessions() {
  const layout = elements.reviewSessions.closest(".review-layout");
  const sessions = state.reviewSessions || [];
  if (layout) layout.classList.toggle("empty", !sessions.length);
  if (!sessions.length) { elements.reviewSessions.replaceChildren(text("p", "Проверок нет.", "empty")); return; }
  if (!state.reviewSelected || !sessions.some((item) => item.id === state.reviewSelected.id)) {
    selectReviewSession(sessions[0]);
    return;
  }
  const groups = new Map();
  for (const session of sessions) {
    const key = reviewVacancyKey(session);
    if (!groups.has(key)) groups.set(key, { vacancy: session.vacancy || {}, sessions: [] });
    groups.get(key).sessions.push(session);
  }
  elements.reviewSessions.replaceChildren(...[...groups.values()].map((group) => {
    const card = document.createElement("div"); card.className = "review-vacancy";
    const heading = document.createElement("div"); heading.className = "review-vacancy-heading";
    heading.append(text("strong", group.vacancy.title || "Без названия"));
    heading.append(text("small", group.vacancy.employer || "Компания не определена", "muted"));
    card.append(heading);
    const profiles = document.createElement("div"); profiles.className = "review-vacancy-profiles";
    for (const session of group.sessions) {
      const chip = document.createElement("label");
      chip.className = `review-profile${state.reviewSelected?.id === session.id ? " active" : ""}`;
      const control = document.createElement("input"); control.type = "checkbox";
      control.checked = state.reviewSendProfiles.has(session.profile_id);
      control.addEventListener("change", () => {
        if (control.checked) state.reviewSendProfiles.add(session.profile_id);
        else state.reviewSendProfiles.delete(session.profile_id);
        updateReviewSendButton();
      });
      const name = document.createElement("button"); name.type = "button"; name.className = "review-profile-name";
      name.append(text("span", profileDisplayName(session.profile_id)));
      name.append(text("small", reviewStatusLabel(session.status)));
      name.addEventListener("click", () => selectReviewSession(session));
      chip.append(control, name);
      profiles.append(chip);
    }
    card.append(profiles);
    const selected = group.sessions.find((item) => item.id === state.reviewSelected?.id);
    if (selected) {
      const cancel = document.createElement("button");
      cancel.type = "button"; cancel.className = "secondary compact review-cancel";
      cancel.textContent = "Убрать"; cancel.disabled = state.reviewBusy;
      cancel.addEventListener("click", () => cancelReviewSession(selected));
      card.append(cancel);
    }
    return card;
  }));
}

async function selectReviewSession(session) {
  state.reviewSelected = session; state.reviewMessage = ""; renderReviewSessions();
  const vacancy = session.vacancy || {};
  elements.reviewSessionTitle.textContent = vacancy.title || `Проверка ${compactID(session.id)}`;
  const meta = vacancy.title
    ? [vacancy.employer || "Компания не определена", reviewStatusLabel(session.status), `профиль ${profileDisplayName(session.profile_id)}`]
    : [reviewStatusLabel(session.status), session.platform, `профиль ${profileDisplayName(session.profile_id)}`, `revision ${session.revision}`];
  elements.reviewSessionMeta.textContent = meta.join(" · ");
  if (vacancy.url) {
    const link = document.createElement("a"); link.href = safeExternalURL(vacancy.url) || vacancy.url; link.target = "_blank"; link.rel = "noopener noreferrer";
    link.textContent = " Открыть вакансию ↗"; link.className = "table-link";
    elements.reviewSessionMeta.append(link);
  }
  elements.reviewSend.hidden = !reviewVacancyID(session);
  elements.reviewSend.disabled = state.reviewBusy;
  updateReviewSendButton();
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
  form.append(text("p", plainText(prompt.question.text), "review-question"));
  const kindLabels = { single: "один вариант", multiple: "несколько вариантов", text: "текстовый ответ" };
  const remaining = Array.isArray(detail.questions) ? detail.questions.length : 0;
  form.append(text("p", `Тип ответа: ${kindLabels[kind] || kind} · в банке ответа ещё нет${remaining > 1 ? ` · осталось вопросов: ${remaining}` : ""}`, "muted"));
  const bankLabel = document.createElement("label"); bankLabel.className = "review-option";
  const bankControl = document.createElement("input"); bankControl.type = "checkbox"; bankControl.checked = true; bankControl.id = "review-bank";
  bankLabel.append(bankControl, text("span", "Сохранить ответ в банк — пригодится в других анкетах"));
  form.append(bankLabel);
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
      label.append(control, text("span", plainText(option.text)));
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
    block.append(text("p", plainText(question.text), "review-question"));
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
        label.append(control, text("span", plainText(option.text)));
        input.append(label);
      }
    } else {
      unsupported = true;
      input = text("p", `Тип вопроса «${question.kind}» пока не поддерживается интерактивно.`, "empty");
    }
    block.append(input);
    const questionBankLabel = document.createElement("label"); questionBankLabel.className = "review-option review-bank-question";
    const questionBank = document.createElement("input"); questionBank.type = "checkbox"; questionBank.checked = true;
    questionBankLabel.append(questionBank, text("span", "Сохранить этот ответ в банк"));
    block.append(questionBankLabel);
    form.append(block);
    fields.push({ question, input, block });
  }
  const bankLabel = document.createElement("label"); bankLabel.className = "review-option";
  const bankControl = document.createElement("input"); bankControl.type = "checkbox"; bankControl.checked = true; bankControl.id = "review-bank";
  bankLabel.append(bankControl, text("span", "Сохранить ответы в банк — пригодятся в других анкетах"));
  // The bulk control mirrors the per-question controls and back: unchecking one
  // answer clears the bulk checkbox, mixed selections become indeterminate.
  const questionBanks = [...form.querySelectorAll(".review-bank-question input")];
  const syncBankControl = () => {
    const checked = questionBanks.filter((control) => control.checked).length;
    bankControl.checked = questionBanks.length > 0 && checked === questionBanks.length;
    bankControl.indeterminate = checked > 0 && checked < questionBanks.length;
  };
  for (const control of questionBanks) control.addEventListener("change", syncBankControl);
  bankControl.addEventListener("change", () => {
    for (const control of questionBanks) control.checked = bankControl.checked;
    bankControl.indeterminate = false;
  });
  syncBankControl();
  form.append(bankLabel);
  const footer = document.createElement("div"); footer.className = "review-actions";
  const submit = text("button", "Сохранить все ответы"); submit.type = "submit";
  submit.disabled = state.reviewBusy || unsupported;
  footer.append(text("span", state.reviewMessage, "muted"), submit); form.append(footer);
  form.addEventListener("submit", (event) => { event.preventDefault(); submitReviewBatch(detail, fields); });
  elements.reviewPrompt.replaceChildren(form);
}

function questionBankValue(block) { return block.querySelector(".review-bank-question input")?.checked !== false; }

async function submitReviewBatch(detail, fields) {
  if (state.reviewBusy) return;
  const answers = [];
  for (const { question, input, block } of fields) {
    if (question.kind === "text") {
      const value = input.value.trim();
      if (!value) { state.reviewMessage = `Заполните: ${question.text}`; renderReviewPrompt(); return; }
      answers.push({ question_id: question.id, text: value, bank: questionBankValue(block) });
      continue;
    }
    if (question.kind === "single" || question.kind === "multiple") {
      const selected = [...input.querySelectorAll("input:checked")].map((control) => control.value);
      if (question.kind === "single" && selected.length !== 1) { state.reviewMessage = `Выберите один вариант: ${question.text}`; renderReviewPrompt(); return; }
      if (question.kind === "multiple" && selected.length === 0) { state.reviewMessage = `Выберите хотя бы один вариант: ${question.text}`; renderReviewPrompt(); return; }
      answers.push({ question_id: question.id, selected_options: selected, bank: questionBankValue(block) });
      continue;
    }
    state.reviewMessage = `Вопрос «${question.text}» не поддерживается`; renderReviewPrompt(); return;
  }
  state.reviewBusy = true; state.reviewMessage = "Отправляю…"; renderReviewPrompt();
  try {
    await enqueue(`/api/v1/review-sessions/${encodeURIComponent(detail.id)}/answers`, {
      expected_revision: detail.revision, source: "dashboard", answers,
      bank: document.querySelector("#review-bank")?.checked !== false,
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
      bank: form.querySelector("#review-bank")?.checked !== false,
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

async function refreshSummary({ background = false } = {}) {
  if (!background) {
    elements.refresh.disabled = true; elements.connectionState.textContent = "Обновление…"; elements.connectionDot.className = "dot pending";
  }
  if (state.cache.summary) {
    state.summary = state.cache.summary;
    renderAccountSwitcher(state.summary.profiles || []);
    renderConfigState(state.summary.config_status); renderStats(state.summary); renderTasks(state.summary.tasks || []);
    renderCampaigns(state.summary.campaigns || []); renderActivity(state.summary.activity || []); renderActivityObservations(state.summary.activity_snapshots || []);
  }
  try {
    const [summary, failures, jobs] = await Promise.all([request("/api/v1/dashboard/summary"), request("/api/v1/tasks/failed"), request("/api/v1/jobs"), refreshApplications()]);
    state.summary = summary; state.cache.summary = summary; state.failedTasks = failures.items || []; state.jobs = jobs.items || [];
    renderAccountSwitcher(summary.profiles || []);
    renderAuthProfileOptions(summary.profiles || []);
    updateCaptchaWarning();
    renderConfigState(summary.config_status); renderStats(summary); renderApplicationFilters(state.applicationObjects); renderApplicationObjects(); renderTasks(summary.tasks || []); renderJobs(state.jobs); renderCampaigns(summary.campaigns || []); renderFailedTasks(state.failedTasks); renderActivity(summary.activity || []); renderActivityObservations(summary.activity_snapshots || []); updateMarkAllRead(state.conversationItems);
    elements.updatedAt.textContent = `Обновлено ${formatDate(summary.generated_at)}`; elements.connectionState.textContent = "Backend доступен"; elements.connectionDot.className = "dot ok";
  } catch (error) { elements.connectionState.textContent = error.message; elements.connectionDot.className = "dot error"; }
  finally { if (!background) elements.refresh.disabled = false; }
}
async function refreshVersion() {
  try {
    const info = await request("/api/v1/version");
    elements.runtimeVersion.textContent = `v${info.version} · API ${info.api_version}`;
    elements.runtimeVersion.title = `commit ${info.commit} · build ${info.build_time} · modified ${info.modified}`;
  } catch (error) { elements.runtimeVersion.textContent = "версия недоступна"; }
}
async function selectConversation(conversation) {
  state.selectedConversation = conversation; renderConversations(state.conversationItems); elements.chatTitle.textContent = conversationLabel(conversation); elements.chatMeta.textContent = `${conversation.employer || "Компания не определена"} · профиль ${profileDisplayName(conversation.profile_id)} · ${conversationStatusLabels[conversation.status] || conversation.status}`;
  const vacancyURL = safeExternalURL(conversation.vacancy_url); elements.chatVacancyLink.classList.toggle("hidden", !vacancyURL); if (vacancyURL) elements.chatVacancyLink.href = vacancyURL; else elements.chatVacancyLink.removeAttribute("href");
  state.selectedMessages = [];
  elements.reply.disabled = false; elements.send.disabled = false; elements.messages.replaceChildren(text("p", "Загрузка…", "empty"));
  try {
    const result = await request(`/api/v1/conversations/${encodeURIComponent(conversation.id)}/messages?live=1`);
    if (state.selectedConversation?.id !== conversation.id) return;
    state.selectedMessages = withPendingMessages(result.items || [], conversation.id); renderMessages(state.selectedMessages);
  } catch (error) { elements.messages.replaceChildren(text("p", error.message, "empty")); }
  try {
    const sync = await enqueue(`/api/v1/conversations/${encodeURIComponent(conversation.id)}/sync`);
    if (sync.created) {
      globalThis.setTimeout(async () => {
        if (state.selectedConversation?.id !== conversation.id) return;
        try {
          const fresh = await request(`/api/v1/conversations/${encodeURIComponent(conversation.id)}/messages`);
          if (state.selectedConversation?.id !== conversation.id) return;
          state.selectedMessages = withPendingMessages(fresh.items || [], conversationID); renderMessages(state.selectedMessages);
          refreshSummary();
        } catch (_) {}
      }, 4000);
    }
  } catch (_) {}
  if (conversation.unread_count && !state.conversationReadBusy.has(conversation.id)) {
    state.conversationReadBusy.add(conversation.id);
    // Optimistic: the counter drops immediately, the durable task confirms it.
    const unread = Number(conversation.unread_count || 0);
    markLocallyRead(conversation.id);
    conversation.unread_count = 0;
    state.conversationUnreadTotal = Math.max(0, state.conversationUnreadTotal - unread);
    renderConversations(state.conversationItems);
    elements.actionState.textContent = "Помечаю открытый диалог прочитанным…";
    try {
      const key = `dashboard-open:${conversation.id}:${conversation.revision}`;
      await enqueue(`/api/v1/conversations/${encodeURIComponent(conversation.id)}/mark-read`, undefined, key);
      elements.actionState.textContent = "Диалог помечен прочитанным";
      refreshConversations(); refreshSummary();
    } catch (error) {
      elements.actionState.textContent = error.message;
      conversation.unread_count = unread;
      state.conversationUnreadTotal += unread;
      renderConversations(state.conversationItems);
    }
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
    const result = await enqueue(`/api/v1/conversations/${encodeURIComponent(conversationID)}/messages?live=1`, { content: { text: value } });
    elements.reply.value = "";
    // Optimistic bubble: the durable task confirms it, and the next message
    // refresh (SSE or interval) replaces the pending copy with the stored one.
    if (state.selectedConversation?.id === conversationID) {
      const pendingID = `pending-${result.task_id}`;
      state.selectedMessages = [...state.selectedMessages, {
        id: pendingID, direction: "outgoing", kind: "text", status: "pending",
        text: value, conversation_id: conversationID, occurred_at: new Date().toISOString(),
      }];
      renderMessages(state.selectedMessages);
      let attempts = 0;
      const confirm = async () => {
        if (state.selectedConversation?.id !== conversationID) return;
        try {
          const fresh = await request(`/api/v1/conversations/${encodeURIComponent(conversationID)}/messages`);
          if (state.selectedConversation?.id !== conversationID) return;
          const items = withPendingMessages(fresh.items || [], conversationID);
          state.selectedMessages = items;
          renderMessages(items);
          const stored = items.some((item) => item.direction === "outgoing" && item.text === value);
          if (stored) return;
        } catch (_) {}
        if (++attempts < 10) globalThis.setTimeout(confirm, 1500);
      };
      globalThis.setTimeout(confirm, 1200);
    }
    if (result.closed) {
      elements.actionState.textContent = "Работодатель закрыл чат — отклик больше не требует ответа";
      state.selectedMessages = state.selectedMessages.filter((item) => !String(item.id).startsWith("pending-"));
      renderMessages(state.selectedMessages);
      await refreshSummary();
    } else {
      elements.actionState.textContent = result.sent ? "Сообщение отправлено" : `Задача ${result.task_id} поставлена в очередь`;
      await refreshSummary();
    }
  } catch (error) { elements.actionState.textContent = error.message; } finally { elements.send.disabled = false; }
});
elements.markAllRead.addEventListener("click", async () => {
  state.markAllReadBusy = true;
  const previousUnread = state.conversationItems.map((item) => item.unread_count || 0);
  for (const item of state.conversationItems) { if (item.unread_count) markLocallyRead(item.id); item.unread_count = 0; }
  state.conversationUnreadTotal = 0;
  renderConversations(state.conversationItems);
  elements.conversationBulkState.textContent = "Ставлю задачи в очередь…";
  try {
    const result = await enqueue("/api/v1/conversations/mark-read");
    elements.conversationBulkState.textContent = result.created ? `Непрочитанных диалогов: ${result.created}` : "Новых задач не потребовалось";
    refreshConversations(); refreshSummary();
  } catch (error) {
    elements.conversationBulkState.textContent = error.message;
    state.conversationItems.forEach((item, index) => { item.unread_count = previousUnread[index]; });
    state.conversationUnreadTotal = previousUnread.reduce((sum, value) => sum + value, 0);
    renderConversations(state.conversationItems);
  }
  finally { state.markAllReadBusy = false; }
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
elements.accountCaptchaButton.addEventListener("click", () => startCaptchaCheck(elements.accountCaptcha.dataset.profile || state.account));
elements.browserCheckSubmit.addEventListener("click", submitBrowserCheckAnswer);
elements.browserCheckAnswer.addEventListener("keydown", (event) => { if (event.key === "Enter") submitBrowserCheckAnswer(); });
elements.browserCheckRefreshImage.addEventListener("click", () => { if (state.browserCheck) elements.browserCheckImage.src = browserCheckImageURL(state.browserCheck); });
elements.browserCheckCancel.addEventListener("click", cancelBrowserCheck);
elements.applicationReset.addEventListener("click", () => { state.applicationFilter = ""; state.applicationQuery = ""; state.applicationSort = "updated_desc"; state.selectedApplications.clear(); state.applicationActionMessage = ""; elements.applicationSearch.value = ""; elements.applicationSort.value = state.applicationSort; elements.applicationBulkAction.value = ""; changeApplicationQuery(); });
elements.conversationSearch.addEventListener("input", () => { state.conversationQuery = elements.conversationSearch.value; clearTimeout(state.conversationSearchTimer); state.conversationSearchTimer = setTimeout(() => refreshConversations(), 250); });
elements.conversationFilter.addEventListener("change", () => { state.conversationFilter = elements.conversationFilter.value; refreshConversations(); });
elements.conversationSort.addEventListener("change", () => { state.conversationSort = elements.conversationSort.value; renderConversations(state.conversationItems); });
const applicationTableScroll = document.querySelector("#applications-section .table-wrap");
if (applicationTableScroll) applicationTableScroll.addEventListener("scroll", () => {
  if (state.applicationLoading || applicationTableScroll.scrollTop + applicationTableScroll.clientHeight < applicationTableScroll.scrollHeight - 240) return;
  if (state.applicationObjects.length >= state.applicationTotal) return;
  refreshApplications({ append: true });
});
elements.conversations.addEventListener("scroll", () => {
  const list = elements.conversations;
  if (state.conversationLoading || list.scrollTop + list.clientHeight < list.scrollHeight - 240) return;
  if (state.conversationItems.length >= state.conversationTotal) return;
  refreshConversations({ append: true });
});
elements.refresh.addEventListener("click", () => { refreshSummary(); refreshProfileResources(); refreshReviewSessions(); });
elements.reviewRefresh.addEventListener("click", () => refreshReviewSessions());
elements.reviewSend.addEventListener("click", sendReviewApplication);
elements.accountSwitcher.addEventListener("change", () => {
  state.account = elements.accountSwitcher.value;
  try { window.localStorage.setItem("job-agent-account", state.account); } catch {}
  state.selectedApplications.clear(); state.applicationOffset = 0; state.applicationTotal = 0;
  state.reviewSelected = null; state.reviewDetail = null;
  refreshSummary(); refreshReviewSessions(); updateCaptchaWarning();
});
elements.reviewFilter.addEventListener("change", () => { state.reviewSelected = null; refreshReviewSessions(); });
let reviewSearchTimer;
elements.reviewSearch.addEventListener("input", () => {
  clearTimeout(reviewSearchTimer);
  reviewSearchTimer = setTimeout(() => {
    state.reviewQuery = elements.reviewSearch.value.trim();
    state.reviewSelected = null;
    refreshReviewSessions();
  }, 250);
});
elements.reviewMore.addEventListener("click", () => refreshReviewSessions({ append: true }));
refreshVersion(); refreshSummary(); refreshConversations(); refreshProfileResources(); refreshReviewSessions(); setInterval(() => { refreshSummary({ background: true }); refreshConversations(); refreshProfileResources(); refreshReviewSessions(); }, 30_000);

// Server-sent change notifications replace most of the polling latency; the
// interval above stays as a safety net.
(() => {
  const stream = new EventSource("/api/v1/events");
  let refreshTimer = 0;
  stream.addEventListener("dashboard", (event) => {
    if (document.hidden) return;
    let sections = [];
    try { sections = (JSON.parse(event.data || "{}").sections) || []; } catch (_) {}
    globalThis.clearTimeout(refreshTimer);
    refreshTimer = globalThis.setTimeout(async () => {
      await refreshSummary({ background: true });
      if (sections.includes("applications")) await refreshApplications();
      if (sections.includes("conversations")) await refreshConversations();
      if (sections.includes("conversations") && state.selectedConversation) {
        try {
          const fresh = await request(`/api/v1/conversations/${encodeURIComponent(state.selectedConversation.id)}/messages`);
          if (state.selectedConversation) { state.selectedMessages = withPendingMessages(fresh.items || [], state.selectedConversation.id); renderMessages(state.selectedMessages); }
        } catch (_) {}
      }
    }, 1200);
  });
  document.addEventListener("visibilitychange", () => { if (!document.hidden) refreshSummary(); });
  stream.addEventListener("error", () => { /* EventSource reconnects on its own */ });
})();

function renderAuthProfileOptions(profiles = []) {
  const select = document.getElementById("auth-profile");
  if (!select) return;
  const items = profiles.filter((item) => item && item.id);
  if (!items.length || select.dataset.ready === "1") return;
  select.replaceChildren(...items.map((item) => {
    const option = document.createElement("option");
    option.value = item.id;
    option.textContent = item.display_name ? `${item.display_name} (${item.id})` : item.id;
    return option;
  }));
  select.dataset.ready = "1";
  if (items.some((item) => item.id === state.account)) select.value = state.account;
}

(() => {
  const startButton = document.getElementById("auth-start");
  if (!startButton) {
    return;
  }
  const profileSelect = document.getElementById("auth-profile");
  const valueInput = document.getElementById("auth-value");
  const valueCaption = document.getElementById("auth-value-caption");
  const submitButton = document.getElementById("auth-submit");
  const cancelButton = document.getElementById("auth-cancel");
  const refreshButton = document.getElementById("auth-refresh");
  const stateLabel = document.getElementById("auth-state");
  const stepLabel = document.getElementById("auth-step");
  const resultLabel = document.getElementById("auth-result");
  const valueRow = document.getElementById("auth-value-row");
  const captchaRow = document.getElementById("auth-captcha-row");
  const captcha = document.getElementById("auth-captcha");
  const captchaRefresh = document.getElementById("auth-captcha-refresh");
  const steps = {
    waiting_identifier: { step: "Введите e-mail или телефон аккаунта HH — платформа отправит код подтверждения.", caption: "E-mail или телефон", type: "text", autocomplete: "username" },
    waiting_otp: { step: "Введите код подтверждения, который прислала платформа.", caption: "Код подтверждения", type: "text", autocomplete: "one-time-code" },
    waiting_password: { step: "Введите пароль от аккаунта HH.", caption: "Пароль", type: "password", autocomplete: "current-password" },
    waiting_captcha: { step: "Введите символы с картинки. Регистр обычно не важен; если не читается — перезагрузите картинку.", caption: "Символы с картинки", type: "text", autocomplete: "off" },
  };
  const statusLabels = { created: "сессия создана", exchanging: "обмен данными с HH", storing: "сохранение сессии", completed: "вход выполнен", expired: "сессия истекла", cancelled: "сессия отменена", failed: "ошибка входа" };
  const idleStep = "Выберите профиль и нажмите «Начать вход» — сервис запросит e-mail или телефон, затем код, пароль или captcha.";
  const terminal = ["completed", "expired", "cancelled", "failed"];
  let sessionId = "";
  let stream = null;

  const setState = (value) => { stateLabel.textContent = value; };
  const setCaptcha = (id) => { captcha.src = `/api/v1/auth/sessions/${encodeURIComponent(id)}/challenge?ts=${Date.now()}`; };

  const renderSession = (session) => {
    sessionId = session.id;
    const step = steps[session.status];
    setState(`${statusLabels[session.status] || session.status} · профиль ${profileDisplayName(session.profile_id)}`);
    stepLabel.hidden = false;
    stepLabel.textContent = step
      ? (session.challenge?.prompt ? `${step.step} Платформа: ${session.challenge.prompt}` : step.step)
      : idleStep;
    valueRow.hidden = !step;
    valueCaption.textContent = step ? step.caption : "Значение";
    valueInput.type = step ? step.type : "text";
    valueInput.autocomplete = step ? step.autocomplete : "off";
    valueInput.disabled = !step;
    submitButton.disabled = !step;
    captchaRow.hidden = session.status !== "waiting_captcha";
    if (session.status === "waiting_captcha") setCaptcha(session.id);
    const finished = terminal.includes(session.status);
    cancelButton.hidden = finished;
    refreshButton.hidden = finished;
    startButton.disabled = !finished && Boolean(sessionId);
    profileSelect.disabled = !finished && Boolean(sessionId);
    resultLabel.hidden = !finished;
    if (session.status === "completed") {
      const target = session.browser_state_reference || session.credential_reference || "";
      resultLabel.textContent = target
        ? `Вход выполнен. Сессия сохранена: ${target}. Профиль готов к работе.`
        : "Вход выполнен. Сессия профиля сохранена.";
      refreshSummary();
      refreshProfileResources();
    } else if (session.status === "failed") {
      resultLabel.textContent = `Вход не выполнен${session.failure_message ? `: ${session.failure_message}` : ""}. Можно начать заново.`;
    } else if (session.status === "expired") {
      resultLabel.textContent = "Сессия истекла. Начните вход заново.";
    } else if (session.status === "cancelled") {
      resultLabel.textContent = "Вход отменён.";
    }
    if (finished) {
      sessionId = "";
      if (stream) { stream.close(); stream = null; }
      profileSelect.disabled = false;
    }
  };

  const subscribe = (id) => {
    if (stream) stream.close();
    stream = new EventSource(`/api/v1/auth/sessions/${encodeURIComponent(id)}/events`);
    stream.addEventListener("session", (message) => {
      const session = JSON.parse(message.data);
      renderSession(session);
      if (terminal.includes(session.status) && stream) { stream.close(); stream = null; }
    });
    stream.addEventListener("error", () => setState("поток прерван, нажмите «Обновить статус»"));
  };

  startButton.addEventListener("click", async () => {
    const profile = profileSelect.value;
    if (!profile) {
      setState("нет доступных профилей — проверьте конфигурацию");
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

  refreshButton.addEventListener("click", async () => {
    if (!sessionId) return;
    const response = await fetch(`/api/v1/auth/sessions/${encodeURIComponent(sessionId)}`);
    if (!response.ok) {
      setState(`ошибка статуса: ${response.status}`);
      return;
    }
    renderSession(await response.json());
  });

  captchaRefresh.addEventListener("click", () => {
    if (sessionId) setCaptcha(sessionId);
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
    const kind = steps[current.status] ? current.status.replace("waiting_", "") : "";
    if (!kind) {
      setState(`сессия не ждёт ввода: ${statusLabels[current.status] || current.status}`);
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
    if (!stream) subscribe(sessionId);
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
