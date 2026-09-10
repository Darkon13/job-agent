const state = { summary: null, selectedConversation: null, jobs: [], applicationObjects: [], applicationFilter: "", applicationQuery: "", markAllReadBusy: false, profileResources: [], profilePlans: new Map(), profileEditors: new Map(), profileMessages: new Map(), profileBusy: new Set(), taskBusy: new Set(), jobBusy: new Set() };
const elements = Object.fromEntries([
  "application-filters", "application-items", "application-filter-state", "application-search", "application-reset", "tasks", "jobs", "campaigns", "failed-tasks", "activity", "activity-observations", "stats", "conversations", "messages", "chat-title", "chat-meta", "chat-vacancy-link",
  "connection-dot", "connection-state", "runtime-version", "updated-at", "refresh", "mark-all-read", "conversation-bulk-state", "reply-form",
  "reply", "send", "action-state",
  "profile-resources", "profile-state-state",
].map((id) => [id.replace(/-([a-z])/g, (_, letter) => letter.toUpperCase()), document.querySelector(`#${id}`)]));
const taskTypeLabels = {
  "vacancy.search_page": "Получить страницу вакансий", "application.campaign": "Запустить кампанию откликов", "application.submit": "Отправить отклик",
  "questionnaire.answer": "Ответить на анкету", "test.complete": "Пройти тест", "test.capture": "Сохранить вопросы теста", "review.answer": "Сохранить проверенный ответ",
  "conversation.reply": "Ответить в чате", "conversation.send": "Отправить сообщение", "conversation.follow_up": "Отправить напоминание", "conversation.follow_up.select": "Выбрать чат для напоминания",
  "conversation.discover": "Обновить список чатов", "conversation.mark_read": "Пометить чат прочитанным", "conversation.sync": "Загрузить сообщения чата", "vacancy.inspect": "Открыть и изучить вакансию",
  "resume.publish": "Опубликовать резюме", "resume.touch": "Поднять резюме", "resume.update": "Обновить резюме", "profile.activity.observe": "Снять показатели активности",
  "profile.bootstrap": "Заполнить профиль", "profile_state.reconcile": "Сверить профиль с конфигурацией", "profile_state.apply": "Применить изменения профиля", "skill_verification.start": "Запустить проверку навыка",
  "calendar.find_slots": "Найти свободное время", "calendar.create_event": "Создать событие", "challenge.respond": "Ответить на проверку", "notification.deliver": "Доставить уведомление",
};
const applicationStatusLabels = {
  new: "Новый", preparing: "Готовится", waiting_validation: "Нужны данные", waiting_approval: "Ждёт подтверждения", ready: "Готов к отправке",
  submitting: "Отправляется", pending_reconciliation: "Проверяется результат", submitted: "Отправлен", dry_run: "Проверочный запуск", skipped: "Пропущен", failed: "Ошибка",
};
const taskStatusLabels = { new: "Ожидает", processing: "Выполняется", waiting_confirmation: "Нужно решение", retry_scheduled: "Повтор запланирован", completed: "Завершена", failed: "Ошибка", dismissed: "Закрыта" };
const campaignStatusLabels = { running: "Выполняется", target_reached: "Цель достигнута", exhausted: "Вакансии закончились", paused_budget: "Пауза: лимит", paused_rate_limit: "Пауза: rate limit", failed: "Ошибка" };
const conversationStatusLabels = { active: "Активный", closed: "Закрыт", rejected: "Отказ", archived: "Архив" };
const activityKindLabels = { "vacancy.inspected": "Просмотрена вакансия", "application.submitted": "Отправлен отклик", "conversation.message_sent": "Отправлено сообщение", "resume.touched": "Поднято резюме" };
const decisionLabels = { qualified: "Подходит", resume_not_suitable: "Резюме не подходит", questionnaire_required: "Нужна анкета", vacancy_test_required: "Нужен тест", vacancy_closed: "Вакансия закрыта", already_applied: "Уже отправлен" };

function text(tag, value, className = "") { const node = document.createElement(tag); node.textContent = value; if (className) node.className = className; return node; }
function statusCell(value, className = "") { const cell = document.createElement("td"); cell.append(text("span", value, `status ${className}`.trim())); return cell; }
function formatDate(value) { return value ? new Intl.DateTimeFormat("ru-RU", { dateStyle: "short", timeStyle: "medium" }).format(new Date(value)) : "—"; }
function taskTypeLabel(value) { return taskTypeLabels[value] || value; }
function applicationStatusLabel(value) { return applicationStatusLabels[value] || value || "—"; }
function taskStatusLabel(value) { return taskStatusLabels[value] || value || "—"; }
function conversationLabel(item) { return item.vacancy_title || `Диалог ${item.platform}`; }
function total(items, predicate = () => true) { return items.filter(predicate).reduce((sum, item) => sum + Number(item.count || 0), 0); }
function safeExternalURL(value) { try { const url = new URL(value); return ["http:", "https:"].includes(url.protocol) ? url.href : ""; } catch (_) { return ""; } }
function compactID(value) { const id = String(value || ""); return id.length > 20 ? `${id.slice(0, 8)}…${id.slice(-6)}` : id || "—"; }

