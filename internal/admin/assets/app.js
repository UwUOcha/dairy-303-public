/* Панель наблюдения: опрос, форматирование, рисование.
 *
 * Ванильный JS без сборки и без внешних библиотек. Причина не в аскезе:
 * панель нужна ровно в тот момент, когда с сервером что-то не так, и зависеть
 * в этот момент от чужого CDN — значит остаться без единственного прибора.
 * Графики поэтому рисуются руками в SVG; их здесь три, и все три простые.
 */

'use strict';

const BASE = document.body.dataset.base || '';

/* Опрос раз в десять секунд. Ответ raspd собирается за единицы миллисекунд,
   так что частота упирается не в сервер, а в то, с какой скоростью человек
   вообще способен замечать изменения. */
const POLL_MS = 10000;
/* История меняется раз в пять минут — чаще её тянуть незачем. */
const HISTORY_MS = 60000;

const state = {
  view: null,
  samples: [],
  range: '24h',
  historyRequest: 0,
  historyStatus: 'загружаю историю…',
  statsPending: false,
  /* tz — часовой пояс вуза, минуты от UTC. До первого ответа считаем
     московский: показать не то время хуже, чем показать прочерк, но
     единственная альтернатива — пустая шапка на первую секунду. */
  tz: 0,
};

/* ── форматирование ──────────────────────────────────────────────────────── */

const NBSP = ' ';

/** Целое с неразрывными пробелами между разрядами: 1 204. */
function num(n) {
  if (n === null || n === undefined || Number.isNaN(n)) return '—';
  return Math.round(n).toString().replace(/\B(?=(\d{3})+(?!\d))/g, NBSP);
}

/** Байты в человеческих единицах. */
function bytes(b) {
  if (!b) return '0' + NBSP + 'Б';
  const units = ['Б', 'КБ', 'МБ', 'ГБ', 'ТБ'];
  let i = 0;
  let v = b;
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
  const digits = v < 10 && i > 0 ? 1 : 0;
  return v.toFixed(digits) + NBSP + units[i];
}

/** Русская форма слова по числу: 1 день, 2 дня, 5 дней. */
function plural(n, one, few, many) {
  const mod10 = n % 10;
  const mod100 = n % 100;
  if (mod10 === 1 && mod100 !== 11) return one;
  if (mod10 >= 2 && mod10 <= 4 && (mod100 < 12 || mod100 > 14)) return few;
  return many;
}

/** Длительность крупными единицами: «29 дней 7 ч», «13 ч 4 мин», «42 с». */
function duration(sec) {
  if (sec === null || sec === undefined || sec < 0) return '—';
  sec = Math.floor(sec);
  const d = Math.floor(sec / 86400);
  const h = Math.floor((sec % 86400) / 3600);
  const m = Math.floor((sec % 3600) / 60);
  if (d > 0) return d + NBSP + plural(d, 'день', 'дня', 'дней') + ' ' + h + NBSP + 'ч';
  if (h > 0) return h + NBSP + 'ч ' + m + NBSP + 'мин';
  if (m > 0) return m + NBSP + 'мин';
  return sec + NBSP + 'с';
}

/** «13 ч назад» от unix-времени; пусто, когда события не было вовсе. */
function ago(unix, now) {
  if (!unix) return 'не было';
  const d = now - unix;
  if (d < 45) return 'только что';
  return duration(d) + ' назад';
}

/** Время суток в поясе вуза. */
function clockOf(unix, tz, withSeconds) {
  const shifted = new Date((unix + tz * 60) * 1000);
  const hh = String(shifted.getUTCHours()).padStart(2, '0');
  const mm = String(shifted.getUTCMinutes()).padStart(2, '0');
  if (!withSeconds) return hh + ':' + mm;
  return hh + ':' + mm + ':' + String(shifted.getUTCSeconds()).padStart(2, '0');
}

/** Минута суток (0..1439) для метки на суточной ленте. */
function minuteOfDay(unix, tz) {
  const shifted = (unix + tz * 60) % 86400;
  return ((shifted % 86400) + 86400) % 86400 / 60;
}

/** Подпись часового пояса: МСК для +180, иначе честное смещение. */
function tzLabel(tz) {
  if (tz === 180) return 'МСК';
  const sign = tz < 0 ? '−' : '+';
  const h = Math.floor(Math.abs(tz) / 60);
  const m = Math.abs(tz) % 60;
  return 'UTC' + sign + h + (m ? ':' + String(m).padStart(2, '0') : '');
}

/* ── мелкие помощники DOM и SVG ──────────────────────────────────────────── */

const $ = (id) => document.getElementById(id);

function el(tag, cls, text) {
  const node = document.createElement(tag);
  if (cls) node.className = cls;
  if (text !== undefined) node.textContent = text;
  return node;
}

const SVG_NS = 'http://www.w3.org/2000/svg';

