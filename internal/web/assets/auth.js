import { installation } from "./config.mjs";
const $ = (s) => document.querySelector(s);
const status = $('#status');
const key = 'mp.login.v1';
const destination = new URLSearchParams(location.search).get('next') === 'admin' ? '/admin/' : '/';
if(location.pathname === '/account') $('#login-view').hidden = true;
let pending;
function message(text) { status.textContent = text; }
async function rpc(path, body = {}) {
  const response = await fetch('/auth/' + path, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body), cache: 'no-store', signal: AbortSignal.timeout(10000) });
  const data = await response.json();
  if (!response.ok) { const error = new Error(data.error || 'Не удалось связаться с сайтом.'); error.status = response.status; throw error; }
  return data;
}
function clearData() {
  try { for (const k of ['mp.schedule-cache.v1', 'mp.changes.v1']) localStorage.removeItem(k); } catch {}
}
clearData();
if ('serviceWorker' in navigator) navigator.serviceWorker.register('/sw.js').catch(() => {});
function remember() { try { pending ? sessionStorage.setItem(key, JSON.stringify(pending)) : sessionStorage.removeItem(key); } catch {} }
function showChallenge() {
  $('#providers').hidden = true; $('#challenge-view').hidden = false;
  const link = $('#bot-link');
  const base = pending.platform === 'tg' ? installation.telegram_url : installation.vk_url;
  link.hidden = !base;
  if (base) { const url = new URL(base); url.searchParams.set(pending.platform === 'tg' ? 'start' : 'ref', 'web_' + pending.challenge); link.href = url.href; }
  link.textContent = pending.platform === 'tg' ? 'Открыть Telegram-бота ↗' : 'Открыть VK-бота ↗';
  $('#manual-command').textContent = '/login ' + pending.challenge;
  $('#code').value = '';
  $('#expiry').textContent = 'Вход действителен 5 минут. Код вводится только в этом браузере.';
}
try { const saved = JSON.parse(sessionStorage.getItem(key)); if (saved?.expires > Date.now() && /^[\w-]{43}$/.test(saved.challenge) && ['tg','vk'].includes(saved.platform)) { pending = saved; showChallenge(); } } catch {}
for (const button of document.querySelectorAll('[data-provider]')) button.hidden = !(button.dataset.provider === 'tg' ? installation.telegram_url : installation.vk_url);
for (const button of document.querySelectorAll('[data-provider]')) button.addEventListener('click', async () => {
  button.disabled = true; message('');
  try { const platform = button.dataset.provider; const data = await rpc('start', { platform }); pending = { platform, challenge: data.challenge, expires: Date.now() + 300000 }; remember(); showChallenge(); $('#bot-link').focus(); }
  catch (e) { message(e.message); } finally { button.disabled = false; }
});
$('#restart').addEventListener('click', () => { pending = null; remember(); $('#providers').hidden = false; $('#challenge-view').hidden = true; message(''); document.querySelector('[data-provider]').focus(); });
$('#copy-command').addEventListener('click', async () => { try { await navigator.clipboard.writeText('/login ' + pending.challenge); $('#copy-command').textContent = 'Скопировано — отправь боту'; } catch { message('Скопируй команду под кнопкой вручную.'); } });
$('#code').addEventListener('input', e => { e.target.value = e.target.value.replace(/\D/g, '').slice(0, 8); });
$('#code-form').addEventListener('submit', async (event) => {
  event.preventDefault(); if (!pending) return;
  const button = event.submitter; button.disabled = true; message('');
  try { await rpc('finish', { challenge: pending.challenge, code: $('#code').value }); pending = null; remember(); const me = await rpc('me'); location.replace(destination === '/admin/' ? destination : '/?group=' + (me.group_id || '') + '&subgroup=' + (me.subgroup_id || 0)); }
  catch(e) { message(e.message); } finally { button.disabled = false; }
});
async function devices() {
  const me = await rpc('devices');
  $('#login-view').hidden = true; $('#account-view').hidden = false;
  document.title = 'Устройства · ' + (installation.app_name || 'Между парами');
  const list = $('#devices'); list.replaceChildren();
  for (const d of me.devices || []) {
    const row = document.createElement('div'); row.className = 'device';
    const title = document.createElement('strong'); title.textContent = d.label; row.append(title);
    const date = document.createElement('p'); date.textContent = 'Вход ' + new Date(d.created * 1000).toLocaleString('ru-RU') + ' · до ' + new Date(d.expires * 1000).toLocaleDateString('ru-RU'); row.append(date);
    if (d.current) { const mark = document.createElement('span'); mark.className = 'current'; mark.textContent = 'Это устройство'; row.append(mark); }
    else { const button = document.createElement('button'); button.textContent = 'Завершить сеанс'; button.addEventListener('click', async () => { button.disabled = true; try { await rpc('revoke', {id:d.id}); await devices(); message('Сеанс завершён.'); } catch(e) { message(e.message); button.disabled = false; } }); row.append(button); }
    list.append(row);
  }
}
$('#logout').addEventListener('click', async () => { $('#logout').disabled = true; try { await rpc('logout'); clearData(); location.replace('/login'); } catch (e) { message(e.message); $('#logout').disabled = false; } });
try { if (location.pathname === '/account') await devices(); else { await rpc('me'); location.replace(destination); } }
catch (e) { if (e.status === 401) { if (location.pathname === '/account') location.replace('/login'); } else message('Не удалось проверить сохранённый вход. ' + e.message); }

setInterval(() => {
  if (!pending) return;
  const remaining = Math.max(0, Math.ceil((pending.expires - Date.now()) / 1000));
  $('#expiry').textContent = remaining ? `Осталось ${Math.floor(remaining / 60)}:${String(remaining % 60).padStart(2, '0')}. Введи код в этом браузере.` : 'Время входа истекло. Нажми «начать заново» ниже.';
}, 1000);
