/* Spool web console — vanilla JS, no frameworks, no external assets. */
'use strict';

const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));

const state = {
  chats: [],
  projects: [],
  config: null,
  chat: null,
  project: '',
  streaming: false,
  nerdOpen: false,
  nerdMessage: null,
  pending: null,
  queued: null,
  slashIndex: -1,
};

const COMMANDS = [
  ['/status', 'Project and container status'],
  ['/health', 'HTTP readiness probes'],
  ['/diagnose', 'Explain the last failure'],
  ['/explain', 'Dependency topology and ports'],
  ['/logs', 'Recent container logs'],
  ['/env', 'Environment variables'],
  ['/env set', 'Set KEY=VALUE'],
  ['/env unset', 'Unset a variable'],
  ['/heal', 'Repair the stack'],
  ['/up', 'Start the stack'],
  ['/down', 'Stop the stack'],
  ['/restart', 'Recreate and verify'],
  ['/nuke', 'Destroy the project'],
  ['/tunnel', 'Public preview URL'],
  ['/onboard', 'Onboard a repository'],
  ['/scan', 'Scan a repository'],
  ['/projects', 'List projects'],
  ['/config', 'Show configuration'],
  ['/config set', 'Set a config key'],
  ['/doctor', 'Environment checks'],
  ['/version', 'Spool version'],
  ['/open', 'Show live URLs'],
  ['/help', 'Help'],
  ['/new', 'New chat'],
  ['/clear', 'New chat'],
  ['/dash', 'Toggle nerd stats'],
];

let els = {};

document.addEventListener('DOMContentLoaded', init);

async function init() {
  els = {
    app: $('#app'),
    thread: $('#thread'),
    empty: $('#empty'),
    input: $('#input'),
    send: $('#send'),
    status: $('#status-line'),
    chatList: $('#chat-list'),
    chatTitle: $('#chat-title'),
    projectChip: $('#project-chip'),
    projectSelect: $('#project-select'),
    projectMeta: $('#project-meta'),
    modelChip: $('#model-chip'),
    nerd: $('#nerd'),
    nerdBody: $('#nerd-body'),
    nerdToggle: $('#nerd-toggle'),
    nerdClose: $('#nerd-close'),
    themeToggle: $('#theme-toggle'),
    modal: $('#modal'),
    modalText: $('#modal-text'),
    modalArgs: $('#modal-args'),
    modalConfirm: $('#modal-confirm'),
    modalCancel: $('#modal-cancel'),
    toast: $('#toast'),
    slash: $('#slash-menu'),
  };
  applyTheme(readTheme(), false);
  bind();
  autoresize();
  try {
    await Promise.all([loadConfig(), loadProjects()]);
    await loadChats();
    if (state.chats.length) {
      await loadChat(state.chats[0].id);
    } else {
      await newChat();
    }
  } catch (err) {
    toast(err.message);
  }
  setInterval(() => { loadProjects().catch(() => {}); }, 6000);
}

function bind() {
  $('#new-chat').addEventListener('click', () => newChat().catch((e) => toast(e.message)));
  $('#help-btn').addEventListener('click', () => send('/help'));
  els.send.addEventListener('click', () => send(els.input.value));
  els.input.addEventListener('keydown', onInputKey);
  els.input.addEventListener('input', () => { autoresize(); updateSlash(); });
  els.modalConfirm.addEventListener('click', confirmPending);
  els.modalCancel.addEventListener('click', () => hideModal());
  els.modal.addEventListener('click', (e) => { if (e.target === els.modal) hideModal(); });
  els.nerdToggle.addEventListener('click', () => toggleNerd());
  els.nerdClose.addEventListener('click', () => toggleNerd(false));
  els.themeToggle.addEventListener('click', () => {
    applyTheme(document.documentElement.dataset.theme === 'dark' ? 'light' : 'dark');
  });
  els.projectSelect.addEventListener('change', () => setProject(els.projectSelect.value));
  $$('.prompts button').forEach((b) => b.addEventListener('click', () => {
    els.input.value = b.dataset.prompt || '';
    els.input.focus();
    updateSlash();
  }));
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') {
      if (!els.modal.hidden) { hideModal(); return; }
      if (state.nerdOpen) { toggleNerd(false); return; }
      els.slash.hidden = true;
    }
    if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
      e.preventDefault();
      newChat().catch((err) => toast(err.message));
    }
  });
}

