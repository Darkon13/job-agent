const state = { summary: null, selectedConversation: null };
const elements = Object.fromEntries([
  "applications", "tasks", "stats", "conversations", "messages", "chat-title", "chat-meta",
  "connection-dot", "connection-state", "updated-at", "refresh", "mark-read", "reply-form",
  "reply", "send", "action-state",
].map((id) => [id.replace(/-([a-z])/g, (_, letter) => letter.toUpperCase()), document.querySelector(`#${id}`)]));
const statLabels = [["vacancies", "Вакансии"], ["applications", "Отклики"], ["application_campaigns", "Кампании"], ["tasks", "Задачи"], ["conversations", "Диалоги"], ["messages", "Сообщения"], ["follow_ups", "Follow-up"]];

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
elements.refresh.addEventListener("click", refreshSummary);
refreshSummary(); setInterval(refreshSummary, 30_000);