function svg(tag, attrs) {
  const node = document.createElementNS(SVG_NS, tag);
  for (const k in attrs) node.setAttribute(k, attrs[k]);
  return node;
}

function svgText(x, y, text, attrs) {
  const node = svg('text', Object.assign({ x, y }, attrs || {}));
  node.textContent = text;
  return node;
}

/** Ширина, доступная под график. Возвращает 0, пока элемент не в потоке. */
function widthOf(node) {
  return Math.max(0, Math.floor(node.getBoundingClientRect().width));
}

/* ── суточная лента ──────────────────────────────────────────────────────── */

/* Главный элемент панели. Он отвечает на вопрос, который у этого сервиса
 * задают чаще всего: когда бот разговаривает с людьми и когда ходит к вузу.
 *
 * Ось — сутки, ровно те самые «минуты от полуночи», которыми система считает
 * и время пары, и время рассылки. Столбики — сколько человек попросили писать
 * им в этот час; пунктиры — последние обходы; бегунок — сейчас.
 */
function drawDayTrack(view) {
  const host = $('day-track');
  const width = widthOf(host);
  if (!width || !view || !view.rasp) return;

  const users = view.rasp.stats.users;
  const sync = view.rasp.stats.sync;
  const tz = state.tz;

  const H = 140;
  const PAD = 10;
  const inner = width - PAD * 2;
  const baseY = 100;
  const maxBar = 54;

  const x = (minute) => PAD + (minute / 1440) * inner;

  const root = svg('svg', {
    viewBox: '0 0 ' + width + ' ' + H,
    width: width,
    height: H,
    role: 'img',
    'aria-label': 'Сутки: время рассылок и обходов вуза'
  });

  /* Ночь и учебный день разной плотности: полоса 00–06 приглушена, чтобы
     скопление утренних столбиков читалось как начало дня, а не как середина. */
  root.appendChild(svg('rect', {
    x: x(0), y: baseY - maxBar - 8, width: x(360) - x(0), height: maxBar + 8,
    fill: 'var(--hover)', rx: 2
  }));
  root.appendChild(svg('rect', {
    x: x(1320), y: baseY - maxBar - 8, width: x(1440) - x(1320), height: maxBar + 8,
    fill: 'var(--hover)', rx: 2
  }));

  // Ось и часовые деления.
  root.appendChild(svg('line', {
    x1: x(0), y1: baseY, x2: x(1440), y2: baseY, stroke: 'var(--line)', 'stroke-width': 1
  }));
  for (let h = 0; h <= 24; h += 3) {
    const px = x(h * 60);
    root.appendChild(svg('line', {
      x1: px, y1: baseY, x2: px, y2: baseY + 5, stroke: 'var(--line)', 'stroke-width': 1,
    }));
    if (h < 24) {
      root.appendChild(svgText(px, baseY + 20, String(h).padStart(2, '0'), {
        fill: 'var(--muted)', 'font-size': 11,
        'text-anchor': h === 0 ? 'start' : 'middle',
      }));
    }
  }

  // Столбики рассылок. Утро и вечер делят часовую ячейку пополам, поэтому
  // совпавший час показывает две колонки рядом, а не одну поверх другой.
  const morning = bucketMap(users.morning_at);
  const evening = bucketMap(users.evening_at);
  const peak = Math.max(1, ...Object.values(morning), ...Object.values(evening));
  const cell = inner / 24;
  const barW = Math.max(2, cell / 2 - 1.5);

  for (let h = 0; h < 24; h++) {
    const left = x(h * 60);
    if (morning[h]) {
      const bh = Math.max(2, (morning[h] / peak) * maxBar);
      root.appendChild(svg('rect', {
        x: left + 0.75, y: baseY - bh, width: barW, height: bh, fill: 'var(--amber)', rx: 1,
      }));
    }
    if (evening[h]) {
      const bh = Math.max(2, (evening[h] / peak) * maxBar);
      root.appendChild(svg('rect', {
        x: left + cell / 2 + 0.75, y: baseY - bh, width: barW, height: bh, fill: 'var(--mint)', rx: 1,
      }));
    }
  }

  // Пик утренней рассылки подписан: это и есть минута, в которую бот делает
  // почти всю свою работу за сутки.
  // На узком экране подпись пика встаёт поверх метки обхода: столбики там
  // всего в пару пикселей, и разводить их некуда. Цифра есть в карточке
  // рассылок, так что на телефоне она здесь лишняя.
  const peakHour = width > 560 ? topHour(morning) : null;
  if (peakHour !== null) {
    root.appendChild(svgText(
      x(peakHour * 60) + barW / 2, baseY - (morning[peakHour] / peak) * maxBar - 7,
      num(morning[peakHour]),
      { fill: 'var(--amber)', 'font-size': 11, 'text-anchor': 'middle' }));
  }

  // Обходы вуза: пунктир на минуте, в которую они прошли.
  const marks = [
    { at: sync.full_sync_at, label: 'полный обход', row: 30 },
    { at: sync.hot_sync_at, label: 'горячие группы', row: 43 },
  ];
  for (const mark of marks) {
    if (!mark.at) continue;
    const px = x(minuteOfDay(mark.at, tz));
    root.appendChild(svg('line', {
      x1: px, y1: mark.row + 4, x2: px, y2: baseY,
      stroke: 'var(--muted)', 'stroke-width': 1, 'stroke-dasharray': '3 3',
    }));
    root.appendChild(svgText(px, mark.row, mark.label, {
      fill: 'var(--muted)', 'font-size': 10,
      'text-anchor': anchorFor(px, width),
    }));
  }

  // Бегунок «сейчас».
  const nowX = x(minuteOfDay(view.now, tz));
  root.appendChild(svg('line', {
    x1: nowX, y1: 20, x2: nowX, y2: baseY + 5, stroke: 'var(--text)', 'stroke-width': 1.5
  }));
  root.appendChild(svg('path', {
    d: 'M' + (nowX - 4) + ' 20 L' + (nowX + 4) + ' 20 L' + nowX + ' 26 Z', fill: 'var(--text)'
  }));
  root.appendChild(svgText(nowX, 13, clockOf(view.now, tz, false), {
    fill: 'var(--text)', 'font-size': 11, 'font-weight': 600,
    'text-anchor': anchorFor(nowX, width)
  }));

  host.replaceChildren(root);
}

