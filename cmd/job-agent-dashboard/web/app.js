function storedAccount() { try { return window.localStorage.getItem("job-agent-account") || ""; } catch { return ""; } }
const state = {
  account: storedAccount(),
  applicationOffset: 0, applicationTotal: 0, applicationGroups: {}, applicationRequest: 0, applicationLoading: false,
  summary: null, selectedConversation: null, selectedMessages: [], jobs: [], applicationObjects: [],
  applicationFilter: "", applicationQuery: "", applicationSort: "updated_desc", selectedApplications: new Set(), applicationActionBusy: false, applicationActionMessage: "",
  conversationQuery: "", conversationFilter: "", conversationSort: "updated_desc", conversationRequest: 0, conversationReadBusy: new Set(), markAllReadBusy: false, conversationAnswerBusy: "",
  conversationItems: [], conversationTotal: 0, conversationUnreadTotal: 0, conversationLoading: false, conversationSearchTimer: 0, conversationPinnedIndex: 0,
  reviewSendProfiles: new Set(),
  cache: { summary: null, applications: null, conversations: new Map(), reviews: null },
  messageRequest: new Map(), messageSignatures: new Map(),
  localReads: new Map(),
  profileResources: [], profilePlans: new Map(), profileEditors: new Map(), profileRevisions: new Map(), profileMessages: new Map(), profileBusy: new Set(), taskBusy: new Set(), jobBusy: new Set(),
  reviewSessions: [], reviewSelected: null, reviewDetail: null, reviewBusy: false, reviewMessage: "",
  reviewQuery: "", reviewHasMore: false, reviewFocusPending: false,
  browserCheck: null, captchaCheckRemaining: [],
};
const elements = Object.fromEntries([
  "application-filters", "application-items", "application-filter-state", "application-search", "application-sort", "application-reset", "application-select-all", "application-selection-state", "application-selection-bar", "application-remove-selected", "application-clear-selection", "application-bulk-action", "application-run-action", "tasks", "jobs-user", "jobs-system", "jobs-pause-user", "jobs-pause-system", "campaigns", "failed-tasks", "activity", "activity-observations", "stats", "conversations", "conversation-search", "conversation-filter", "conversation-sort", "messages", "chat-title", "chat-meta", "chat-vacancy-link",
  "connection-dot", "connection-state", "runtime-version", "updated-at", "refresh", "mark-all-read", "conversation-bulk-state", "reply-form", "account-switcher",
  "reply", "send", "action-state",
  "profile-resources", "profile-state-state",
  "review-state", "review-filter", "review-search", "review-more", "review-refresh", "review-sessions", "review-session-title", "review-session-meta", "review-prompt", "review-send",
  "browser-check", "browser-check-state", "browser-check-image", "browser-check-answer", "browser-check-submit", "browser-check-refresh-image", "browser-check-cancel",
  "conversation-page-state", "account-captcha", "account-captcha-button", "account-captcha-label", "browser-check-controls",
  "account-auth", "account-auth-button", "account-auth-label",
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
function applicationCanRemove(item) { return ["waiting_validation", "waiting_approval", "submitted", "dry_run", "skipped", "failed", "ready"].includes(item.status); }

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
    state.applicationActionMessage = "";
    const vacancyID = String(item.vacancy_url || "").match(/\/vacancy\/(\d+)/)?.[1] || "";
    await focusReviewSession(vacancyID, item.profile_id);
  } catch (error) {
    state.applicationActionMessage = `Не удалось запросить анкету: ${error.message}`;
  }
  button.disabled = false;
  updateApplicationSelection(state.applicationObjects);
}
// matchingReviewSession finds the captured questionnaire among the listed
// sessions: the vacancy identifies it, the profile disambiguates a vacancy
// captured for several accounts.
function matchingReviewSession(vacancyID, profileID) {
  const sessions = state.reviewSessions || [];
  const candidates = vacancyID ? sessions.filter((session) => reviewVacancyID(session) === vacancyID) : [];
  return candidates.find((session) => session.profile_id === profileID) || candidates[0] || null;
}
// focusReviewSession waits for the asynchronously captured questionnaire and
// selects exactly that card. Without it the review section keeps the first
// session selected and the operator answers the wrong vacancy.
async function focusReviewSession(vacancyID, profileID) {
  if (!vacancyID) {
    refreshReviewSessions();
    document.getElementById("review-title")?.scrollIntoView({ behavior: "smooth", block: "start" });
    return;
  }
  // The captured questionnaire may fall outside the active review filters.
  if (elements.reviewFilter.value || state.reviewQuery) {
    elements.reviewFilter.value = ""; elements.reviewSearch.value = ""; state.reviewQuery = "";
  }
  state.reviewSelected = null; state.reviewFocusPending = true;
  const deadline = Date.now() + 60000;
  try {
    for (;;) {
      await refreshReviewSessions();
      const session = matchingReviewSession(vacancyID, profileID);
      if (session) {
        selectReviewSession(session);
        document.getElementById("review-title")?.scrollIntoView({ behavior: "smooth", block: "start" });
        return;
      }
      if (Date.now() >= deadline) {
        elements.reviewState.textContent = "HH ещё готовит анкету — обновите список проверок";
        return;
      }
      await new Promise((resolve) => setTimeout(resolve, 2000));
    }
  } finally {
    state.reviewFocusPending = false;
    renderReviewSessions();
  }
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
// updateAuthWarning surfaces profiles whose HH session was rejected: the
// operator must sign in again before applications or campaigns can work.
function updateAuthWarning() {
  const warnings = state.summary?.auth_warnings || [];
  const target = warnings.find((item) => item.profile_id === state.account) || warnings[0] || null;
  elements.accountAuth.hidden = !target;
  if (!target) return;
  elements.accountAuth.dataset.profile = target.profile_id;
  const count = Number(target.count || 0);
  elements.accountAuthLabel.textContent = `HH отклонил сессию: ${profileDisplayName(target.profile_id)}${count > 1 ? ` (${count})` : ""} — нужен повторный вход`;
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
// updateApplicationSelection keeps the compact selection bar and the row
// highlight in sync with the selected set.
function updateApplicationSelection(items = visibleApplicationObjects()) {
  const known = new Set(items.map((item) => item.id));
  for (const id of [...state.selectedApplications]) if (!known.has(id)) state.selectedApplications.delete(id);
  const count = state.selectedApplications.size;
  if (elements.applicationSelectionBar) elements.applicationSelectionBar.hidden = count === 0;
  if (elements.applicationSelectionState) elements.applicationSelectionState.textContent = count ? `Выбрано: ${count}` : "Ничего не выбрано";
  if (elements.applicationRemoveSelected) elements.applicationRemoveSelected.disabled = count === 0 || state.applicationActionBusy;
}

async function removeSelectedApplications() {
  const ids = [...state.selectedApplications];
  if (!ids.length || state.applicationActionBusy) return;
  if (!globalThis.confirm(`Убрать выбранные отклики (${ids.length})? Локальные записи и чаты будут удалены.`)) return;
  state.applicationActionBusy = true;
  updateApplicationSelection();
  try {
    const result = await enqueue("/api/v1/applications/remove", { application_ids: ids });
    const failures = (result.results || []).filter((entry) => entry.error);
    state.applicationActionMessage = failures.length
      ? `Не удалось убрать: ${failures.length}`
      : `Создано задач: ${result.created}.`;
    state.selectedApplications.clear();
    await refreshSummary();
    await refreshApplications();
  } catch (error) {
    state.applicationActionMessage = error.message;
  } finally {
    state.applicationActionBusy = false;
    updateApplicationSelection();
  }
}

function renderApplicationObjects() {
  const items = visibleApplicationObjects();
  elements.applicationFilterState.textContent = `${state.applicationTotal ? state.applicationOffset + 1 : 0}–${state.applicationOffset + items.length} из ${state.applicationTotal} по фильтру`;
  updateApplicationProfileColumn();
  if (!items.length) {
    const row = document.createElement("tr");
    const cell = text("td", "Под этот фильтр откликов нет");
    cell.colSpan = 5;
    row.append(cell);
    elements.applicationItems.replaceChildren(row);
    return;
  }
  elements.applicationItems.replaceChildren(...items.map((item) => renderApplicationRow(item)));
  updateApplicationSelection(items);
}

function applicationNeedsInput(item) {
  const validationSkipped = item.status === "skipped" && ["questionnaire_required", "vacancy_test_required", "platform_validation_required"].includes(item.decision_code);
  return validationSkipped || item.status === "waiting_validation";
}

function applicationCanRetry(item) {
  const validationSkipped = item.status === "skipped" && ["questionnaire_required", "vacancy_test_required", "platform_validation_required"].includes(item.decision_code);
  return ["waiting_validation", "failed"].includes(item.status) || validationSkipped;
}

// rowIcon renders a small square icon button for row actions.
function rowIcon(kind, title, handler) {
  const button = text("button", "", `row-icon ${kind}`);
  button.type = "button";
  button.title = title;
  button.setAttribute("aria-label", title);
  button.disabled = state.applicationActionBusy;
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("viewBox", "0 0 16 16");
  svg.setAttribute("width", "13");
  svg.setAttribute("height", "13");
  svg.setAttribute("aria-hidden", "true");
  const path = document.createElementNS("http://www.w3.org/2000/svg", "path");
  path.setAttribute("fill", "none");
  path.setAttribute("stroke", "currentColor");
  path.setAttribute("stroke-width", "1.6");
  path.setAttribute("stroke-linecap", "round");
  path.setAttribute("stroke-linejoin", "round");
  path.setAttribute("d", kind === "retry"
    ? "M13.2 8a5.2 5.2 0 1 1-1.7-3.9M13.4 2v3.2h-3.2"
    : "M4.4 4.4l7.2 7.2M11.6 4.4l-7.2 7.2");
  svg.append(path);
  button.append(svg);
  button.addEventListener("click", (event) => { event.stopPropagation(); handler(); });
  return button;
}

// applicationStatusCell renders the status; a questionnaire that needs the
// operator becomes the call to action itself.
function applicationStatusCell(item) {
  const group = applicationGroup(item);
  const cell = document.createElement("td");
  cell.className = "application-state";
  if (applicationNeedsInput(item) && item.platform === "hh") {
    const button = text("button", "", `status status-${group} status-action`);
    button.type = "button";
    button.disabled = state.applicationActionBusy;
    button.title = "Открыть анкету и ответить на вопросы";
    button.append(text("span", applicationGroupLabels[group]), text("small", "анкета", "status-hint"));
    button.addEventListener("click", (event) => { event.stopPropagation(); captureQuestionnaire(item, button); });
    cell.append(button);
  } else {
    cell.append(text("span", applicationGroupLabels[group], `status status-${group}`));
  }
  const reason = applicationReason(item);
  if (reason) cell.append(text("div", reason, "muted application-reason"));
  return cell;
}

function applicationRowActions(item) {
  const actions = [];
  if (applicationCanRetry(item)) {
    actions.push(rowIcon("retry", "Повторить подготовку и отправку отклика", () => retryApplication(item)));
  }
  if (applicationCanRemove(item)) {
    actions.push(rowIcon("remove", "Убрать отклик из рабочего списка", () => removeApplication(item)));
  }
  return actions;
}

// renderApplicationRow keeps the row narrow: the vacancy link and company share
// one cell, the status carries the reason, and clicking the row toggles its
// selection for the bulk actions.
function renderApplicationRow(item) {
  const row = document.createElement("tr");
  row.className = "application-row";
  if (state.selectedApplications.has(item.id)) row.classList.add("selected");
  row.addEventListener("click", (event) => {
    if (event.target.closest("a, button, input, label, select")) return;
    if (state.selectedApplications.has(item.id)) state.selectedApplications.delete(item.id);
    else state.selectedApplications.add(item.id);
    row.classList.toggle("selected", state.selectedApplications.has(item.id));
    updateApplicationSelection();
  });

  const url = safeExternalURL(item.vacancy_url);
  const vacancy = document.createElement("td");
  vacancy.className = "application-vacancy";
  if (url) {
    const link = text("a", item.vacancy_title || "Без названия", "application-vacancy-link");
    link.href = url;
    link.target = "_blank";
    link.rel = "noopener noreferrer";
    vacancy.append(link);
  } else {
    vacancy.append(text("strong", item.vacancy_title || "Без названия"));
  }
  vacancy.append(text("div", item.employer || "Компания не определена", "muted"));

  const profileCell = document.createElement("td");
  profileCell.className = "application-profile";
  profileCell.append(text("strong", profileDisplayName(item.profile_id)));
  profileCell.append(text("small", item.profile_id, "muted"));

  const actions = document.createElement("td");
  actions.className = "task-actions";
  const buttons = applicationRowActions(item);
  if (buttons.length) actions.append(...buttons);
  else actions.textContent = "—";

  row.append(vacancy, profileCell, applicationStatusCell(item), text("td", formatDate(item.updated_at)), actions);
  return row;
}

async function removeApplication(item) {
  const title = item.vacancy_title || item.employer || item.id;
  if (!globalThis.confirm(`Убрать «${title}» из рабочего списка? Локальная запись и чат будут удалены.`)) return;
  state.selectedApplications.delete(item.id);
  state.applicationActionBusy = true;
  renderApplicationObjects();
  try {
    const result = await enqueue("/api/v1/applications/remove", { application_ids: [item.id] });
    const failures = (result.results || []).filter((entry) => entry.error);
    state.applicationActionMessage = failures.length
      ? `Не удалось убрать: ${failures[0].error}`
      : `Задача на удаление создана (${result.created}).`;
    await refreshSummary();
    await refreshApplications();
  } catch (error) {
    state.applicationActionMessage = error.message;
  } finally {
    state.applicationActionBusy = false;
    renderApplicationObjects();
  }
}

// The profile column is redundant while one account is selected: every row
// belongs to it. It returns with the combined "all profiles" view.
function updateApplicationProfileColumn() {
  const table = document.getElementById("application-table");
  if (table) table.classList.toggle("profile-hidden", Boolean(state.account));
}
function renderTasks(items = []) {
  if (!items.length) { const row = document.createElement("tr"); const cell = text("td", "Очередь пуста"); cell.colSpan = 6; row.append(cell); elements.tasks.replaceChildren(row); return; }
  elements.tasks.replaceChildren(...items.map((item) => {
    const row = document.createElement("tr");
    const actions = document.createElement("td");
    actions.className = "task-actions";
    actions.append(rowIcon("cancel", "Отменить задачу: она не будет выполнена", () => cancelTask(item)));
    const profile = text("td", item.profile_id ? profileDisplayName(item.profile_id) : "—");
    const availability = document.createElement("td");
    const countdown = text("span", "", "countdown");
    countdown.dataset.nextRun = item.available_at;
    availability.append(countdown);
    row.append(
      text("td", taskTypeLabel(item.type)),
      statusCell(taskStatusLabel(item.status), `task-${item.status}`),
      profile,
      availability,
      text("td", String(item.attempts)),
      actions,
    );
    return row;
  }));
}

// cancelTask removes a queued task from the execution queue; running and
// finished tasks are rejected by the backend.
async function cancelTask(item) {
  if (!globalThis.confirm(`Отменить задачу «${taskTypeLabel(item.type)}»? Она не будет выполнена.`)) return;
  try {
    await enqueue(`/api/v1/tasks/${encodeURIComponent(item.id)}/cancel`, { reason: "cancelled from the dashboard" });
    elements.connectionState.textContent = `Задача «${taskTypeLabel(item.type)}» отменена`;
    await refreshSummary();
  } catch (error) {
    elements.connectionState.textContent = error.message;
  }
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
// jobCronLabel turns the common cron shapes into a short human cadence, for
// example "каждый час"; the raw expression stays in the tooltip.
function jobCronLabel(expression) {
  const parts = String(expression || "").trim().split(/\s+/);
  if (parts.length !== 5) return "";
  const [minute, hour, , , weekday] = parts;
  if (minute === "0" && hour === "*") return "каждый час";
  if (/^\*\/\d+$/.test(minute) && hour === "*") return `каждые ${minute.slice(2)} мин`;
  if (/^\d+$/.test(minute) && hour === "*") return `каждый час в :${minute.padStart(2, "0")}`;
  if (/^\d+$/.test(minute) && /^\*\/\d+$/.test(hour)) return `каждые ${hour.slice(2)} ч в :${minute.padStart(2, "0")}`;
  if (/^\d+$/.test(minute) && /^\d+$/.test(hour)) {
    const time = `${hour.padStart(2, "0")}:${minute.padStart(2, "0")}`;
    return weekday === "*" ? `каждый день в ${time}` : `по дням недели ${weekday} в ${time}`;
  }
  return "";
}

function jobScheduleLines(item) {
  const seen = new Set();
  const lines = [];
  for (const schedule of item.schedules || []) {
    // Interval schedules are timers ("каждые 30 мин"); cron schedules get a
    // human cadence plus the raw expression and timezone.
    const cadence = schedule.interval
      ? `каждые ${formatDuration(schedule.interval)}`
      : [jobCronLabel(schedule.expression), schedule.expression, schedule.timezone].filter(Boolean).join(" · ");
    const parts = [cadence];
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
// jobCountdownNode shows only the remaining time; the schedule itself lives in
// the tooltip behind the question mark.
function jobCountdownNode(item) {
  const runs = (item.schedules || []).map((schedule) => schedule.next_run_at).filter(Boolean).sort();
  if (!runs.length) return text("span", "вручную", "muted");
  const value = text("span", "", "countdown");
  value.dataset.nextRun = runs[0];
  return value;
}

function jobScheduleTitle(item) {
  const lines = jobScheduleLines(item);
  return lines.length ? lines.join("; ") : "запускается только вручную";
}

function jobPausedProfiles(item) {
  return new Set((item.pauses || []).map((entry) => entry.profile_id).filter(Boolean));
}

// jobActivityBadge counts bound profiles and how many of them are not paused,
// for example 1/3 when one of three profiles is paused.
function jobActivityBadge(item) {
  const profiles = jobProfiles(item);
  const paused = jobPausedProfiles(item);
  const active = profiles.filter((profileID) => !paused.has(profileID)).length;
  const badge = text("span", `${active}/${profiles.length}`, "job-badge");
  badge.title = `активных профилей: ${active} из ${profiles.length}`;
  if (profiles.length && active === 0) badge.classList.add("paused");
  return badge;
}

function jobPauseNotes(item) {
  const reasons = [...new Set((item.pauses || []).map((entry) => entry.reason === "auth_required" ? "требуется вход" : "пауза оператора"))];
  const names = [...new Set((item.pauses || []).map((entry) => profileDisplayName(entry.profile_id)))];
  return `${reasons.join(", ")}${names.length ? ` (${names.join(", ")})` : ""}`;
}

// jobStateButton renders OK/PAUSED and toggles the pause for one profile or for
// the whole job when no profile is given.
const lockIconPath = "M5.2 7V5.6a2.8 2.8 0 0 1 5.6 0V7h.6c.6 0 1 .4 1 1v4.4c0 .6-.4 1-1 1H4.6c-.6 0-1-.4-1-1V8c0-.6.4-1 1-1h.6zm1.4 0h2.8V5.6a1.4 1.4 0 0 0-2.8 0V7z";

function lockIcon() {
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("viewBox", "0 0 16 16");
  svg.setAttribute("width", "11");
  svg.setAttribute("height", "11");
  svg.setAttribute("aria-hidden", "true");
  const path = document.createElementNS("http://www.w3.org/2000/svg", "path");
  path.setAttribute("d", lockIconPath);
  path.setAttribute("fill", "currentColor");
  svg.append(path);
  return svg;
}

// jobStateControl returns the state button and, for a read-only system state, a
// separate lock icon next to it.
function jobStateControl(item, profileID = "") {
  const paused = profileID ? jobPausedProfiles(item).has(profileID) : Boolean(item.paused);
  const button = jobStateButton(item, profileID);
  if (!item.system || paused) return [button];
  const lock = text("span", "", "job-lock");
  lock.title = "состояние задаёт сервис: системная джоба встаёт на паузу сама (например, при разлогине)";
  lock.setAttribute("aria-label", "переключение недоступно");
  lock.append(lockIcon());
  return [button, lock];
}

function jobStateButton(item, profileID = "") {
  const paused = profileID ? jobPausedProfiles(item).has(profileID) : Boolean(item.paused);
  const button = text("button", paused ? "PAUSED" : "OK", `job-state ${paused ? "paused" : "ok"}`);
  button.type = "button";
  if (item.system && !paused) {
    button.disabled = true;
    button.title = "состояние задаёт сервис: системная джоба встаёт на паузу сама (например, при разлогине)";
    button.setAttribute("aria-label", "OK, переключение недоступно");
  } else {
    button.disabled = state.jobBusy.has(item.tag);
    button.title = paused
      ? `снять паузу${profileID ? ` для ${profileDisplayName(profileID)}` : ""}`
      : `поставить на паузу${profileID ? ` для ${profileDisplayName(profileID)}` : " для всех профилей"}`;
    button.addEventListener("click", (event) => { event.stopPropagation(); toggleJobPause(item, !paused, profileID); });
  }
  if (!profileID && paused && item.pauses?.length) button.title += `: ${jobPauseNotes(item)}`;
  return button;
}

const playIconPath = "M4.5 3.1v9.8l8.4-4.9z";

function jobRunButton(item, profileID = "") {
  const button = text("button", "", "job-run");
  button.type = "button";
  button.disabled = state.jobBusy.has(item.tag);
  button.title = profileID ? `запустить для ${profileDisplayName(profileID)}` : "запустить для всех профилей";
  button.setAttribute("aria-label", button.title);
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("viewBox", "0 0 16 16");
  svg.setAttribute("width", "14");
  svg.setAttribute("height", "14");
  svg.setAttribute("aria-hidden", "true");
  const path = document.createElementNS("http://www.w3.org/2000/svg", "path");
  path.setAttribute("d", playIconPath);
  path.setAttribute("fill", "currentColor");
  svg.append(path);
  button.append(svg);
  button.addEventListener("click", (event) => { event.stopPropagation(); runJob(item, profileID); });
  return button;
}

function toggleJobExpanded(tag) {
  if (state.expandedJobs.has(tag)) state.expandedJobs.delete(tag);
  else state.expandedJobs.add(tag);
  renderJobs(state.jobs);
}

function renderJobs(items = []) {
  items = state.account ? items.filter((item) => jobProfiles(item).includes(state.account)) : items;
  const groups = [
    { system: false, body: elements.jobsUser, button: elements.jobsPauseUser, key: "group:user" },
    { system: true, body: elements.jobsSystem, button: elements.jobsPauseSystem, key: "group:system" },
  ];
  for (const group of groups) {
    const groupItems = items.filter((item) => Boolean(item.system) === group.system).sort((left, right) => String(left.tag).localeCompare(String(right.tag)));
    const pausedCount = groupItems.filter((item) => item.paused).length;
    if (group.system) {
      // System jobs are paused by the service itself; the operator only gets a
      // recovery action when something already paused them.
      group.button.hidden = pausedCount === 0;
      group.button.textContent = "Снять паузу со всех";
      group.button.dataset.paused = "1";
      group.button.disabled = state.jobBusy.has(group.key);
    } else {
      group.button.textContent = pausedCount === groupItems.length && groupItems.length ? "Снять паузу со всех" : "Поставить все на паузу";
      group.button.disabled = state.jobBusy.has(group.key) || groupItems.length === 0;
      group.button.dataset.paused = pausedCount === groupItems.length && groupItems.length ? "1" : "";
    }
    if (!groupItems.length) {
      const row = document.createElement("tr"); const cell = text("td", "Нет доступных jobs: проверьте enabled, авторизацию и capabilities профиля"); cell.colSpan = 4; row.append(cell);
      group.body.replaceChildren(row);
      continue;
    }
    group.body.replaceChildren(...groupItems.map((item) => renderJobRow(item)).flat());
  }
  updateCountdowns();
}
// renderJobRow renders the compact job line; clicking it reveals the
// per-profile details. The returned slice holds the detail row when the job is
// expanded.
function renderJobRow(item) {
    const row = document.createElement("tr");
    const expanded = state.expandedJobs.has(item.tag);
    row.className = expanded ? "job-row expanded" : "job-row";
    row.setAttribute("aria-expanded", String(expanded));
    row.addEventListener("click", () => toggleJobExpanded(item.tag));

    const jobCell = document.createElement("td");
    const title = document.createElement("div");
    title.className = "job-title";
    title.append(text("span", item.description || item.tag));
    if (jobProfiles(item).length > 1) title.append(jobActivityBadge(item));
    jobCell.append(title);
    const subtitle = [item.description ? item.tag : "", taskTypeLabel(item.task_type)].filter(Boolean).join(" · ");
    if (subtitle) jobCell.append(text("small", subtitle, "muted"));

    const scheduleCell = document.createElement("td");
    scheduleCell.className = "job-schedule-cell";
    scheduleCell.append(jobCountdownNode(item));
    if (jobScheduleLines(item).length) {
      const help = text("span", "?", "job-help");
      help.title = jobScheduleTitle(item);
      help.setAttribute("aria-label", `расписание: ${jobScheduleTitle(item)}`);
      scheduleCell.append(help);
    }

    const stateCell = document.createElement("td");
    stateCell.append(...jobStateControl(item));

    const runCell = document.createElement("td");
    runCell.className = "job-run-cell";
    runCell.append(jobRunButton(item));

    row.append(jobCell, scheduleCell, stateCell, runCell);
    if (!expanded) return [row];
    return [row, ...renderJobDetailRows(item)];
}
// renderJobDetailRows shows the per-profile lines inside one cell: the info
// stays on the left and the state and run controls on the right, so the parent
// row keeps its columns and the block reads as part of the same job.
function renderJobDetailRows(item) {
  const commands = Array.isArray(item.commands) ? item.commands : [];
  const profiles = jobProfiles(item);
  const row = document.createElement("tr");
  row.className = "job-detail-row";
  const cell = document.createElement("td");
  cell.colSpan = 4;
  const list = document.createElement("div");
  list.className = "job-detail-list";
  for (const profileID of profiles) {
    const line = document.createElement("div");
    line.className = "job-detail-line";
    const info = document.createElement("div");
    info.className = "job-detail-info-cell";
    info.append(text("strong", profileDisplayName(profileID)));
    const command = commands.find((entry) => entry.profile_id === profileID);
    const summary = command ? jobParameterSummary({ payload: command.payload }) : "";
    if (summary) info.append(text("div", summary, "muted job-detail-info"));
    const actions = document.createElement("div");
    actions.className = "job-detail-actions";
    actions.append(...jobStateControl(item, profileID), jobRunButton(item, profileID));
    line.append(info, actions);
    list.append(line);
  }
  if (!profiles.length) list.append(text("div", "нет привязанных профилей", "muted"));
  list.append(text("div", `расписание: ${jobScheduleTitle(item)}`, "muted job-detail-schedule"));
  cell.append(list);
  row.append(cell);
  return [row];
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
  // The panel is informational: only the last few runs stay on the main page.
  const visible = items.slice(0, 5);
  elements.campaigns.replaceChildren(...visible.map((item) => {
    const row = document.createElement("tr");
    const grouped = new Map();
    for (const entry of item.applications || []) { const group = applicationGroup(entry); grouped.set(group, (grouped.get(group) || 0) + Number(entry.count || 0)); }
    const outcomes = [...grouped.entries()].map(([group, count]) => `${applicationGroupLabels[group]}: ${count}`).join(" · ") || "нет откликов";
    const status = item.stop_reason ? `${campaignStatusLabels[item.status] || item.status}: ${item.stop_reason}` : campaignStatusLabels[item.status] || item.status;
    row.append(text("td", item.id, "task-id"), text("td", item.job_tag), text("td", status), text("td", String(item.target_successful)), text("td", outcomes), text("td", formatDate(item.updated_at)));
    return row;
  }));
}
// profileCatalogIdentity renders the cached account facts of a profile.
function profileCatalogIdentity(entry) {
  const identity = entry.identity;
  if (!identity) return "аккаунт ещё не входил";
  const parts = [identity.display_name, identity.email, identity.phone].map(plainText).filter(Boolean);
  if (identity.account_hash) parts.push(`аккаунт ${identity.account_hash}`);
  return parts.length ? parts.join(" · ") : "данные аккаунта пусты";
}

// profileCatalogResumes renders the declared resume list of a profile.
function profileCatalogResumes(entry) {
  const resumes = entry.resumes || [];
  if (!resumes.length) return "резюме нет";
  return resumes.map((resume) => {
    const label = resume.title ? `${resume.title} (${compactID(resume.id)})` : compactID(resume.id);
    return resume.primary ? `${label} — основное` : label;
  }).join(", ");
}

// profileSnapshots returns the activity snapshots of one profile, newest first.
function profileSnapshots(profileID) {
  return (state.summary?.activity_snapshots || [])
    .filter((item) => item.profile_id === profileID)
    .sort((left, right) => String(right.observed_at || "").localeCompare(String(left.observed_at || "")));
}

function profileFacts(profileID) {
  return (state.summary?.activity || []).filter((item) => item.profile_id === profileID);
}

function profileLatestSnapshot(profileID) {
  return profileSnapshots(profileID)[0] || null;
}

// profileScoreColor maps the activity score to a red-to-green hue so the
// colour scale stays readable for any percentage.
function profileScoreColor(score) {
  const bounded = Math.max(0, Math.min(100, Number(score) || 0));
  return `hsl(${Math.round(bounded * 1.35)} 70% 62%)`;
}

function profileMetric(label, value, delta = "", color = "") {
  const block = document.createElement("div");
  block.className = "metric";
  block.append(text("span", label, "metric-label"));
  const row = document.createElement("div");
  row.className = "metric-value";
  const strong = text("strong", value);
  if (color) strong.style.color = color;
  row.append(strong);
  if (delta) row.append(text("small", delta, "metric-delta"));
  block.append(row);
  return block;
}

// profileMetrics renders the collected gauges as labelled counters; the
// detailed snapshots live in the expanded row.
function profileMetrics(profileID) {
  const snapshot = profileLatestSnapshot(profileID);
  if (!snapshot) return text("span", "нет данных", "muted");
  const grid = document.createElement("div");
  grid.className = "metric-grid";
  if (typeof snapshot.score === "number") {
    grid.append(profileMetric("активность", `${snapshot.score}%`, "", profileScoreColor(snapshot.score)));
  }
  grid.append(profileMetric("просмотры", counter(snapshot.views), snapshot.new_views ? `+${snapshot.new_views}` : ""));
  grid.append(profileMetric("приглашения", counter(snapshot.invitations), snapshot.new_invitations ? `+${snapshot.new_invitations}` : ""));
  if (snapshot.search_shows !== null && snapshot.search_shows !== undefined) {
    grid.append(profileMetric("показы", String(snapshot.search_shows)));
  }
  return grid;
}

function profileRowSession(session) {
  if (!session || !session.present) return "нет";
  return session.modified_at ? `есть (${formatDate(session.modified_at)})` : "есть";
}

function profileResumesSummary(entry) {
  const resumes = entry.resumes || [];
  if (!resumes.length) return "нет";
  const primary = resumes.find((resume) => resume.primary) || resumes[0];
  const label = primary.title || compactID(primary.id);
  return resumes.length > 1 ? `${label} +${resumes.length - 1}` : label;
}

// renderProfiles merges the config catalog, the collected metrics and the
// activity facts into one expandable row per profile.
function renderProfiles() {
  const container = document.getElementById("profile-rows");
  if (!container) return;
  let entries = state.profileCatalog || [];
  if (state.account) entries = entries.filter((entry) => entry.tag === state.account);
  const stateLabel = document.getElementById("profiles-state");
  if (stateLabel) stateLabel.textContent = entries.length ? `Профилей: ${entries.length}` : "Профилей нет";
  if (!entries.length) {
    const row = document.createElement("tr");
    const cell = text("td", "Профили не объявлены. Добавьте первый в блоке ниже.", "muted");
    cell.colSpan = 5;
    row.append(cell);
    container.replaceChildren(row);
    return;
  }
  container.replaceChildren(...entries.map((entry) => renderProfileRow(entry)).flat());
}

function renderProfileRow(entry) {
  const row = document.createElement("tr");
  const expanded = state.expandedProfiles.has(entry.tag);
  row.className = expanded ? "profile-row expanded" : "profile-row";
  row.setAttribute("aria-expanded", String(expanded));
  row.addEventListener("click", () => toggleProfileExpanded(entry.tag));

  const profile = document.createElement("td");
  const heading = document.createElement("div");
  heading.className = "job-title";
  heading.append(text("span", profileDisplayName(entry.tag)));
  if (!entry.enabled) heading.append(text("span", "выключен", "job-badge"));
  profile.append(heading);
  profile.append(text("small", `${entry.tag} · ${entry.adapter || "—"}/${entry.platform || "—"}`, "muted"));

  const resumes = document.createElement("td");
  resumes.className = "profile-resumes";
  resumes.append(text("span", profileResumesSummary(entry)));

  const metrics = document.createElement("td");
  metrics.className = "profile-metrics";
  metrics.append(profileMetrics(entry.tag));

  const session = document.createElement("td");
  session.append(text("span", profileRowSession(entry.session)));

  const source = document.createElement("td");
  source.append(text("span", entry.source || "config", "muted"));

  row.append(profile, resumes, metrics, session, source);
  if (!expanded) return [row];
  return [row, renderProfileDetail(entry)];
}

function renderProfileDetail(entry) {
  const row = document.createElement("tr");
  row.className = "profile-detail-row";
  const cell = document.createElement("td");
  cell.colSpan = 5;

  const identity = document.createElement("div");
  identity.className = "profile-detail-line";
  identity.append(text("strong", "Аккаунт"));
  identity.append(text("div", profileCatalogIdentity(entry), "muted"));
  cell.append(identity);

  const resumes = document.createElement("div");
  resumes.className = "profile-detail-line";
  resumes.append(text("strong", "Резюме"));
  resumes.append(text("div", profileCatalogResumes(entry), "muted"));
  cell.append(resumes);

  const snapshots = profileSnapshots(entry.tag);
  if (snapshots.length) {
    // The history is long; three recent samples are enough for a trend.
    const recent = snapshots.slice(0, 3);
    cell.append(profileDetailTable("Снимки HH", ["Снято", "Окно", "Активность", "Показы", "Просмотры", "Приглашения"],
      recent.map((item) => [
        formatDate(item.observed_at),
        item.period_days === undefined || item.period_days === null ? "—" : `${item.period_days} д`,
        item.score === undefined || item.score === null ? "—" : `${item.score}%`,
        counter(item.search_shows),
        item.new_views ? `${counter(item.views)} (+${item.new_views})` : counter(item.views),
        item.new_invitations ? `${counter(item.invitations)} (+${item.new_invitations})` : counter(item.invitations),
      ])));
    if (snapshots.length > recent.length) {
      cell.append(text("div", `показаны последние ${recent.length} из ${snapshots.length} снимков`, "muted profile-detail-note"));
    }
  }

  const facts = profileFacts(entry.tag);
  if (facts.length) {
    cell.append(profileDetailTable("Подтверждённые действия профиля", ["Действие", "Платформа", "Количество", "Последнее"],
      facts.map((item) => [activityKindLabels[item.kind] || item.kind, item.platform, String(item.count), formatDate(item.last_occurred_at)])));
  }

  const jobs = (state.jobs || []).filter((item) => jobProfiles(item).includes(entry.tag));
  if (jobs.length) {
    const line = document.createElement("div");
    line.className = "profile-detail-line";
    line.append(text("strong", "Джобы профиля"));
    const list = document.createElement("div");
    list.className = "profile-detail-jobs";
    for (const job of jobs) {
      const item = document.createElement("div");
      item.className = "profile-detail-job";
      item.append(text("span", job.description || job.tag));
      const paused = jobPausedProfiles(job).has(entry.tag);
      item.append(text("span", paused ? "PAUSED" : "OK", `job-state job-state-static ${paused ? "paused" : "ok"}`));
      item.append(jobCountdownNode(job));
      list.append(item);
    }
    line.append(list);
    cell.append(line);
  }

  row.append(cell);
  return row;
}

function profileDetailTable(caption, headers, rows) {
  const block = document.createElement("div");
  block.className = "profile-detail-line";
  block.append(text("strong", caption));
  const wrap = document.createElement("div");
  wrap.className = "table-wrap";
  const table = document.createElement("table");
  const head = document.createElement("thead");
  const headRow = document.createElement("tr");
  headRow.append(...headers.map((label) => text("th", label)));
  head.append(headRow);
  const body = document.createElement("tbody");
  for (const values of rows) {
    const tr = document.createElement("tr");
    tr.append(...values.map((value) => text("td", String(value ?? "—"))));
    body.append(tr);
  }
  table.append(head, body);
  wrap.append(table);
  block.append(wrap);
  return block;
}

function toggleProfileExpanded(tag) {
  if (state.expandedProfiles.has(tag)) state.expandedProfiles.delete(tag);
  else state.expandedProfiles.add(tag);
  renderProfiles();
}
function counter(value) { return value === null || value === undefined ? "—" : String(value); }
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
  if (append && state.conversationLoading) return;
  // Non-append refreshes are sequenced instead of dropped: a filter change must
  // not be swallowed by an in-flight poll, and an older response must never
  // overwrite a newer list.
  const requestID = (state.conversationRequest || 0) + 1;
  state.conversationRequest = requestID;
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
    if (state.conversationRequest !== requestID) return;
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
    if (state.conversationRequest === requestID) {
      state.conversationTotal = state.conversationItems.length;
      elements.conversationBulkState.textContent = error.message;
    }
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

// messageSignature identifies a rendered message list so an unchanged refresh
// leaves the panel alone instead of rebuilding it under the operator's eyes.
function messageSignature(items) {
  return (items || []).map((item) => `${item.id}:${item.status || ""}:${(item.text || "").length}`).join("|");
}
// loadConversationMessages refreshes the open chat through one ordered path:
// a response that lost the race with a newer request never touches the panel.
async function loadConversationMessages(conversationID, { live = false } = {}) {
  const requestID = (state.messageRequest.get(conversationID) || 0) + 1;
  state.messageRequest.set(conversationID, requestID);
  const result = await request(`/api/v1/conversations/${encodeURIComponent(conversationID)}/messages${live ? "?live=1" : ""}`);
  if (state.selectedConversation?.id !== conversationID) return null;
  if (state.messageRequest.get(conversationID) !== requestID) return null;
  const items = withPendingMessages(result.items || [], conversationID);
  state.selectedMessages = items;
  const signature = messageSignature(items);
  if (state.messageSignatures.get(conversationID) !== signature) {
    state.messageSignatures.set(conversationID, signature);
    renderMessages(items);
  }
  return items;
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
  if (state.draftLabels?.has(profileID)) return state.draftLabels.get(profileID);
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
    if (!item || !item.id) continue;
    // The resume count tells two accounts of the same person apart without
    // opening the profile section.
    const count = Number(item.resumes) || 0;
    labels.set(item.id, `${item.display_name || item.id}${count ? ` · ${count} резюме` : ""}`);
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
  if (state.reviewLoading) return;
  state.reviewLoading = true;
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
    elements.reviewState.textContent = state.reviewSessions.length
      ? `Анкет: ${state.reviewSessions.length}${state.reviewHasMore ? "+" : ""}`
      : "Анкет нет";
    renderReviewSessions();
  } catch (error) {
    elements.reviewState.textContent = error.message;
    elements.reviewSessions.replaceChildren(text("p", "Не удалось загрузить проверки.", "empty panel"));
  } finally {
    state.reviewLoading = false;
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
  const external = session.vacancy?.external_id;
  if (external) return String(external);
  const platform = String(session.platform || "");
  const definition = String(session.test_definition_id || "");
  const prefix = `${platform}:vacancy:`;
  if (platform && definition.startsWith(prefix)) return definition.slice(prefix.length);
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
    // While a captured questionnaire is still being looked up, keep the list
    // unselected instead of jumping to the top card.
    if (state.reviewFocusPending) return;
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

// toggleJobPause pauses or resumes a job: paused schedules stop creating tasks
// and resume with an immediate run.
// toggleJobGroupPause pauses or resumes every job of a group.
async function toggleJobGroupPause(key, group, paused) {
  state.jobBusy.add(key); renderJobs(state.jobs);
  try {
    const result = await enqueue(`/api/v1/jobs/${paused ? "pause" : "resume"}?group=${encodeURIComponent(group)}`);
    elements.connectionState.textContent = `${group === "system" ? "Системные" : "Пользовательские"} джобы: ${result.paused ? "на паузе" : "снова выполняются"} (затронуто: ${result.affected})`;
    await refreshSummary();
  } catch (error) {
    elements.connectionState.textContent = error.message;
  } finally {
    state.jobBusy.delete(key); renderJobs(state.jobs);
  }
}
elements.jobsPauseUser.addEventListener("click", () => toggleJobGroupPause("group:user", "user", !elements.jobsPauseUser.dataset.paused));
elements.jobsPauseSystem.addEventListener("click", () => toggleJobGroupPause("group:system", "system", !elements.jobsPauseSystem.dataset.paused));
async function toggleJobPause(job, paused, profileID = "") {
  state.jobBusy.add(job.tag); renderJobs(state.jobs);
  try {
    const query = profileID ? `?profile_id=${encodeURIComponent(profileID)}` : "";
    const result = await enqueue(`/api/v1/jobs/${encodeURIComponent(job.tag)}/${paused ? "pause" : "resume"}${query}`);
    const scope = result.profile_id ? ` (${profileDisplayName(result.profile_id)})` : "";
    elements.connectionState.textContent = `Job ${job.tag}${scope}: ${result.paused ? "на паузе" : "снова выполняется"}`;
    await refreshSummary();
  } catch (error) {
    elements.connectionState.textContent = error.message;
  } finally {
    state.jobBusy.delete(job.tag); renderJobs(state.jobs);
  }
}
async function runJob(job, profileID = "") {
  state.jobBusy.add(job.tag); renderJobs(state.jobs);
  try {
    const query = profileID ? `?profile_id=${encodeURIComponent(profileID)}` : "";
    const result = await enqueue(`/api/v1/jobs/${encodeURIComponent(job.tag)}/runs${query}`);
    const scope = profileID ? ` (${profileDisplayName(profileID)})` : "";
    elements.connectionState.textContent = `Job ${job.tag}${scope}: задача ${result.task_id} поставлена в очередь`;
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
    renderConfigState(state.summary.config_status); renderStats(state.summary); renderTasks(state.queuedTasks || []);
    renderCampaigns(state.summary.campaigns || []); renderProfiles();
  }
  try {
    const [summary, failures, jobs, queued] = await Promise.all([request("/api/v1/dashboard/summary"), request("/api/v1/tasks/failed"), request("/api/v1/jobs"), request("/api/v1/tasks/queued?limit=100"), refreshApplications()]);
    state.summary = summary; state.cache.summary = summary; state.failedTasks = failures.items || []; state.jobs = jobs.items || []; state.queuedTasks = queued.items || [];
    renderAccountSwitcher(summary.profiles || []);
    renderAuthProfileOptions(summary.profiles || []);
    refreshProfileCatalog();
    refreshDrafts();
    updateCaptchaWarning();
    updateAuthWarning();
    renderConfigState(summary.config_status); renderStats(summary); renderApplicationFilters(state.applicationObjects); renderApplicationObjects(); renderTasks(state.queuedTasks || []); renderJobs(state.jobs); renderCampaigns(summary.campaigns || []); renderFailedTasks(state.failedTasks); renderProfiles(); updateMarkAllRead(state.conversationItems);
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
  state.messageSignatures.delete(conversation.id);
  elements.reply.disabled = false; elements.send.disabled = false; elements.messages.replaceChildren(text("p", "Загрузка…", "empty"));
  try {
    await loadConversationMessages(conversation.id, { live: true });
  } catch (error) { elements.messages.replaceChildren(text("p", error.message, "empty")); }
  try {
    const sync = await enqueue(`/api/v1/conversations/${encodeURIComponent(conversation.id)}/sync`);
    if (sync.created) {
      globalThis.setTimeout(async () => {
        if (state.selectedConversation?.id !== conversation.id) return;
        try {
          await loadConversationMessages(conversation.id);
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
          const items = await loadConversationMessages(conversationID);
          if (items && items.some((item) => item.direction === "outgoing" && item.text === value)) return;
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
// Enter sends the reply; Shift+Enter keeps the newline.
elements.reply.addEventListener("keydown", (event) => {
  if (event.key !== "Enter" || event.shiftKey || event.isComposing) return;
  event.preventDefault();
  elements.replyForm.requestSubmit();
});
elements.accountAuthButton.addEventListener("click", () => {
  const profile = elements.accountAuth.dataset.profile || "";
  if (profile) {
    const select = document.getElementById("auth-profile");
    if (select) select.value = profile;
  }
  document.getElementById("auth-section")?.scrollIntoView({ behavior: "smooth", block: "start" });
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
elements.accountCaptchaButton.addEventListener("click", () => startCaptchaCheck(elements.accountCaptcha.dataset.profile || state.account));
elements.browserCheckSubmit.addEventListener("click", submitBrowserCheckAnswer);
elements.browserCheckAnswer.addEventListener("keydown", (event) => { if (event.key === "Enter") submitBrowserCheckAnswer(); });
elements.browserCheckRefreshImage.addEventListener("click", () => { if (state.browserCheck) elements.browserCheckImage.src = browserCheckImageURL(state.browserCheck); });
elements.browserCheckCancel.addEventListener("click", cancelBrowserCheck);
elements.applicationRemoveSelected?.addEventListener("click", removeSelectedApplications);
elements.applicationClearSelection?.addEventListener("click", () => { state.selectedApplications.clear(); updateApplicationSelection(); renderApplicationObjects(); });
elements.applicationReset.addEventListener("click", () => { state.applicationFilter = ""; state.applicationQuery = ""; state.applicationSort = "updated_desc"; state.applicationActionMessage = ""; elements.applicationSearch.value = ""; elements.applicationSort.value = state.applicationSort; changeApplicationQuery(); });
elements.conversationSearch.addEventListener("input", () => { state.conversationQuery = elements.conversationSearch.value; clearTimeout(state.conversationSearchTimer); state.conversationSearchTimer = setTimeout(() => refreshConversations(), 250); });
elements.conversationFilter.addEventListener("change", () => { state.conversationFilter = elements.conversationFilter.value; refreshConversations(); });
elements.conversationSort.addEventListener("change", () => { state.conversationSort = elements.conversationSort.value; renderConversations(state.conversationItems); });
const applicationTableScroll = document.querySelector("#applications-section .table-wrap");
if (applicationTableScroll) applicationTableScroll.addEventListener("scroll", () => {
  if (state.applicationLoading || applicationTableScroll.scrollTop + applicationTableScroll.clientHeight < applicationTableScroll.scrollHeight - 240) return;
  if (state.applicationObjects.length >= state.applicationTotal) return;
  refreshApplications({ append: true });
});
// The review list loads the next page when the operator scrolls to the bottom,
// the same way the applications and conversations lists do.
elements.reviewSessions.addEventListener("scroll", () => {
  const list = elements.reviewSessions;
  if (!state.reviewHasMore || state.reviewLoading) return;
  if (list.scrollTop + list.clientHeight < list.scrollHeight - 240) return;
  refreshReviewSessions({ append: true });
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
refreshVersion(); refreshSummary(); refreshConversations(); refreshProfileResources(); refreshReviewSessions(); setInterval(() => { refreshSummary({ background: true }); refreshConversations(); refreshProfileResources(); refreshReviewSessions(); }, 30_000);
// The conversation list drives unread work, so it polls on its own faster
// cadence; the SSE above only shortens the latency further.
setInterval(() => { if (!document.hidden) refreshConversations(); }, 10_000);

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
        try { await loadConversationMessages(state.selectedConversation.id); } catch (_) {}
      }
    }, 1200);
  });
  document.addEventListener("visibilitychange", () => { if (!document.hidden) { refreshSummary(); refreshConversations(); } });
  stream.addEventListener("error", () => { /* EventSource reconnects on its own */ });
})();

// The profile onboarding state mirrors the backend drafts: the active draft
// drives the shared login wizard, the primary map remembers the resume choice.
if (!state.draftLabels) state.draftLabels = new Map();
if (!state.draftPrimary) state.draftPrimary = new Map();
if (!state.drafts) state.drafts = [];
if (!state.activeDraft) state.activeDraft = "";
if (!state.profileCatalog) state.profileCatalog = [];
if (!state.expandedJobs) state.expandedJobs = new Set();
if (!state.expandedProfiles) state.expandedProfiles = new Set();

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
      if (state.activeDraft && state.activeDraft === session.profile_id) {
        finishDraftSession(session.profile_id);
      }
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

  // beginAuth starts one interactive session and hands it to the shared
  // wizard. The profile onboarding block calls it for a draft tag, so both
  // entry points drive the same steps.
  const beginAuth = async (profile) => {
    if (!profile) {
      setState("нет доступных профилей — проверьте конфигурацию");
      return false;
    }
    setState("создание сессии…");
    const response = await fetch("/api/v1/auth/sessions", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ platform: "hh", profile_id: profile }),
    });
    if (!response.ok) {
      setState(`ошибка создания сессии: ${response.status}`);
      return false;
    }
    const session = await response.json();
    renderSession(session);
    subscribe(session.id);
    return true;
  };
  state.startAuthSession = beginAuth;
  startButton.addEventListener("click", () => beginAuth(profileSelect.value));

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
  // A backend restart or a transient failure must not leave the operator with
  // a permanently broken captcha: retry the image a few times on its own.
  let captchaAttempts = 0;
  captcha.addEventListener("load", () => { captchaAttempts = 0; });
  captcha.addEventListener("error", () => {
    if (captchaRow.hidden || !sessionId || captchaAttempts >= 5) return;
    captchaAttempts += 1;
    globalThis.setTimeout(() => {
      if (!captchaRow.hidden && sessionId) setCaptcha(sessionId);
    }, 2_000 * captchaAttempts);
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

// --- профили: каталог и онбординг через черновики -------------------------

async function refreshProfileCatalog() {
  try {
    const payload = await request("/api/v1/profiles");
    state.profileCatalog = payload.items || [];
  } catch (_) {
    return;
  }
  renderProfiles();
}

function draftIdentityLabel(draft) {
  if (!draft.identity) return "";
  const parts = [draft.identity.display_name, draft.identity.email, draft.identity.phone].map(plainText).filter(Boolean);
  if (draft.identity.account_hash) parts.push(`аккаунт ${draft.identity.account_hash}`);
  return parts.join(" · ");
}

function draftStatusLabel(status) {
  return { pending: "ожидает входа", ready: "готов к сохранению", applied: "сохранён (нужен перезапуск)" }[status] || status || "—";
}

function renderDrafts() {
  const container = document.getElementById("draft-rows");
  if (!container) return;
  const drafts = state.drafts || [];
  if (!drafts.length) {
    const row = document.createElement("tr");
    const cell = text("td", "Черновиков нет.", "muted");
    cell.colSpan = 4;
    row.append(cell);
    container.replaceChildren(row);
    return;
  }
  const rows = [];
  for (const draft of drafts) {
    const row = document.createElement("tr");
    row.className = "draft-row";
    row.append(text("td", draft.tag));
    row.append(text("td", draftStatusLabel(draft.status)));
    row.append(text("td", draft.identity ? draftIdentityLabel(draft) || "данные пусты" : "—"));
    const actions = document.createElement("td");
    actions.className = "task-actions";
    if (draft.status !== "applied") {
      const login = text("button", draft.status === "pending" ? "Продолжить вход" : "Войти заново", "secondary compact");
      login.type = "button";
      login.addEventListener("click", (event) => { event.stopPropagation(); startDraftSession(draft.tag); });
      actions.append(login);
      const refresh = text("button", "Снять данные", "secondary compact");
      refresh.type = "button";
      refresh.addEventListener("click", (event) => { event.stopPropagation(); captureDraft(draft.tag); });
      actions.append(refresh);
    }
    if (draft.status === "ready") {
      const save = text("button", "Сохранить профиль");
      save.type = "button";
      save.addEventListener("click", (event) => { event.stopPropagation(); applyDraft(draft.tag); });
      actions.append(save);
    }
    const remove = text("button", "Удалить", "secondary compact");
    remove.type = "button";
    remove.addEventListener("click", (event) => { event.stopPropagation(); deleteDraft(draft.tag); });
    actions.append(remove);
    row.append(actions);
    rows.push(row);

    const resumes = draft.resumes || [];
    if (draft.status === "ready" && resumes.length) {
      const detail = document.createElement("tr");
      detail.className = "draft-detail-row";
      const cell = document.createElement("td");
      cell.colSpan = 4;
      const restartLabel = document.createElement("label");
      restartLabel.className = "draft-restart";
      const restartBox = document.createElement("input");
      restartBox.type = "checkbox";
      restartBox.id = `draft-restart-${draft.tag}`;
      restartBox.checked = true;
      restartLabel.append(restartBox, text("span", "перезапустить сервис после сохранения (профиль подключится сразу)"));
      cell.append(restartLabel);
      cell.append(text("span", "Основное резюме: ", "muted"));
      for (const resume of resumes) {
        const label = document.createElement("label");
        label.className = "draft-resume";
        const radio = document.createElement("input");
        radio.type = "radio";
        radio.name = `draft-primary-${draft.tag}`;
        radio.value = resume.id;
        radio.checked = (state.draftPrimary.get(draft.tag) || resumes[0].id) === resume.id;
        radio.addEventListener("change", () => state.draftPrimary.set(draft.tag, resume.id));
        label.append(radio, text("span", resume.title ? `${resume.title} (${resume.id})` : resume.id));
        cell.append(label);
      }
      detail.append(cell);
      rows.push(detail);
    }
  }
  container.replaceChildren(...rows);
}

function setDraftStep(message) {
  const element = document.getElementById("draft-step");
  if (element) element.textContent = message;
}

async function refreshDrafts() {
  try {
    const payload = await request("/api/v1/profile-drafts");
    state.drafts = payload.items || [];
  } catch (_) {
    return;
  }
  renderDrafts();
}

async function startDraftSession(tag) {
  if (!tag) {
    const input = document.getElementById("draft-tag");
    tag = (input?.value || "").trim().toLowerCase();
  }
  if (!/^[a-z0-9][a-z0-9_-]{0,63}$/.test(tag)) {
    setDraftStep("Тег должен быть коротким: строчные латинские буквы, цифры, дефис или подчёркивание.");
    return;
  }
  state.draftLabels.set(tag, tag);
  state.activeDraft = tag;
  setDraftStep(`создаю сессию для «${tag}»…`);
  const started = typeof state.startAuthSession === "function" ? await state.startAuthSession(tag) : false;
  if (!started) {
    setDraftStep(`не удалось начать вход для «${tag}»; попробуйте ещё раз.`);
    state.activeDraft = "";
    return;
  }
  setDraftStep("вход начат: отвечайте на шаги в блоке «Вход в HH».");
  const section = document.getElementById("auth-section");
  if (section) section.scrollIntoView({ behavior: "smooth", block: "start" });
}

async function captureDraft(tag) {
  setDraftStep(`снимаю данные аккаунта «${tag}»…`);
  try {
    await request(`/api/v1/profile-drafts/${encodeURIComponent(tag)}/refresh`, { method: "POST" });
    setDraftStep("данные аккаунта обновлены.");
  } catch (error) {
    setDraftStep(`не удалось снять данные: ${error.message}`);
  }
  await refreshDrafts();
}

async function finishDraftSession(tag) {
  state.activeDraft = "";
  setDraftStep(`вход выполнен, снимаю данные аккаунта «${tag}»…`);
  try {
    await request(`/api/v1/profile-drafts/${encodeURIComponent(tag)}/refresh`, { method: "POST" });
    setDraftStep("вход выполнен: проверьте имя, выберите основное резюме и сохраните профиль.");
  } catch (error) {
    setDraftStep(`вход выполнен, но данные аккаунта не снялись: ${error.message}. Нажмите «Снять данные» в черновике.`);
  }
  await refreshDrafts();
  const container = document.getElementById("draft-rows");
  if (container) container.scrollIntoView({ behavior: "smooth", block: "center" });
}

async function applyDraft(tag) {
  const primary = state.draftPrimary.get(tag) || "";
  const restartBox = document.getElementById(`draft-restart-${tag}`);
  const restart = !restartBox || restartBox.checked;
  setDraftStep(`сохраняю профиль «${tag}»…`);
  let applied = null;
  try {
    applied = await request(`/api/v1/profile-drafts/${encodeURIComponent(tag)}/apply`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ primary_resume: primary, restart }),
    });
  } catch (error) {
    setDraftStep(`не удалось сохранить профиль: ${error.message}`);
    return;
  }
  await refreshDrafts();
  await refreshProfileCatalog();
  if (!applied.restart_scheduled) {
    setDraftStep(`профиль «${tag}» сохранён в profile-store. Перезапустите backend, чтобы он подключился.`);
    return;
  }
  setDraftStep(`профиль «${tag}» сохранён, backend перезапускается…`);
  await waitForBackendRestart(tag);
}

// waitForBackendRestart polls the version endpoint while the supervisor starts
// the process again, then refreshes every view that depends on the profile.
async function waitForBackendRestart(tag) {
  const started = Date.now();
  while (Date.now() - started < 120_000) {
    await new Promise((resolve) => globalThis.setTimeout(resolve, 2_000));
    try {
      const version = await request("/api/v1/version");
      if (version && version.version) {
        setDraftStep(`backend снова доступен (${version.version}); профиль «${tag}» подключён.`);
        await refreshSummary();
        await refreshProfileCatalog();
        await refreshDrafts();
        return;
      }
    } catch (_) { /* the backend is still down, keep waiting */ }
  }
  setDraftStep("backend не поднялся за 2 минуты — проверьте контейнер и логи.");
}

async function deleteDraft(tag) {
  if (!globalThis.confirm(`Удалить черновик «${tag}»? Файл сессии останется на диске.`)) return;
  try {
    await request(`/api/v1/profile-drafts/${encodeURIComponent(tag)}`, { method: "DELETE" });
    setDraftStep(`черновик «${tag}» удалён.`);
  } catch (error) {
    setDraftStep(`не удалось удалить черновик: ${error.message}`);
  }
  await refreshDrafts();
}

(() => {
  const startButton = document.getElementById("draft-start");
  if (!startButton) return;
  const tagInput = document.getElementById("draft-tag");
  const refreshButton = document.getElementById("profiles-refresh");
  startButton.addEventListener("click", async () => {
    const tag = (tagInput?.value || "").trim().toLowerCase();
    if (!/^[a-z0-9][a-z0-9_-]{0,63}$/.test(tag)) {
      setDraftStep("Тег должен быть коротким: строчные латинские буквы, цифры, дефис или подчёркивание.");
      return;
    }
    setDraftStep(`создаю черновик «${tag}»…`);
    try {
      await request("/api/v1/profile-drafts", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ tag }),
      });
    } catch (error) {
      setDraftStep(`не удалось создать черновик: ${error.message}`);
      return;
    }
    if (tagInput) tagInput.value = "";
    await refreshDrafts();
    await startDraftSession(tag);
  });
  refreshButton?.addEventListener("click", () => { refreshProfileCatalog(); refreshDrafts(); });
  refreshProfileCatalog();
  refreshDrafts();
})();