/* ------------------------------------------------------------------ API */
async function api(path, opts = {}) {
  const res = await fetch(path, {
    headers: { 'Content-Type': 'application/json', ...(opts.headers || {}) },
    ...opts,
  });
  if (res.status === 204) return null;
  if (!res.ok) {
    let message = res.statusText;
    try { const body = await res.json(); if (body && body.error) message = body.error; } catch (_) {}
    throw new Error(message || `HTTP ${res.status}`);
  }
  return res.json();
}

/* --------------------------------------------------------------- helpers */
function escapeHTML(value) {
  return String(value).replace(/[&<>"']/g, (c) => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
  }[c]));
}

function relTime(iso) {
  if (!iso) return '';
  const then = new Date(iso).getTime();
  if (!then) return '';
  const sec = Math.max(1, Math.floor((Date.now() - then) / 1000));
  if (sec < 60) return `${sec}s ago`;
  const min = Math.floor(sec / 60);
  if (min < 60) return `${min}m ago`;
  const hr = Math.floor(min / 60);
  if (hr < 24) return `${hr}h ago`;
  return `${Math.floor(hr / 24)}d ago`;
}

function fmtDuration(ns) {
  if (!ns || ns < 1000) return ns ? `${ns}ns` : '';
  if (ns < 1e6) return `${(ns / 1e3).toFixed(0)}µs`;
  if (ns < 1e9) return `${(ns / 1e6).toFixed(1)}ms`;
  return `${(ns / 1e9).toFixed(2)}s`;
}

function fmtBytes(n) {
  if (!n) return '0';
  if (n < 1024) return `${n}B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)}KB`;
  return `${(n / (1024 * 1024)).toFixed(1)}MB`;
}

function autoresize() {
  els.input.style.height = 'auto';
  els.input.style.height = `${Math.min(els.input.scrollHeight, 200)}px`;
}

function scrollBottom() {
  requestAnimationFrame(() => { els.thread.scrollTop = els.thread.scrollHeight; });
}

function setStatus(text) {
  els.status.textContent = text || '';
}

function toast(message) {
  els.toast.textContent = message;
  els.toast.hidden = false;
  clearTimeout(toast._timer);
  toast._timer = setTimeout(() => { els.toast.hidden = true; }, 3800);
}