/** Подписи у краёв прижимаются внутрь, иначе их срезает границей svg. */
function anchorFor(px, width) {
  if (px < 40) return 'start';
  if (px > width - 40) return 'end';
  return 'middle';
}

function bucketMap(buckets) {
  const out = {};
  for (const b of buckets || []) out[b.hour] = b.count;
  return out;
}

function topHour(map) {
  let best = null;
  for (const h in map) if (best === null || map[h] > map[best]) best = h;
  return best === null ? null : Number(best);
}

/* ── график ресурсов ─────────────────────────────────────────────────────── */

/* Процессор и память на одной шкале 0–100 %: обе величины относительные, и
 * держать под них две сетки значило бы занять вдвое больше места ради того же
 * ответа — «легла ли машина». */
function drawHostChart(samples) {
  const host = $('chart-host');
  const width = widthOf(host);
  if (!width) return;

  if (state.historyStatus) {
    host.replaceChildren(el('p', 'empty', state.historyStatus));
    return;
  }

  // Одной точкой линию не проведёшь, а пустой холст с осями читается как
  // поломка. Пока точек меньше двух, честнее сказать словами.
  if (samples.length < 2) {
    host.replaceChildren(el('p', 'empty',
      'история копится: график появится, когда наберутся первые точки'));
    return;
  }

  const H = 132;
  const PAD_L = 34;
  const PAD_R = 8;
  const TOP = 12;
  const BOT = 22;
  const plotW = width - PAD_L - PAD_R;
  const plotH = H - TOP - BOT;

  const t0 = samples[0].at;
  const t1 = samples[samples.length - 1].at;
  const span = Math.max(1, t1 - t0);
  const x = (s) => PAD_L + ((s.at - t0) / span) * plotW;
  const y = (pct) => TOP + plotH - (Math.min(100, Math.max(0, pct)) / 100) * plotH;

  const root = svg('svg', {
    viewBox: '0 0 ' + width + ' ' + H, width: width, height: H,
    role: 'img', 'aria-label': 'Процессор и память за выбранный период'
  });

  // Сетка 0 / 50 / 100 %.
  for (const pct of [0, 50, 100]) {
    const py = y(pct);
    root.appendChild(svg('line', {
      x1: PAD_L, y1: py, x2: width - PAD_R, y2: py,
      stroke: pct === 0 ? 'var(--line)' : 'var(--line-soft)', 'stroke-width': 1,
    }));
    root.appendChild(svgText(PAD_L - 7, py + 3.5, pct + '%', {
      fill: 'var(--muted)', 'font-size': 10, 'text-anchor': 'end',
    }));
  }

  const memPct = (s) => (s.mem_total ? (s.mem_used / s.mem_total) * 100 : 0);

  // Процессор — заливкой: он скачет, и площадь читается лучше линии.
  const cpuLine = samples.map((s) => x(s) + ' ' + y(s.cpu)).join(' L');
  root.appendChild(svg('path', {
    d: 'M' + x(samples[0]) + ' ' + y(0) + ' L' + cpuLine +
       ' L' + x(samples[samples.length - 1]) + ' ' + y(0) + ' Z',
    fill: 'var(--purple)'
  }));
  root.appendChild(svg('path', {
    d: 'M' + cpuLine, fill: 'none', stroke: 'var(--mint)', 'stroke-width': 1.5,
    'stroke-linejoin': 'round', 'stroke-linecap': 'round'
  }));

  // Память — линией: она ровная, и заливка только спорила бы с процессором.
  root.appendChild(svg('path', {
    d: 'M' + samples.map((s) => x(s) + ' ' + y(memPct(s))).join(' L'),
    fill: 'none', stroke: 'var(--amber)', 'stroke-width': 1.5,
    'stroke-linejoin': 'round', 'stroke-linecap': 'round'
  }));

  // Края периода подписаны временем: без них график — просто форма.
  root.appendChild(svgText(PAD_L, H - 6, chartTime(t0), {
    fill: 'var(--muted)', 'font-size': 10
  }));
  root.appendChild(svgText(width - PAD_R, H - 6, chartTime(t1), {
    fill: 'var(--muted)', 'font-size': 10, 'text-anchor': 'end'
  }));

  const last = samples[samples.length - 1];
  root.appendChild(svgText(PAD_L + 6, TOP + 10,
    'цп ' + last.cpu.toFixed(0) + '%   память ' + memPct(last).toFixed(0) + '%', {
      fill: 'var(--muted)', 'font-size': 11,
    }));

  host.replaceChildren(root);
}

