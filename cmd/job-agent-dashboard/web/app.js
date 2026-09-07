const state = { summary: null, selectedConversation: null, profileResources: [], profilePlans: new Map(), profileEditors: new Map(), profileMessages: new Map(), profileBusy: new Set() };
const elements = Object.fromEntries([
  "applications", "tasks", "stats", "conversations", "messages", "chat-title", "chat-meta",
  "connection-dot", "connection-state", "updated-at", "refresh", "mark-read", "reply-form",
  "reply", "send", "action-state",
  "profile-resources", "profile-state-state",
].map((id) => [id.replace(/-([a-z])/g, (_, letter) => letter.toUpperCase()), document.querySelector(`#${id}`)]));
const statLabels = [["vacancies", "Вакансии"], ["applications", "Отклики"], ["application_campaigns", "Кампании"], ["tasks", "Задачи"], ["profile_state_proposals", "Планы профиля"], ["conversations", "Диалоги"], ["messages", "Сообщения"], ["follow_ups", "Follow-up"]];

function text(tag, value, className = "") { const node = document.createElement(tag); node.textContent = value; if (className) node.className = className; return node; }
function formatDate(value) { return value ? new Intl.DateTimeFormat("ru-RU", { dateStyle: "short", timeStyle: "medium" }).format(new Date(value)) : "—"; }
function conversationLabel(item) { return `${item.platform} · ${item.profile_id}`; }

function renderStats(stats = {}) {
  elements.stats.replaceChildren(...statLabels.map(([key, label]) => { const card = document.createElement("article"); card.className = "stat-card"; card.append(text("strong", String(stats[key] ?? 0)), text("span", label)); return card; }));
}
function renderRows(target, items, fields) {
  if (!items.length) { const row = document.createElement("tr"); const cell = text("td", "Пока нет данных"); cell.colSpan = fields.length; row.append(cell); target.replaceChildren(row); return; }
  target.replaceChildren(...items.map((item) => { const row = document.createElement("tr"); fields.forEach((field, index) => { const cell = document.createElement("td"); const value = item[field] || (field === "decision_code" ? "—" : "0"); cell.append(index < fields.length - 1 ? text("span", String(value), "status") : document.createTextNode(String(value))); row.append(cell); }); return row; }));
}
function renderConversations(items = []) {
  if (!items.length) { elements.conversations.replaceChildren(text("p", "Диалогов пока нет", "empty")); return; }
  elements.conversations.replaceChildren(...items.map((item) => { const button = document.createElement("button"); button.type = "button"; button.className = `conversation${state.selectedConversation?.id === item.id ? " active" : ""}`; button.append(text("strong", conversationLabel(item)), text("small", `${item.status} · ${formatDate(item.updated_at)}`)); button.addEventListener("click", () => selectConversation(item)); return button; }));
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

async function refreshSummary() {
  elements.refresh.disabled = true; elements.connectionState.textContent = "Обновление…"; elements.connectionDot.className = "dot pending";
  try {
    const summary = await request("/api/v1/dashboard/summary"); state.summary = summary;
    renderStats(summary.stats); renderRows(elements.applications, summary.applications || [], ["status", "decision_code", "count"]); renderRows(elements.tasks, summary.tasks || [], ["type", "status", "count"]); renderConversations(summary.conversations || []);
    elements.updatedAt.textContent = `Обновлено ${formatDate(summary.generated_at)}`; elements.connectionState.textContent = "Backend доступен"; elements.connectionDot.className = "dot ok";
  } catch (error) { elements.connectionState.textContent = error.message; elements.connectionDot.className = "dot error"; }
  finally { elements.refresh.disabled = false; }
}
async function selectConversation(conversation) {
  state.selectedConversation = conversation; renderConversations(state.summary?.conversations || []); elements.chatTitle.textContent = conversationLabel(conversation); elements.chatMeta.textContent = `${conversation.status} · revision ${conversation.revision}`; elements.markRead.disabled = false; elements.reply.disabled = false; elements.send.disabled = false; elements.messages.replaceChildren(text("p", "Загрузка…", "empty"));
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
elements.markRead.addEventListener("click", async () => {
  if (!state.selectedConversation) return; elements.markRead.disabled = true; elements.actionState.textContent = "Создаю задачу…";
  try { const result = await enqueue(`/api/v1/conversations/${encodeURIComponent(state.selectedConversation.id)}/mark-read`); elements.actionState.textContent = `Задача ${result.task_id} поставлена в очередь`; await refreshSummary(); } catch (error) { elements.actionState.textContent = error.message; } finally { elements.markRead.disabled = false; }
});
elements.refresh.addEventListener("click", () => { refreshSummary(); refreshProfileResources(); });
refreshSummary(); refreshProfileResources(); setInterval(() => { refreshSummary(); refreshProfileResources(); }, 30_000);