/* --------------------------------------------------------------- markdown */
function inlineMarkdown(line) {
  const codes = [];
  let s = line.replace(/`([^`]+)`/g, (_, code) => {
    codes.push(`<code>${code}</code>`);
    return `\u0000IC${codes.length - 1}\u0000`;
  });
  s = s.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>');
  s = s.replace(/(^|[\s(])\*([^*\n]+)\*(?=[\s).,!?;:]|$)/g, '$1<em>$2</em>');
  s = s.replace(/\[([^\]]+)\]\((https?:\/\/[^)\s]+)\)/g,
    '<a href="$2" target="_blank" rel="noreferrer noopener">$1</a>');
  return s.replace(/\u0000IC(\d+)\u0000/g, (_, i) => codes[Number(i)]);
}

function renderMarkdown(text) {
  const lines = String(text || '').replace(/\r\n/g, '\n').split('\n');
  const out = [];
  let code = null;
  let codeLang = '';
  let list = [];
  let listTag = 'ul';

  const flushList = () => {
    if (!list.length) return;
    out.push(`<${listTag}>${list.join('')}</${listTag}>`);
    list = [];
  };

  for (const raw of lines) {
    const line = raw;
    const trimmed = line.trim();
    if (trimmed.startsWith('```')) {
      flushList();
      if (!code) {
        code = [];
        codeLang = trimmed.slice(3).trim();
      } else {
        out.push(`<pre><code class="language-${escapeHTML(codeLang)}">${escapeHTML(code.join('\n'))}</code></pre>`);
        code = null;
        codeLang = '';
      }
      continue;
    }
    if (code) { code.push(line); continue; }

    if (/^#{1,3}\s+/.test(trimmed)) {
      flushList();
      const level = trimmed.match(/^#+/)[0].length;
      out.push(`<h${level}>${inlineMarkdown(escapeHTML(trimmed.replace(/^#+\s+/, '')))}</h${level}>`);
      continue;
    }
    if (/^[-*+]\s+/.test(trimmed)) {
      if (listTag !== 'ul') { flushList(); listTag = 'ul'; }
      list.push(`<li>${inlineMarkdown(escapeHTML(trimmed.replace(/^[-*+]\s+/, '')))}</li>`);
      continue;
    }
    if (/^\d+\.\s+/.test(trimmed)) {
      if (listTag !== 'ol') { flushList(); listTag = 'ol'; }
      list.push(`<li>${inlineMarkdown(escapeHTML(trimmed.replace(/^\d+\.\s+/, '')))}</li>`);
      continue;
    }
    flushList();
    if (/^>\s?/.test(trimmed)) {
      out.push(`<blockquote>${inlineMarkdown(escapeHTML(trimmed.replace(/^>\s?/, '')))}</blockquote>`);
      continue;
    }
    if (/^(-{3,}|\*{3,}|_{3,})$/.test(trimmed)) { out.push('<hr>'); continue; }
    if (trimmed === '') { out.push(''); continue; }
    out.push(`<p>${inlineMarkdown(escapeHTML(line))}</p>`);
  }
  flushList();
  if (code) out.push(`<pre><code>${escapeHTML(code.join('\n'))}</code></pre>`);
  return `<div class="md">${out.join('\n')}</div>`;
}

/* --------------------------------------------------------------- projects */
async function loadProjects() {
  state.projects = await api('/api/projects');
  if (!state.project) {
    state.project = state.chat?.project || state.projects[0]?.name || '';
  }
  const current = state.project;
  els.projectSelect.innerHTML = '';
  const none = document.createElement('option');
  none.value = '';
  none.textContent = state.projects.length ? '— no project —' : 'No projects yet';
  els.projectSelect.appendChild(none);
  for (const p of state.projects) {
    const opt = document.createElement('option');
    opt.value = p.name;
    opt.textContent = p.name;
    els.projectSelect.appendChild(opt);
  }
  els.projectSelect.value = state.projects.some((p) => p.name === current) ? current : '';
  state.project = els.projectSelect.value;
  updateProjectUI();
}

function updateProjectUI() {
  const project = state.projects.find((p) => p.name === state.project);
  els.projectChip.textContent = state.project || '';
  els.projectChip.classList.toggle('hidden', !state.project);
  if (!project) {
    els.projectMeta.textContent = state.projects.length ? 'No project selected' : 'Onboard a repo to begin';
    return;
  }
  if (project.total > 0) {
    const health = project.healthy ? 'healthy' : 'degraded';
    els.projectMeta.textContent = `${project.running}/${project.total} running · ${health}`;
  } else {
    els.projectMeta.textContent = `${project.services} service(s) · ${project.infra} infra`;
  }
}

function setProject(name) {
  state.project = name || '';
  if (state.chat) state.chat.project = state.project;
  updateProjectUI();
}

async function loadConfig() {
  try {
    state.config = await api('/api/config');
    const label = `${state.config.provider || 'agent'}/${state.config.model || '—'}`;
    els.modelChip.textContent = label;
    els.modelChip.classList.toggle('warn', !state.config.key_configured && state.config.provider !== 'ollama');
  } catch (_) {
    els.modelChip.textContent = 'agent';
  }
}

/* ---------------------------------------------------------------- chats */
async function loadChats() {
  state.chats = await api('/api/chats');
  renderChatList();
}