/* ── график новых пользователей ──────────────────────────────────────────── */

function chartTime(unix) {
  const date = new Date((unix + state.tz * 60) * 1000).toISOString().slice(0, 10);
  return (['24h', '7d', '30d'].includes(state.range) ? ruDate(date) + ' · ' : '') +
    clockOf(unix, state.tz, false);
}

function drawNewChart(days) {
  const host = $('chart-new');
  const width = widthOf(host);
  if (!width) return;
  if (!days || !days.length) {
    host.replaceChildren(el('p', 'empty', 'нет данных о новых пользователях'));
    return;
  }

  const H = 92;
  const TOP = 14;
  const BOT = 18;
  const plotH = H - TOP - BOT;
  const gap = 2;
  const cell = width / days.length;
  const barW = Math.max(2, cell - gap);
  const peak = Math.max(0, ...days.map((d) => d.count));

  const root = svg('svg', {
    viewBox: '0 0 ' + width + ' ' + H, width: width, height: H,
    role: 'img', 'aria-label': 'Новые пользователи за 30 дней'
  });

  days.forEach((d, i) => {
    const h = d.count ? Math.max(2, (d.count / Math.max(1, peak)) * plotH) : 1;
    root.appendChild(svg('rect', {
      x: i * cell + gap / 2, y: TOP + plotH - h, width: barW, height: h,
      fill: d.count ? 'var(--amber)' : 'var(--line-soft)', rx: 1,
    }));
  });

  root.appendChild(svg('line', {
    x1: 0, y1: TOP + plotH + 1, x2: width, y2: TOP + plotH + 1, stroke: 'var(--line)'
  }));

  const total = days.reduce((a, d) => a + d.count, 0);
  root.appendChild(svgText(0, 9, 'пик ' + num(peak) + ' за день', {
    fill: 'var(--muted)', 'font-size': 10
  }));
  root.appendChild(svgText(width, 9, num(total) + ' за 30 дней', {
    fill: 'var(--muted)', 'font-size': 10, 'text-anchor': 'end'
  }));
  root.appendChild(svgText(0, H - 5, ruDate(days[0].date), {
    fill: 'var(--muted)', 'font-size': 10
  }));
  root.appendChild(svgText(width, H - 5, ruDate(days[days.length - 1].date), {
    fill: 'var(--muted)', 'font-size': 10, 'text-anchor': 'end'
  }));

  host.replaceChildren(root);
}

const MONTHS = ['янв', 'фев', 'мар', 'апр', 'мая', 'июн',
                'июл', 'авг', 'сен', 'окт', 'ноя', 'дек'];

function ruDate(iso) {
  const parts = iso.split('-');
  if (parts.length !== 3) return iso;
  return Number(parts[2]) + ' ' + MONTHS[Number(parts[1]) - 1];
}

/* ── отрисовка блоков ────────────────────────────────────────────────────── */

function renderFigures(view) {
  const host = $('figures');
  if (!view.rasp) return;
  const st = view.rasp.stats;

  const cards = [
    {
      kind: 'people',
      value: num(st.users.total),
      label: 'пользователей',
      note: num(st.users.configured) + ' выбрали группу · ' +
            num(st.users.active_7d) + ' за неделю',
    },
    {
      kind: 'people',
      value: num(st.users.active_1d),
      label: 'активны за сутки',
      note: st.users.total
        ? Math.round((st.users.active_1d / st.users.total) * 100) + '% базы'
        : '—',
    },
    {
      kind: 'machine',
      value: num(st.content.groups_active),
      label: 'групп в копии',
      note: num(st.content.lessons) + ' занятий · ' + bytes(st.content.db_bytes),
    },
    {
      kind: 'machine',
      value: duration(view.host.uptime_sec),
      label: 'аптайм машины',
      note: 'raspd ' + duration(view.rasp.rasp.uptime_sec) +
            (view.rasp.bot ? ' · botd ' + duration(view.rasp.bot.runtime.uptime_sec) : ''),
    },
  ];

  host.replaceChildren(...cards.map((c) => {
    const box = el('div', 'figure figure--' + c.kind);
    box.appendChild(el('div', 'figure__value', c.value));
    box.appendChild(el('div', 'figure__label', c.label));
    box.appendChild(el('div', 'figure__note', c.note));
    return box;
  }));
}