function renderStats(summary = {}) {
  const applications = summary.applications || [];
  const tasks = summary.tasks || [];
  const metrics = [
    { value: total(applications, (item) => item.status === "submitted"), label: "Отклики отправлены", filter: "submitted" },
    { value: total(applications, (item) => ["waiting_validation", "waiting_approval", "pending_reconciliation", "failed"].includes(item.status)), label: "Требуют внимания", filter: "attention" },
    { value: total(tasks, (item) => ["new", "processing", "retry_scheduled", "waiting_confirmation"].includes(item.status)), label: "Работа в очереди" },
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
  if (state.applicationFilter === "attention" && !["waiting_validation", "waiting_approval", "pending_reconciliation", "failed"].includes(item.status)) return false;
  if (state.applicationFilter && state.applicationFilter !== "attention" && item.status !== state.applicationFilter) return false;
  const query = state.applicationQuery.trim().toLocaleLowerCase("ru");
  return !query || [item.vacancy_title, item.employer, item.profile_id, item.decision_code].some((value) => String(value || "").toLocaleLowerCase("ru").includes(query));
}
function setApplicationFilter(value) {
  state.applicationFilter = state.applicationFilter === value ? "" : value;
  renderApplicationFilters(state.summary?.applications || []); renderApplicationObjects();
  document.querySelector("#applications-section")?.scrollIntoView({ behavior: "smooth", block: "start" });
}
function renderApplicationFilters(items = []) {
  const grouped = new Map();
  for (const item of items) grouped.set(item.status, (grouped.get(item.status) || 0) + Number(item.count || 0));
  const attention = total(items, (item) => ["waiting_validation", "waiting_approval", "pending_reconciliation", "failed"].includes(item.status));
  const filters = [["", "Все", total(items)], ...(attention ? [["attention", "Требуют внимания", attention]] : []), ...[...grouped.entries()].map(([status, count]) => [status, applicationStatusLabel(status), count])];
  elements.applicationFilters.replaceChildren(...filters.map(([value, label, count]) => {
    const button = document.createElement("button"); button.type = "button"; button.className = `filter-card${state.applicationFilter === value ? " active" : ""}`;
    button.append(text("strong", String(count)), text("span", label)); button.addEventListener("click", () => setApplicationFilter(value)); return button;
  }));
}
function renderApplicationObjects() {
  const items = state.applicationObjects.filter(applicationMatchesFilter);
  elements.applicationFilterState.textContent = `${items.length} из ${state.applicationObjects.length} последних откликов`;
  if (!items.length) { const row = document.createElement("tr"); const cell = text("td", "Под этот фильтр откликов нет"); cell.colSpan = 7; row.append(cell); elements.applicationItems.replaceChildren(row); return; }
  elements.applicationItems.replaceChildren(...items.map((item) => {
    const row = document.createElement("tr");
    const vacancy = document.createElement("td"); vacancy.append(text("strong", item.vacancy_title || "Без названия"));
    const action = document.createElement("td"); const url = safeExternalURL(item.vacancy_url);
    if (url) { const link = text("a", "Открыть ↗", "table-link"); link.href = url; link.target = "_blank"; link.rel = "noopener noreferrer"; action.append(link); } else action.textContent = "—";
    const decision = decisionLabels[item.decision_code] || item.decision_code || (item.failure_category ? `Ошибка: ${item.failure_category}` : "—");
    row.append(vacancy, text("td", item.employer || "—"), text("td", item.profile_id), statusCell(applicationStatusLabel(item.status), `status-${item.status}`), text("td", decision), text("td", formatDate(item.updated_at)), action);
    return row;
  }));
}
function renderTasks(items = []) {
  const queued = items.filter((item) => !["completed", "dismissed"].includes(item.status));
  if (!queued.length) { const row = document.createElement("tr"); const cell = text("td", "Очередь пуста"); cell.colSpan = 4; row.append(cell); elements.tasks.replaceChildren(row); return; }
  elements.tasks.replaceChildren(...queued.map((item) => {
    const row = document.createElement("tr"); row.append(text("td", taskTypeLabel(item.type)), statusCell(taskStatusLabel(item.status), `task-${item.status}`), text("td", String(item.priority)), text("td", String(item.count))); return row;
  }));
}
function renderJobs(items = []) {
  if (!items.length) { const row = document.createElement("tr"); const cell = text("td", "Нет доступных jobs: проверьте enabled, авторизацию и capabilities профиля"); cell.colSpan = 6; row.append(cell); elements.jobs.replaceChildren(row); return; }
  elements.jobs.replaceChildren(...items.map((item) => {
    const row = document.createElement("tr");
    const action = document.createElement("td");
    const run = text("button", "Запустить", "secondary compact"); run.type = "button"; run.disabled = state.jobBusy.has(item.tag);
    run.addEventListener("click", () => runJob(item)); action.append(run);
    row.append(text("td", item.tag), text("td", taskTypeLabel(item.task_type)), text("td", item.platform), text("td", item.profile_id), text("td", String(item.priority)), action);
    return row;
  }));
}
function renderFailedTasks(items = []) {
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
    const outcomes = (item.applications || []).map((entry) => `${applicationStatusLabel(entry.status)}${entry.decision_code ? ` / ${decisionLabels[entry.decision_code] || entry.decision_code}` : ""}: ${entry.count}`).join(" · ") || "нет откликов";
    const status = item.stop_reason ? `${campaignStatusLabels[item.status] || item.status}: ${item.stop_reason}` : campaignStatusLabels[item.status] || item.status;
    row.append(text("td", item.id, "task-id"), text("td", item.job_tag), text("td", status), text("td", String(item.target_successful)), text("td", outcomes), text("td", formatDate(item.updated_at)));
    return row;
  }));
}
function renderActivity(items = []) {
  if (!items.length) { const row = document.createElement("tr"); const cell = text("td", "Подтверждённых действий пока нет"); cell.colSpan = 5; row.append(cell); elements.activity.replaceChildren(row); return; }
  elements.activity.replaceChildren(...items.map((item) => {
    const row = document.createElement("tr");
    row.append(text("td", item.profile_id), text("td", item.platform), text("td", activityKindLabels[item.kind] || item.kind), text("td", String(item.count)), text("td", formatDate(item.last_occurred_at)));
    return row;
  }));
}
function counter(value) { return value === null || value === undefined ? "—" : String(value); }
function renderActivityObservations(items = []) {
  const latest = [];
  const seen = new Set();
  for (const item of items) {
    const key = `${item.platform}\u0000${item.profile_id}\u0000${item.resume_id}`;
    if (seen.has(key)) continue;
    seen.add(key); latest.push(item);
  }
  if (!latest.length) { elements.activityObservations.replaceChildren(text("p", "Показатели ещё не снимались. Запустите job «Снять показатели активности».", "empty panel")); return; }
  elements.activityObservations.replaceChildren(...latest.map((item) => {
    const card = document.createElement("article"); card.className = "panel activity-card";
    const heading = document.createElement("div"); heading.className = "panel-heading";
    const identity = document.createElement("div"); const resume = text("p", `${item.platform} · резюме ${compactID(item.resume_id)}`, "muted"); resume.title = item.resume_id; identity.append(text("h2", item.profile_id), resume);
    heading.append(identity, text("span", item.period_days === undefined ? "период не указан" : `${item.period_days} дней`, "tag")); card.append(heading);
    const metrics = document.createElement("div"); metrics.className = "activity-metrics";
    [["Показы в поиске", counter(item.search_shows), ""], ["Просмотры", counter(item.views), item.new_views ? `+${item.new_views}` : ""], ["Приглашения", counter(item.invitations), item.new_invitations ? `+${item.new_invitations}` : ""]].forEach(([label, value, delta]) => {
      const metric = document.createElement("div"); metric.append(text("span", label), text("strong", value), delta ? text("small", delta) : document.createTextNode("")); metrics.append(metric);
    });
    card.append(metrics, text("p", `Снято ${formatDate(item.observed_at)}${item.score_hidden ? " · общая шкала скрыта HH" : ""}`, "muted")); return card;
  }));
}
function renderConversations(items = []) {
  updateMarkAllRead(items);
  if (!items.length) { elements.conversations.replaceChildren(text("p", "Диалогов пока нет", "empty")); return; }
  elements.conversations.replaceChildren(...items.map((item) => {
    const button = document.createElement("button"); button.type = "button"; button.className = `conversation${state.selectedConversation?.id === item.id ? " active" : ""}`;
    const heading = document.createElement("span"); heading.className = "conversation-heading"; heading.append(text("strong", conversationLabel(item)));
    if (item.unread_count) heading.append(text("span", String(item.unread_count), "unread-badge"));
    button.append(heading, text("span", item.employer || "Компания не определена", "conversation-employer"), text("small", `${item.profile_id} · ${conversationStatusLabels[item.status] || item.status} · ${formatDate(item.updated_at)}`));
    button.addEventListener("click", () => selectConversation(item)); return button;
  }));
}
function updateMarkAllRead(items = []) {
  const unread = items.reduce((sum, item) => sum + Number(item.unread_count || 0), 0);
  elements.markAllRead.textContent = unread ? `Прочитать все (${unread})` : "Все прочитано";
  elements.markAllRead.disabled = state.markAllReadBusy || unread === 0;
}
function renderMessages(items = []) {
  if (!items.length) { elements.messages.replaceChildren(text("p", "В этом диалоге сообщений пока нет.", "empty")); return; }
  elements.messages.replaceChildren(...items.map((item) => { const article = document.createElement("article"); article.className = `message ${item.direction || ""}`; article.append(text("p", item.text || `[${item.kind}]`), text("time", formatDate(item.occurred_at))); return article; })); elements.messages.scrollTop = elements.messages.scrollHeight;
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
    const actions = document.createElement("div"); actions.className = "resource-actions";
    const status = text("span", state.profileMessages.get(resource.tag) || "", "muted");
    const planButton = text("button", "Построить план"); planButton.type = "button"; planButton.disabled = !resource.readable || state.profileBusy.has(resource.tag);
    planButton.addEventListener("click", () => planProfileState(resource, planButton)); actions.append(status, planButton);
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
    const [summary, failures, jobs, applications] = await Promise.all([request("/api/v1/dashboard/summary"), request("/api/v1/tasks/failed"), request("/api/v1/jobs"), request("/api/v1/applications?limit=100")]);
    state.summary = summary; state.failedTasks = failures.items || []; state.jobs = jobs.items || []; state.applicationObjects = applications.items || [];
    if (state.selectedConversation) state.selectedConversation = (summary.conversations || []).find((item) => item.id === state.selectedConversation.id) || null;
    renderStats(summary); renderApplicationFilters(summary.applications || []); renderApplicationObjects(); renderTasks(summary.tasks || []); renderJobs(state.jobs); renderCampaigns(summary.campaigns || []); renderFailedTasks(state.failedTasks); renderActivity(summary.activity || []); renderActivityObservations(summary.activity_snapshots || []); renderConversations(summary.conversations || []);
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
  try { const result = await request(`/api/v1/conversations/${encodeURIComponent(conversation.id)}/messages`); renderMessages(result.items || []); } catch (error) { elements.messages.replaceChildren(text("p", error.message, "empty")); }
}
function newIdempotencyKey() {
  if (globalThis.crypto?.randomUUID) return globalThis.crypto.randomUUID();
  const suffix = Math.random().toString(36).slice(2);
  return `dashboard-${Date.now()}-${suffix}`;
}
async function enqueue(path, body) { const headers = { "Idempotency-Key": newIdempotencyKey() }; if (body !== undefined) headers["Content-Type"] = "application/json"; return request(path, { method: "POST", headers, body: body === undefined ? undefined : JSON.stringify(body) }); }

elements.replyForm.addEventListener("submit", async (event) => {
  event.preventDefault(); const value = elements.reply.value.trim(); if (!state.selectedConversation || !value) return; elements.send.disabled = true; elements.actionState.textContent = "Создаю задачу…";
  try { const result = await enqueue(`/api/v1/conversations/${encodeURIComponent(state.selectedConversation.id)}/messages`, { content: { text: value } }); elements.reply.value = ""; elements.actionState.textContent = `Задача ${result.task_id} поставлена в очередь`; await refreshSummary(); } catch (error) { elements.actionState.textContent = error.message; } finally { elements.send.disabled = false; }
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
elements.applicationSearch.addEventListener("input", () => { state.applicationQuery = elements.applicationSearch.value; renderApplicationObjects(); });
elements.applicationReset.addEventListener("click", () => { state.applicationFilter = ""; state.applicationQuery = ""; elements.applicationSearch.value = ""; renderApplicationFilters(state.summary?.applications || []); renderApplicationObjects(); });
elements.refresh.addEventListener("click", () => { refreshSummary(); refreshProfileResources(); });
refreshVersion(); refreshSummary(); refreshProfileResources(); setInterval(() => { refreshSummary(); refreshProfileResources(); }, 30_000);