function renderChatList() {
  els.chatList.innerHTML = '';
  for (const chat of state.chats) {
    const item = document.createElement('button');
    item.type = 'button';
    item.className = `chat-item${state.chat && state.chat.id === chat.id ? ' active' : ''}`;
    const title = document.createElement('span');
    title.className = 'chat-item-title';
    title.textContent = chat.title || 'New chat';
    const meta = document.createElement('span');
    meta.className = 'chat-item-meta';
    meta.textContent = `${chat.project || 'no project'} · ${relTime(chat.updated_at)}`;
    const del = document.createElement('button');
    del.type = 'button';
    del.className = 'chat-delete';
    del.textContent = '✕';
    del.title = 'Delete chat';
    del.addEventListener('click', async (ev) => {
      ev.stopPropagation();
      if (!window.confirm('Delete this chat?')) return;
      try {
        await api(`/api/chats/${chat.id}`, { method: 'DELETE' });
        if (state.chat && state.chat.id === chat.id) { state.chat = null; await newChat(); }
        await loadChats();
      } catch (err) { toast(err.message); }
    });
    item.addEventListener('click', () => loadChat(chat.id).catch((e) => toast(e.message)));
    item.append(title, meta, del);
    els.chatList.appendChild(item);
  }
}

async function newChat() {
  const chat = await api('/api/chats', {
    method: 'POST',
    body: JSON.stringify({ project: state.project }),
  });
  state.chat = chat;
  state.project = chat.project || state.project;
  els.chatTitle.textContent = chat.title || 'New chat';
  renderThread();
  await loadChats();
  els.input.focus();
}

async function loadChat(id) {
  const chat = await api(`/api/chats/${id}`);
  state.chat = chat;
  state.project = chat.project || '';
  if (state.projects.some((p) => p.name === state.project)) {
    els.projectSelect.value = state.project;
  }
  els.chatTitle.textContent = chat.title || 'New chat';
  updateProjectUI();
  renderThread();
  renderChatList();
  els.input.focus();
}

/* --------------------------------------------------------------- thread */
function renderThread() {
  els.thread.innerHTML = '';
  const messages = state.chat?.messages || [];
  els.empty.hidden = messages.length > 0;
  const inner = document.createElement('div');
  inner.className = 'thread-inner';
  for (const msg of messages) inner.appendChild(renderMessage(msg));
  els.thread.appendChild(inner);
  renderChatList();
  scrollBottom();
}

function renderMessage(msg) {
  const article = document.createElement('article');
  article.className = `msg ${msg.role}`;

  if (msg.role === 'user') {
    const role = document.createElement('div');
    role.className = 'role';
    role.textContent = 'You';
    const bubble = document.createElement('div');
    bubble.className = 'bubble';
    bubble.textContent = msg.text || '';
    article.append(role, bubble);
    return article;
  }

  const role = document.createElement('div');
  role.className = 'role';
  role.innerHTML = '<span class="mark">◈</span><span>Spool</span>';
  article.appendChild(role);

  if (msg.intent && msg.intent.kind) {
    const chip = document.createElement('div');
    chip.className = 'intent-chip';
    chip.textContent = `🎯 ${msg.intent.kind}${msg.intent.project ? ' · ' + msg.intent.project : ''}${msg.intent.service ? '/' + msg.intent.service : ''}`;
    article.appendChild(chip);
  }

  const body = document.createElement('div');
  body.className = 'assistant-body';
  if (msg.text) {
    body.innerHTML = renderMarkdown(msg.text);
  } else if (msg.streaming) {
    body.innerHTML = '<div class="md"><p>…</p></div>';
  }
  article.appendChild(body);

  const pills = buildPills(msg.events || []);
  if (pills.children.length) article.appendChild(pills);

  const tools = document.createElement('div');
  tools.className = 'msg-tools';
  const nerd = document.createElement('button');
  nerd.type = 'button';
  nerd.className = 'nerd-link';
  nerd.textContent = '{} nerd stats';
  nerd.addEventListener('click', () => openNerd(msg));
  tools.appendChild(nerd);
  article.appendChild(tools);

  article._message = msg;
  return article;
}