function renderWeb(view) {
  const host = $('web-figures');
  const web = view.rasp?.stats?.web;
  if (!web) {
    host.replaceChildren(el('p', 'empty', 'Статистика входов на сайт недоступна.'));
    return;
  }
  const cards = [
    {value: web.signed_in, label: 'вошли на сайт', note: 'из ' + num(web.allowed) + ' аккаунтов с доступом'},
    {value: web.sessions, label: 'действующих сессий', note: 'браузеры и устройства · до 4 на аккаунт'},
    {value: web.telegram, label: 'через Telegram', note: 'аккаунтов с сохранённым входом'},
    {value: web.vk, label: 'через ВКонтакте', note: 'аккаунтов с сохранённым входом'},
  ];
  host.replaceChildren(...cards.map(c => {
    const box = el('div', 'figure figure--people');
    box.appendChild(el('div', 'figure__value', num(c.value)));
    box.appendChild(el('div', 'figure__label', c.label));
    box.appendChild(el('div', 'figure__note', c.note));
    return box;
  }));
}

function renderResources(view) {
  const h = view.host;
  const host = $('res');

  if (h.err) {
    host.replaceChildren(el('p', 'empty', 'метрики машины недоступны: ' + h.err));
    return;
  }

  const rows = [
    { name: 'цп', pct: h.cpu_percent, value: h.cpu_percent.toFixed(0) + '% · ' + h.cores + ' ядр.' },
    { name: 'память', pct: pct(h.mem_used, h.mem_total), value: bytes(h.mem_used) + ' / ' + bytes(h.mem_total) },
    { name: 'подкачка', pct: pct(h.swap_used, h.swap_total), value: bytes(h.swap_used) + ' / ' + bytes(h.swap_total) },
    { name: 'диск', pct: pct(h.disk_used, h.disk_total), value: bytes(h.disk_used) + ' / ' + bytes(h.disk_total) },
  ];

  host.replaceChildren(...rows.map((r) => {
    const row = el('div', 'meter');
    row.appendChild(el('span', 'meter__name', r.name));

    const rail = el('div', 'meter__rail');
    const fill = el('div', 'meter__fill' + (r.pct >= 90 ? ' is-hot' : r.pct >= 70 ? ' is-warm' : ''));
    fill.style.width = Math.min(100, Math.max(1, r.pct)) + '%';
    rail.appendChild(fill);
    row.appendChild(rail);

    row.appendChild(el('span', 'meter__value', r.value));
    return row;
  }));

  host.appendChild(el('div', 'res__load',
    'нагрузка ' + h.load1.toFixed(2) + ' · ' + h.load5.toFixed(2) + ' · ' + h.load15.toFixed(2) +
    ' за 1, 5 и 15 минут'));
}

function pct(used, total) {
  return total ? (used / total) * 100 : 0;
}

function renderPlatforms(view) {
  const host = $('platforms');
  if (!view.rasp) return;
  const list = view.rasp.stats.users.by_platform || [];
  const total = list.reduce((a, p) => a + p.count, 0) || 1;

  host.replaceChildren(...list.map((p) => {
    const row = el('div', 'split__row');
    row.appendChild(el('span', 'split__name', p.label));

    const rail = el('div', 'split__rail');
    const cls = p.label === 'tg' ? 'split__fill--tg' : p.label === 'vk' ? 'split__fill--vk' : 'split__fill--other';
    const fill = el('div', 'split__fill ' + cls);
    fill.style.width = (p.count / total) * 100 + '%';
    rail.appendChild(fill);
    row.appendChild(rail);

    row.appendChild(el('span', 'split__value',
      num(p.count) + NBSP + '·' + NBSP + Math.round((p.count / total) * 100) + '%'));
    return row;
  }));
}

function renderReach(view) {
  if (!view.rasp) return;
  const u = view.rasp.stats.users;
  const n = u.notify;
  pairs($('reach'), [
    ['бот пишет сам', num(n.notify_on)],
    ['утреннее расписание', num(n.morning)],
    ['вечернее расписание', num(n.evening)],
    ['новости о правках', num(n.changes)],
    ['пишет и в пустые дни', num(n.empty_days)],
    ['заблокировали бота', num(u.blocked), probeNote(u)],
  ]);
  renderGone(u);
}

// probeNote объясняет цифру ушедших, пока её не на что опереть.
//
// Обход спрашивает площадку о каждом человеке раз в неделю и идёт порциями,
// так что в первые дни после обновления ноль рядом с «заблокировали» значит
// «ещё не спрашивали», а не «никто не ушёл». Как только круг замкнулся,
// пояснение пропадает само.
function probeNote(u) {
  if (!u.configured || u.probed >= u.configured) return null;
  return 'проверено ' + num(u.probed) + ' из ' + num(u.configured);
}

