'use strict';

const $ = (sel) => document.querySelector(sel);

let tunnels = [];      // последний ответ сервера
let lastJson = '';     // чтобы не перерисовывать список, если ничего не изменилось
let pollTimer = null;
let editingId = null;  // id редактируемого туннеля, null - создаём новый
let offline = false;
let stopped = false; // программа остановлена кнопкой «Выйти»
let installing = false; // идёт установка ssh
let okDismissed = false; // плашка "ssh установлен" закрыта

/* ---------------------------------------------------------------- запросы */

async function api(action, body) {
  const res = await fetch('/api/' + action, {
    method: body ? 'POST' : 'GET',
    // Сервер принимает запросы только с этим заголовком (защита от чужих сайтов)
    headers: { 'X-Tunnels': '1', 'Content-Type': 'application/json' },
    body: body ? JSON.stringify(body) : undefined,
  });
  return res.json();
}

function toast(text, ms = 2200) {
  const el = $('#toast');
  el.textContent = text;
  el.classList.add('on');
  clearTimeout(el.timer);
  el.timer = setTimeout(() => el.classList.remove('on'), ms);
}

async function copyText(text, what) {
  try {
    await navigator.clipboard.writeText(text);
  } catch {
    // На http-страницах без localhost clipboard API может быть закрыт - старый способ
    const area = document.createElement('textarea');
    area.value = text;
    document.body.appendChild(area);
    area.select();
    document.execCommand('copy');
    area.remove();
  }
  toast(what + ' скопирован');
}