function buildPills(events) {
  const wrap = document.createElement('div');
  wrap.className = 'pills';
  const open = [];
  for (const ev of events) {
    if (ev.type === 'tool_start') {
      const pill = document.createElement('span');
      pill.className = 'pill running';
      pill.innerHTML = '<span class="dot"></span>';
      const name = document.createElement('span');
      name.className = 'name';
      name.textContent = ev.tool || 'tool';
      const message = document.createElement('span');
      message.className = 'msg';
      message.textContent = argsSummary(ev.args);
      pill.append(name, message);
      pill._tool = ev.tool;
      wrap.appendChild(pill);
      open.push(pill);
    } else if (ev.type === 'tool_progress') {
      const pill = open[open.length - 1];
      if (pill) {
        const message = pill.querySelector('.msg');
        if (message) message.textContent = ev.message || '';
      }
    } else if (ev.type === 'tool_end') {
      let pill = null;
      for (let i = open.length - 1; i >= 0; i--) {
        if (open[i]._tool === ev.tool) { pill = open[i]; break; }
      }
      if (!pill) {
        pill = document.createElement('span');
        pill.className = 'pill';
        pill.innerHTML = '<span class="dot"></span>';
        const name = document.createElement('span');
        name.className = 'name';
        name.textContent = ev.tool || 'tool';
        pill.appendChild(name);
        wrap.appendChild(pill);
      }
      pill.classList.remove('running');
      pill.classList.add(ev.success ? 'ok' : 'fail');
      const message = pill.querySelector('.msg') || document.createElement('span');
      message.className = 'msg';
      message.textContent = ev.success ? (ev.message || 'done') : (ev.message || 'failed');
      if (!message.parentNode) pill.appendChild(message);
      pill.title = `${ev.tool || 'tool'} · ${fmtDuration(ev.duration)}`;
      open.splice(open.indexOf(pill), 1);
    }
  }
  return wrap;
}

function argsSummary(args) {
  if (!args) return '';
  try {
    const s = Object.entries(args).map(([k, v]) => `${k}=${typeof v === 'string' ? v : JSON.stringify(v)}`).join(' ');
    return s.length > 46 ? `${s.slice(0, 46)}…` : s;
  } catch (_) { return ''; }
}

/* ---------------------------------------------------------------- send */
async function send(rawText, opts = {}) {
  const text = String(rawText || '').trim();
  if (!text || state.streaming) return;
  if (handleClientCommand(text)) return;
  if (!state.chat) await newChat();
  if (!state.chat) return;

  const confirm = Boolean(opts.confirm);
  const retry = Boolean(opts.retry);
  const appendUser = opts.appendUser !== false;
  if (appendUser) {
    state.chat.messages.push({
      id: `local-${Date.now()}`,
      role: 'user',
      text,
      created_at: new Date().toISOString(),
    });
  }
  const placeholder = {
    id: 'stream', role: 'assistant', text: '', events: [], intent: null,
    streaming: true, created_at: new Date().toISOString(),
  };
  state.chat.messages.push(placeholder);
  els.input.value = '';
  autoresize();
  updateSlash();
  state.streaming = true;
  els.send.disabled = true;
  setStatus('thinking…');
  renderThread();

  try {
    await streamChat({
      chat_id: state.chat.id,
      project: state.project,
      text,
      confirm,
      retry,
    }, placeholder);
  } catch (err) {
    placeholder.streaming = false;
    placeholder.text = `Error: ${err.message}`;
    renderThread();
    toast(err.message);
  } finally {
    state.streaming = false;
    els.send.disabled = false;
    setStatus('');
    renderThread();
    if (state.queued) {
      const queued = state.queued;
      state.queued = null;
      setTimeout(() => send(queued.text, { confirm: true, retry: true, appendUser: false }), 0);
    }
  }
}