// renderGone — из каких групп уходили. Одна строка мелким шрифтом.
function renderGone(u) {
  const host = $('gone');
  const list = (u.blocked_groups || []).slice(0, 3);
  if (!list.length) {
    host.replaceChildren();
    host.hidden = true;
    return;
  }
  host.hidden = false;
  host.replaceChildren(
    el('span', 'gone__label', 'уходили из'),
    el('span', 'gone__list',
       list.map((g) => g.name + NBSP + '·' + NBSP + g.count).join(', ')));
}

function renderTopGroups(view) {
  const host = $('top-groups');
  if (!view.rasp) return;
  const list = view.rasp.stats.users.top_groups || [];

  if (!list.length) {
    host.replaceChildren(el('p', 'empty', 'группы ещё никто не выбрал'));
    return;
  }
  const peak = list[0].count || 1;

  host.replaceChildren(...list.map((g) => {
    const row = el('div', 'rank');

    const name = el('div', 'rank__name');
    name.appendChild(document.createTextNode(g.name));
    const meta = [g.department, g.course ? g.course + ' курс' : ''].filter(Boolean).join(' · ');
    if (meta) name.appendChild(el('span', 'rank__meta', meta));
    row.appendChild(name);

    row.appendChild(el('div', 'rank__count', num(g.count)));

    const bar = el('div', 'rank__bar');
    bar.style.width = Math.max(2, (g.count / peak) * 100) + '%';
    row.appendChild(bar);
    return row;
  }));
}

function renderDeps(view) {
  const host = $('top-deps');
  if (!view.rasp) return;
  const list = view.rasp.stats.users.top_departments || [];

  if (!list.length) {
    host.replaceChildren(el('p', 'empty', 'нет данных'));
  } else {
    const peak = list[0].count || 1;
    host.replaceChildren(...list.map((d) => {
      const row = el('div', 'rank');
      row.appendChild(el('div', 'rank__name', d.label));
      row.appendChild(el('div', 'rank__count', num(d.count)));
      const bar = el('div', 'rank__bar');
      bar.style.width = Math.max(2, (d.count / peak) * 100) + '%';
      row.appendChild(bar);
      return row;
    }));
  }

  const courses = view.rasp.stats.users.by_course || [];
  $('courses').replaceChildren(...courses.map((c) => {
    const chip = el('span', 'chip');
    chip.appendChild(document.createTextNode(c.label + ' '));
    chip.appendChild(el('b', null, num(c.count)));
    return chip;
  }));
}

function renderSync(view) {
  if (!view.rasp) return;
  const s = view.rasp.stats.sync;
  const now = view.rasp.now;
  const tz = state.tz;

  // Знаменатель — только те месяцы, которые синк обязан держать свежими:
  // прошлогодний месяц выпущенной группы не обновляется никогда и протухшим
  // не считается.
  const staleClass = s.months_stale === 0 ? 'is-good'
    : s.months_stale > s.tracked * 0.2 ? 'is-bad' : 'is-warn';

  const rows = [
    ['полный обход', ago(s.full_sync_at, now), s.full_sync_at ? clockOf(s.full_sync_at, tz, false) : '', fullSyncClass(s.full_sync_at, now)],
    ['горячие группы', ago(s.hot_sync_at, now), s.hot_sync_at ? clockOf(s.hot_sync_at, tz, false) : '', ''],
    ['каталог групп', ago(s.groups_sync_at, now), '', ''],
    ['месяцев загружено', num(s.months), 'синк держит ' + num(s.tracked), ''],
    ['из них протухло', num(s.months_stale),
      s.tracked ? Math.round((s.months_stale / s.tracked) * 100) + '% от нужных' : '', staleClass],
    ['вуз правил за сутки', num(s.changed_day), 'за неделю ' + num(s.changed_week), s.changed_day ? 'is-warn' : ''],
    ['снимков «до правки»', num(s.change_days), '', ''],
  ];

  if (view.rasp.sync_error) {
    rows.push(['последняя ошибка контура', view.rasp.sync_error, '', 'is-bad']);
  }
  pairs($('sync'), rows);
}

/* Ночной обход — единственная операция с расписанием: если он не проходил
   больше суток, копия стареет целиком, и это худшее, что может случиться
   тихо. */
function fullSyncClass(at, now) {
  if (!at) return 'is-bad';
  const age = now - at;
  if (age > 36 * 3600) return 'is-bad';
  if (age > 26 * 3600) return 'is-warn';
  return 'is-good';
}