function escapeHtml(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

/* ---------------------------------------------------------------- опрос статусов */

async function refresh() {
  if (stopped) return;
  try {
    const data = await api('list');
    if (offline) { offline = false; toast('Связь с панелью восстановлена'); }

    $('#banner').hidden = !data.sshMissing;
    $('#sshinfo').textContent = data.sshPath ? 'ssh: ' + data.sshPath : '';
    installing = data.installing;
    $('#bannerok').hidden = !data.installDone || okDismissed;
    $('#okver').textContent = data.installDone || '';
    $('#install').disabled = installing;
    $('#installmsg').textContent = installing
      ? 'Идёт установка. Нажмите «Да» в окне Windows, это займёт 1-3 минуты…'
      : (data.installErr || '');
    if (data.installErr) $('#installmsg').className = 'err-text';
    $('#bannercmd').textContent = data.sshHint ? data.sshHint.split('выполните: ')[1] : '';

    // Перерисовываем только при изменениях: нет мигания и сбитого выделения текста
    const json = JSON.stringify(data.tunnels);
    $('#login').classList.toggle('need', data.tunnels.some((t) => t.warn && t.warn.startsWith('Укажите')));
    if (json !== lastJson) {
      lastJson = json;
      tunnels = data.tunnels;
      render();
    }
  } catch {
    if (!offline) { offline = true; toast('Нет связи с панелью. Не закрыто ли окно программы?', 4000); }
  }
  schedulePoll();
}

function schedulePoll() {
  clearTimeout(pollTimer);
  if (document.hidden) return; // вкладка в фоне - не нагружаем сервер
  const busy = installing || tunnels.some((t) => t.status === 'connecting');
  pollTimer = setTimeout(refresh, busy ? 1000 : 5000);
}

document.addEventListener('visibilitychange', () => { if (!document.hidden) refresh(); });

/* ---------------------------------------------------------------- отрисовка */

function actionButtons(t) {
  switch (t.status) {
    case 'up':
      return `<a class="go" href="${escapeHtml(t.url)}" target="_blank" rel="noopener">Войти на сайт</a>
              <button data-a="down" class="bad">Стоп</button>`;
    case 'connecting':
      return '<button disabled>Подключение…</button><button data-a="down" class="bad">Отмена</button>';
    case 'invalid':
      return '<button data-a="edit" class="pri">Исправить</button>';
    default:
      return '<button data-a="up" class="pri">Подключить</button>';
  }
}

function rowHtml(t) {
  const showError = (t.status === 'failed' || t.status === 'invalid') && t.error;
  return `
    <div class="row ${t.status}" data-id="${t.id}">
      <span class="dot"></span>
      <div>
        <div class="name">${escapeHtml(t.name)}</div>
        <div class="sub">${escapeHtml(t.user ? t.user + '@' : '')}${escapeHtml(t.host)}:${escapeHtml(t.sshPort)} · localhost:${t.localPort} → ${escapeHtml(t.target)}</div>
        <a class="url" href="${escapeHtml(t.url)}" target="_blank" rel="noopener">${escapeHtml(t.url)}</a>
      </div>
      <div class="act">
        ${actionButtons(t)}
        ${t.hasPassword ? '<button class="ghost" data-a="pw" title="Копировать пароль">🔑</button>' : ''}
        <button class="ghost" data-a="url" title="Копировать ссылку">🔗</button>
        <button class="ghost" data-a="edit" title="Изменить">✎</button>
      </div>
      ${showError ? `<div class="msg err">${escapeHtml(t.error)}</div>` : ''}
      ${t.warn ? `<div class="msg warn">${escapeHtml(t.warn)}</div>` : ''}
    </div>`;
}

function render() {
  const query = $('#q').value.trim().toLowerCase();
  const up = tunnels.filter((t) => t.status === 'up').length;
  $('#count').textContent = tunnels.length ? `${up} из ${tunnels.length} подключено` : '';

  const rows = tunnels.filter((t) => !query || (t.name + t.host + t.url + t.localPort).toLowerCase().includes(query));
  if (!rows.length) {
    $('#list').innerHTML = `<div class="empty">${tunnels.length ? 'Ничего не найдено' : 'Пока пусто. Нажмите «Импорт» и вставьте свои команды из блокнота.'}</div>`;
    return;
  }
  $('#list').innerHTML = rows.map(rowHtml).join('');
}

/* ---------------------------------------------------------------- действия в списке */

// Один обработчик на весь список (делегирование), а не по обработчику на каждую кнопку
$('#list').addEventListener('click', async (e) => {
  const btn = e.target.closest('button[data-a]');
  if (!btn) return;
  const id = btn.closest('.row').dataset.id;
  const t = tunnels.find((x) => x.id === id);

  switch (btn.dataset.a) {
    case 'up': {
      t.status = 'connecting'; // сразу показываем реакцию, не ждём ответ сервера
      render();
      const res = await api('up', { id });
      if (!res.ok) toast(res.error, 5000);
      refresh();
      break;
    }
    case 'down': {
      const res = await api('down', { id });
      if (res.error) toast(res.error, 7000);
      refresh();
      break;
    }
    case 'pw':
      copyText((await api('password', { id })).password, 'Пароль');
      break;
    case 'url':
      copyText(t.url, 'Ссылка');
      break;
    case 'edit':
      openEditor(id);
      break;
  }
});

$('#install').onclick = async () => {
  const res = await api('installssh', {});
  if (res.error) toast(res.error, 6000);
  refresh();
};
$('#okhide').onclick = () => { okDismissed = true; $('#bannerok').hidden = true; };
$('#bannercopy').onclick = () => copyText($('#bannercmd').textContent, 'Команда');

$('#upall').onclick = async () => {
  const res = await api('upall', {});
  if (!res.ok) toast(res.error, 6000);
  refresh();
};
$('#downall').onclick = async () => { await api('downall', {}); refresh(); };
$('#q').oninput = render;

// "/" ставит курсор в поиск, как на GitHub
document.addEventListener('keydown', (e) => {
  if (e.key === '/' && !/INPUT|TEXTAREA/.test(document.activeElement.tagName)) {
    e.preventDefault();
    $('#q').focus();
  }
});

/* ---------------------------------------------------------------- форма туннеля */

let hintTimer;

// Подсказка под полем команды: показываем, как мы поняли строку
async function checkCommand() {
  const command = $('#cmd').value.trim();
  const hint = $('#hint');
  if (!command) { hint.className = 'hint'; hint.textContent = ''; return; }

  const r = await api('parse', { command });
  if (r.error) { hint.className = 'hint e'; hint.textContent = r.error; return; }
  hint.className = 'hint g';
  hint.textContent = `✓ ${r.user ? r.user + '@' : ''}${r.host}:${r.sshPort} · localhost:${r.localPort} → ${r.target}`;
  $('#url').placeholder = 'http://localhost:' + r.localPort;
}

$('#cmd').oninput = () => { clearTimeout(hintTimer); hintTimer = setTimeout(checkCommand, 250); }; // debounce

$('#eye').onclick = () => {
  const input = $('#pw');
  input.type = input.type === 'password' ? 'text' : 'password';
  $('#eye').textContent = input.type === 'password' ? 'Показать' : 'Скрыть';
};

$('#add').onclick = () => {
  editingId = null;
  $('#dtitle').textContent = 'Новый туннель';
  ['#cmd', '#name', '#pw', '#url'].forEach((sel) => { $(sel).value = ''; });
  $('#hint').textContent = '';
  $('#del').hidden = true;
  $('#dlg').showModal();
  $('#cmd').focus();
};

async function openEditor(id) {
  const t = await api('get', { id });
  editingId = id;
  $('#dtitle').textContent = 'Изменить туннель';
  $('#cmd').value = t.command;
  $('#name').value = t.name;
  $('#pw').value = t.password;
  $('#url').value = t.url;
  $('#pw').type = 'password';
  $('#eye').textContent = 'Показать';
  $('#del').hidden = false;
  $('#dlg').showModal();
  checkCommand();
}

$('#cancel').onclick = () => $('#dlg').close();

$('#f').onsubmit = async (e) => {
  e.preventDefault();
  const res = await api('save', {
    id: editingId || '',
    command: $('#cmd').value,
    name: $('#name').value,
    password: $('#pw').value,
    url: $('#url').value,
  });
  if (res.error) { $('#hint').className = 'hint e'; $('#hint').textContent = res.error; return; }
  $('#dlg').close();
  refresh();
};

// Подтверждение через confirm(): для локальной утилиты этого достаточно
$('#del').onclick = async () => {
  if (!confirm('Удалить туннель (и отключить его)?')) return;
  await api('delete', { id: editingId });
  $('#dlg').close();
  refresh();
};

/* ---------------------------------------------------------------- импорт */

$('#imp').onclick = () => {
  $('#itext').value = '';
  $('#ihint').textContent = '';
  $('#idlg').showModal();
  $('#itext').focus();
};
$('#icancel').onclick = () => $('#idlg').close();

$('#if').onsubmit = async (e) => {
  e.preventDefault();
  const res = await api('import', { text: $('#itext').value });
  if (res.bad.length) {
    $('#ihint').textContent = 'Не распознано:\n' + res.bad.join('\n');
    if (!res.added) return; // всё плохое - оставляем окно открытым, чтобы поправить
  }
  $('#idlg').close();
  toast('Добавлено: ' + res.added);
  refresh();
};

refresh();

/* ---------------------------------------------------------------- логин и пароль для всех туннелей */

async function loadSettings() {
  const me = await api('settings');
  $('#login').value = me.login || '';
  $('#gpw').value = me.password || '';
}

$('#me').onsubmit = async (e) => {
  e.preventDefault();
  await api('savesettings', { login: $('#login').value, password: $('#gpw').value });
  toast('Сохранено');
  lastJson = ''; // перерисовать: пропадут предупреждения «Укажите логин»
  refresh();
};

$('#geye').onclick = () => {
  const input = $('#gpw');
  input.type = input.type === 'password' ? 'text' : 'password';
  $('#geye').textContent = input.type === 'password' ? 'Показать' : 'Скрыть';
};

// Список для передачи коллеге в формате импорта: название | команда | (пароль не передаём) | ссылка
$('#exportlist').onclick = () => {
  const lines = tunnels.map((t) => [t.name, t.command, '', t.url].join(' | '));
  copyText(lines.join('\n'), 'Список');
};

$('#quit').onclick = async () => {
  if (!confirm('Выключить программу и отключить все туннели?')) return;
  stopped = true;
  await api('quit', {});
  document.body.innerHTML = '<p style="padding:60px 20px;text-align:center">Программа остановлена, туннели отключены. Эту вкладку можно закрыть.</p>';
  clearTimeout(pollTimer);
};

loadSettings();