function handleClientCommand(text) {
  const cmd = text.toLowerCase();
  if (cmd === '/new' || cmd === '/clear') {
    newChat().catch((e) => toast(e.message));
    return true;
  }
  if (cmd === '/dash' || cmd === '/nerd') {
    toggleNerd();
    return true;
  }
  return false;
}

async function streamChat(payload, placeholder) {
  const res = await fetch('/api/chat', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  });
  if (!res.ok) {
    let message = res.statusText;
    try { const j = await res.json(); if (j && j.error) message = j.error; } catch (_) {}
    throw new Error(message || `HTTP ${res.status}`);
  }
  if (!res.body) throw new Error('streaming not supported');
  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buffer = '';

  while (true) {
    const { value, done } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });
    let idx;
    while ((idx = buffer.indexOf('\n\n')) >= 0) {
      const frame = buffer.slice(0, idx);
      buffer = buffer.slice(idx + 2);
      const line = frame.split('\n').find((l) => l.startsWith('data: '));
      if (!line) continue;
      let payloadEvent;
      try { payloadEvent = JSON.parse(line.slice(6)); } catch (_) { continue; }
      if (payloadEvent.type === 'event') {
        handleAgentEvent(payloadEvent.event, placeholder);
      } else if (payloadEvent.type === 'start') {
        if (payloadEvent.chat) {
          if (!state.chat) state.chat = { messages: [] };
          state.chat.id = payloadEvent.chat.id;
          state.chat.title = payloadEvent.chat.title;
        }
      } else if (payloadEvent.type === 'done') {
        finalizeTurn(payloadEvent, placeholder, payload);
      } else if (payloadEvent.type === 'error') {
        throw new Error(payloadEvent.error || 'stream error');
      }
    }
  }
}

function handleAgentEvent(ev, placeholder) {
  if (!ev || !ev.type) return;
  placeholder.events.push(ev);
  if (ev.type === 'intent') {
    const a = ev.args || {};
    placeholder.intent = {
      kind: a.kind || 'request',
      project: a.project || '',
      service: a.service || '',
      confidence: a.confidence || 0,
      source: a.source || '',
    };
    setStatus(`intent · ${placeholder.intent.kind}`);
  } else if (ev.type === 'tool_start') {
    setStatus(`running ${ev.tool}…`);
  } else if (ev.type === 'tool_progress') {
    setStatus(ev.message || `running…`);
  } else if (ev.type === 'tool_end') {
    setStatus(ev.success ? `${ev.tool} done` : `${ev.tool} failed`);
  } else if (ev.type === 'final') {
    placeholder.text = ev.message || '';
  }
  renderThread();
}

function finalizeTurn(done, placeholder, payload) {
  if (done.message) {
    placeholder.id = done.message.id || placeholder.id;
    placeholder.text = done.message.text || placeholder.text;
    placeholder.intent = done.message.intent || placeholder.intent;
    placeholder.events = done.message.events || placeholder.events;
  }
  placeholder.streaming = false;
  if (done.chat) {
    if (!state.chat) state.chat = { messages: [] };
    state.chat.title = done.chat.title || state.chat.title;
    state.chat.project = done.chat.project || state.chat.project;
    state.chat.project_id = done.chat.project_id || state.chat.project_id;
    if (done.chat.project) {
      state.project = done.chat.project;
      if (state.projects.some((p) => p.name === state.project)) {
        els.projectSelect.value = state.project;
      }
    }
    els.chatTitle.textContent = state.chat.title || 'New chat';
    updateProjectUI();
  }
  renderThread();
  loadChats().catch(() => {});
  loadProjects().catch(() => {});
  if (done.needs_confirmation && done.confirmation) {
    state.pending = { text: payload.text, confirmation: done.confirmation };
    showModal(done.confirmation);
  }
}