function renderQueue(view) {
  if (!view.rasp) return;
  const o = view.rasp.stats.outbox;
  const now = view.rasp.now;

  const rows = [
    ['в очереди', num(o.pending), '', o.pending > 500 ? 'is-warn' : ''],
    ['застряло (3+ попытки)', num(o.stuck), '', o.stuck ? 'is-bad' : 'is-good'],
  ];
  if (o.oldest_created) rows.push(['самое старое', ago(o.oldest_created, now), '', '']);
  for (const p of o.by_platform || []) rows.push(['в очереди · ' + p.label, num(p.count), '', '']);
  pairs($('queue'), rows);

  const host = $('counters');
  const bot = view.rasp.bot;
  if (!bot || !bot.platforms || !bot.platforms.length) {
    host.replaceChildren(el('p', 'empty',
      bot ? 'бот на связи, но ещё ничего не считал' : 'бот ещё не отчитывался'));
    return;
  }

  host.replaceChildren(...bot.platforms.map((p) => {
    const box = el('div', 'pc');
    box.appendChild(el('span', 'pc__name', p.platform));
    box.appendChild(stat('событий', num(p.updates)));
    box.appendChild(stat('отправлено', num(p.sent)));
    box.appendChild(stat('ошибок', num(p.errors), p.errors > 0));
    box.appendChild(stat('не дошло', num(p.failed), p.failed > 0));
    box.appendChild(stat('заблокировали', num(p.blocked)));
    return box;
  }));
}

function stat(label, value, bad) {
  const node = el('span', 'pc__stat' + (bad ? ' is-bad' : ''));
  node.appendChild(document.createTextNode(label + ' '));
  node.appendChild(el('b', null, value));
  return node;
}

function renderLog(view) {
  const host = $('log');
  if (!view.rasp) return;

  // Записи обоих демонов в одной ленте: разбирать, кто из них пожаловался,
  // почти всегда нужно уже после того, как увидел саму жалобу.
  const rows = [];
  for (const e of view.rasp.log || []) rows.push({ e, src: 'raspd' });
  if (view.rasp.bot) for (const e of view.rasp.bot.log || []) rows.push({ e, src: 'botd' });
  rows.sort((a, b) => new Date(b.e.at) - new Date(a.e.at));

  if (!rows.length) {
    host.replaceChildren(el('p', 'empty', 'тихо: ни одной жалобы с момента запуска'));
    return;
  }

  host.replaceChildren(...rows.slice(0, 60).map(({ e, src }) => {
    const row = el('div', 'log__row');
    const at = Math.floor(new Date(e.at).getTime() / 1000);
    row.appendChild(el('div', 'log__time', clockOf(at, state.tz, false)));
    row.appendChild(el('div', 'log__src', src));

    const msg = el('div', 'log__msg' + (e.level === 'ERROR' ? ' is-error' : ' is-warn'));
    msg.appendChild(document.createTextNode(e.msg));
    if (e.attrs) msg.appendChild(el('span', 'log__attrs', e.attrs));
    row.appendChild(msg);
    return row;
  }));
}

function renderFoot(view) {
  if (!view.rasp) {
    $('foot-daemons').textContent = 'raspd недоступен';
    return;
  }
  const parts = ['raspd ' + view.rasp.rasp.go_version +
                 ' · горутин ' + view.rasp.rasp.goroutines +
                 ' · ' + bytes(view.rasp.rasp.sys)];
  if (view.rasp.bot) {
    const age = view.rasp.now - view.rasp.bot.received_at;
    parts.push('botd горутин ' + view.rasp.bot.runtime.goroutines +
               ' · ' + bytes(view.rasp.bot.runtime.sys) +
               ' · отчёт ' + ago(view.rasp.bot.received_at, view.rasp.now) +
               (age > 120 ? ' ⚠' : ''));
  }
  $('foot-daemons').textContent = parts.join('    ');
}

/** Заполняет список пар. Каждая строка: [подпись, значение, приписка, класс]. */
function pairs(host, rows) {
  host.replaceChildren(...rows.map(([label, value, note, cls]) => {
    const row = document.createElement('div');
    row.appendChild(el('dt', null, label));
    const dd = el('dd', cls || null);
    dd.appendChild(document.createTextNode(value));
    if (note) dd.appendChild(el('small', null, note));
    row.appendChild(dd);
    return row;
  }));
}

/* ── опрос ───────────────────────────────────────────────────────────────── */

async function fetchJSON(path) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), 8000);
  try {
    const resp = await fetch(BASE + path, { cache: 'no-store', signal: controller.signal });
    if (resp.status === 401 || resp.status === 403) {
      document.querySelector('.admin-panel').replaceChildren();
      location.replace(resp.status === 401 ? '/login?next=admin' : '/');
      throw new Error('Доступ завершён');
    }
    if (!resp.ok) throw new Error('панель ответила ' + resp.status);
    return await resp.json();
  } finally {
    clearTimeout(timer);
  }
}