/* --------------------------------------------------------------- modal */
function showModal(confirmation) {
  const destructive = confirmation.destructive;
  els.modalText.textContent = destructive
    ? `Spool needs approval for a destructive action: ${confirmation.tool}.`
    : `Spool needs approval to run: ${confirmation.tool}.`;
  els.modalArgs.textContent = JSON.stringify(confirmation.args || {}, null, 2);
  els.modal.hidden = false;
  els.modalConfirm.focus();
}

function hideModal() {
  els.modal.hidden = true;
  state.pending = null;
}

function confirmPending() {
  const pending = state.pending;
  hideModal();
  if (!pending) return;
  if (state.streaming) {
    // The confirmation can be clicked while the current turn is still finalizing.
    state.queued = pending;
    toast('Executing after the current turn…');
    return;
  }
  send(pending.text, { confirm: true, retry: true, appendUser: false });
}

/* --------------------------------------------------------- nerd stats */
function toggleNerd(force) {
  const open = force === undefined ? !state.nerdOpen : Boolean(force);
  state.nerdOpen = open;
  els.app.classList.toggle('nerd-open', open);
  els.nerd.hidden = !open;
  if (open) {
    const msg = state.nerdMessage || lastAssistantMessage();
    state.nerdMessage = msg;
    renderNerd(msg);
  }
}

function openNerd(msg) {
  state.nerdMessage = msg;
  toggleNerd(true);
}

function lastAssistantMessage() {
  const messages = state.chat?.messages || [];
  for (let i = messages.length - 1; i >= 0; i--) {
    if (messages[i].role === 'assistant') return messages[i];
  }
  return null;
}

function renderNerd(msg) {
  const body = els.nerdBody;
  body.innerHTML = '';
  if (!msg) {
    body.innerHTML = '<div class="nerd-empty">Run an agent turn to see tool calls, timings, and raw events.</div>';
    return;
  }
  const events = msg.events || [];
  const tools = events.filter((e) => e.type === 'tool_start').length;
  const totalDuration = events.reduce((sum, e) => sum + (e.duration || 0), 0);
  const bytes = events.reduce((sum, e) => sum + (e.bytes || 0), 0);

  const summary = document.createElement('dl');
  summary.className = 'nerd-summary';
  const rows = [
    ['intent', msg.intent?.kind || '—'],
    ['project', msg.intent?.project || state.project || '—'],
    ['confidence', msg.intent?.confidence ? Number(msg.intent.confidence).toFixed(2) : '—'],
    ['source', msg.intent?.source || '—'],
    ['tool calls', String(tools)],
    ['tool time', fmtDuration(totalDuration) || '—'],
    ['bytes', fmtBytes(bytes)],
    ['events', String(events.length)],
  ];
  for (const [k, v] of rows) {
    const dt = document.createElement('dt'); dt.textContent = k;
    const dd = document.createElement('dd'); dd.textContent = v;
    summary.append(dt, dd);
  }
  body.appendChild(summary);

  const toolbar = document.createElement('div');
  toolbar.className = 'nerd-toolbar';
  const copy = document.createElement('button');
  copy.type = 'button';
  copy.className = 'ghost';
  copy.textContent = 'Copy JSON';
  copy.addEventListener('click', () => {
    navigator.clipboard.writeText(JSON.stringify({ message: msg, events }, null, 2))
      .then(() => toast('Copied events JSON'))
      .catch(() => toast('Clipboard unavailable'));
  });
  toolbar.appendChild(copy);
  body.appendChild(toolbar);

  if (!events.length) {
    const empty = document.createElement('div');
    empty.className = 'nerd-empty';
    empty.textContent = 'No events recorded for this message.';
    body.appendChild(empty);
    return;
  }

  for (const ev of events) {
    const row = document.createElement('div');
    row.className = 'nerd-event';
    const top = document.createElement('div');
    top.className = 'nerd-event-top';
    const type = document.createElement('span');
    type.className = 'nerd-type';
    type.textContent = ev.type + (ev.tool ? ` · ${ev.tool}` : '');
    const time = document.createElement('span');
    time.className = 'nerd-time';
    time.textContent = ev.time ? new Date(ev.time).toLocaleTimeString() : '';
    top.append(type, time);
    row.appendChild(top);

    const sub = document.createElement('div');
    sub.className = 'nerd-event-sub';
    const bits = [];
    if (ev.state) bits.push(`state=${ev.state}`);
    if (ev.success) bits.push('success');
    if (ev.duration) bits.push(fmtDuration(ev.duration));
    if (ev.bytes) bits.push(fmtBytes(ev.bytes));
    if (ev.message) bits.push(ev.message);
    sub.textContent = bits.join(' · ');
    row.appendChild(sub);

    if (ev.args || ev.output) {
      const details = document.createElement('details');
      const summaryEl = document.createElement('summary');
      summaryEl.textContent = 'args / result';
      const pre = document.createElement('pre');
      const payload = {};
      if (ev.args) payload.args = ev.args;
      if (ev.output) payload.output = ev.output;
      pre.textContent = JSON.stringify(payload, null, 2);
      details.append(summaryEl, pre);
      row.appendChild(details);
    }
    body.appendChild(row);
  }
}

/* --------------------------------------------------------------- slash */
function updateSlash() {
  const value = els.input.value;
  if (!value.startsWith('/') || value.includes('\n')) {
    els.slash.hidden = true;
    state.slashIndex = -1;
    return;
  }
  const query = value.toLowerCase();
  const matches = COMMANDS.filter(([cmd]) => cmd.startsWith(query) || query.startsWith(cmd));
  if (!matches.length) { els.slash.hidden = true; return; }
  if (state.slashIndex >= matches.length) state.slashIndex = 0;
  els.slash.innerHTML = '';
  matches.forEach(([cmd, desc], i) => {
    const item = document.createElement('button');
    item.type = 'button';
    item.className = `slash-item${i === state.slashIndex ? ' active' : ''}`;
    const code = document.createElement('code');
    code.textContent = cmd;
    const span = document.createElement('span');
    span.textContent = desc;
    item.append(code, span);
    item.addEventListener('mousedown', (e) => { e.preventDefault(); insertSlash(cmd); });
    els.slash.appendChild(item);
  });
  els.slash.hidden = false;
}

function insertSlash(cmd) {
  els.input.value = `${cmd} `;
  els.slash.hidden = true;
  state.slashIndex = -1;
  els.input.focus();
  autoresize();
}

function onInputKey(e) {
  if (!els.slash.hidden) {
    const items = $$('.slash-item', els.slash);
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      state.slashIndex = Math.min(items.length - 1, state.slashIndex + 1);
      updateSlashActive(items);
      return;
    }
    if (e.key === 'ArrowUp') {
      e.preventDefault();
      state.slashIndex = Math.max(0, state.slashIndex - 1);
      updateSlashActive(items);
      return;
    }
    if (e.key === 'Enter' && !e.shiftKey && items.length) {
      e.preventDefault();
      const cmd = items[Math.max(0, state.slashIndex)].querySelector('code').textContent;
      insertSlash(cmd);
      return;
    }
    if (e.key === 'Escape') {
      els.slash.hidden = true;
      return;
    }
  }
  if (e.key === 'Enter' && !e.shiftKey) {
    e.preventDefault();
    send(els.input.value);
  }
}

function updateSlashActive(items) {
  items.forEach((el, i) => el.classList.toggle('active', i === state.slashIndex));
}

function readTheme() {
  try { return localStorage.getItem('spool-theme') || 'light'; } catch (_) { return 'light'; }
}

function applyTheme(theme, persist = true) {
  const value = theme === 'dark' ? 'dark' : 'light';
  document.documentElement.dataset.theme = value;
  if (els.themeToggle) {
    els.themeToggle.textContent = value === 'dark' ? 'Light theme' : 'Dark theme';
    els.themeToggle.setAttribute('aria-pressed', value === 'dark' ? 'true' : 'false');
  }
  if (persist) {
    try { localStorage.setItem('spool-theme', value); } catch (_) {}
  }
}