async function loadStats() {
  if (state.statsPending) return;
  state.statsPending = true;
  try {
    const view = await fetchJSON('/api/stats');
    state.view = view;
    if (Number.isFinite(view.tz_offset)) state.tz = view.tz_offset;
    render(view);
    drawHostChart(state.samples);
  } finally {
    state.statsPending = false;
  }
}

async function loadHistory() {
  const request = ++state.historyRequest;
  const range = state.range;
  $('chart-host').setAttribute('aria-busy', 'true');
  try {
    const data = await fetchJSON('/api/history?range=' + encodeURIComponent(range));
    if (request !== state.historyRequest) return;
    state.samples = data.samples || [];
    state.historyStatus = '';
  } catch (err) {
    if (request !== state.historyRequest) return;
    state.samples = [];
    state.historyStatus = 'Не удалось загрузить историю. Выбери период для повтора; автоматический повтор — через минуту.';
  } finally {
    if (request === state.historyRequest) {
      $('chart-host').setAttribute('aria-busy', 'false');
      drawHostChart(state.samples);
    }
  }
}

function render(view) {
  renderFigures(view);
  renderWeb(view);
  renderResources(view);
  renderPlatforms(view);
  renderReach(view);
  renderTopGroups(view);
  renderDeps(view);
  renderSync(view);
  renderQueue(view);
  renderLog(view);
  renderFoot(view);
  drawDayTrack(view);
  if (view.rasp) drawNewChart(view.rasp.stats.users.new_by_day);
  else {
    for (const id of ['figures', 'day-track', 'chart-new', 'top-groups', 'top-deps', 'log']) {
      $(id).replaceChildren(el('p', 'empty', 'Данные расписания недоступны.'));
    }
    for (const id of ['platforms', 'reach', 'courses', 'counters']) $(id).replaceChildren();
    $('gone').replaceChildren();
    $('gone').hidden = true;
    for (const id of ['sync', 'queue']) pairs($(id), [['состояние', 'данные недоступны']]);
  }

  $('tz').textContent = tzLabel(state.tz);
  setHealth(view);
}

function setHealth(view) {
  const pulse = $('pulse');
  const fresh = $('freshness');

  if (view.rasp_error) {
    pulse.classList.add('is-down');
    fresh.classList.add('is-stale');
    fresh.textContent = view.rasp
      ? 'raspd молчит, данные ' + duration(view.rasp_age) + ' назад'
      : 'raspd недоступен';
    return;
  }
  pulse.classList.remove('is-down');
  fresh.classList.remove('is-stale');
  fresh.textContent = 'обновлено только что';
}

function failed(err) {
  $('pulse').classList.add('is-down');
  const fresh = $('freshness');
  fresh.classList.add('is-stale');
  fresh.textContent = err.name === 'AbortError'
    ? 'панель не ответила за 8 секунд · повторяю…'
    : 'панель не отвечает: ' + err.message;
}

/* ── часы ────────────────────────────────────────────────────────────────── */

/* Часы идут по времени вуза, а не браузера: расписание живёт по нему, и
   сверять «сейчас» с ним же — единственный способ не ошибиться на час. */
function tickClock() {
  $('clock').textContent = clockOf(Math.floor(Date.now() / 1000), state.tz, true);
}

/* ── запуск ──────────────────────────────────────────────────────────────── */

function bindRanges() {
  $('ranges').addEventListener('click', (ev) => {
    const button = ev.target.closest('button[data-range]');
    if (!button) return;
    for (const b of $('ranges').querySelectorAll('button')) {
      b.classList.toggle('is-on', b === button);
      b.setAttribute('aria-pressed', String(b === button));
    }
    state.range = button.dataset.range;
    state.samples = [];
    state.historyStatus = 'загружаю историю…';
    drawHostChart(state.samples);
    loadHistory();
  });
}

/* Перерисовка на изменение ширины: графики нарисованы в пикселях, потому что
   иначе подписи растягивались бы вместе с холстом и на телефоне становились
   нечитаемыми. */
function bindResize() {
  let timer = null;
  window.addEventListener('resize', () => {
    clearTimeout(timer);
    timer = setTimeout(() => {
      if (state.view) {
        drawDayTrack(state.view);
        if (state.view.rasp) drawNewChart(state.view.rasp.stats.users.new_by_day);
      }
      drawHostChart(state.samples);
    }, 150);
  });
}

function start() {
// Never restore a private dashboard from the browser's back/forward cache.
window.addEventListener('pagehide',()=>{document.querySelector('.admin-panel')?.replaceChildren();});
window.addEventListener('pageshow',e=>{if(e.persisted) location.reload();});
document.addEventListener('visibilitychange',()=>{if(!document.hidden) loadStats().catch(failed);});

  bindRanges();
  bindResize();

  tickClock();
  setInterval(tickClock, 1000);

  const poll = () => loadStats().catch(failed);
  poll();
  setInterval(poll, POLL_MS);

  drawHostChart(state.samples);
  loadHistory();
  setInterval(loadHistory, HISTORY_MS);
}

start();
