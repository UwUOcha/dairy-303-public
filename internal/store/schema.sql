-- Схема локальной копии расписания вуза.
--
-- Главное отличие от первоначального наброска архитектуры — связка
-- group_lessons. В наброске занятие принадлежало одной группе
-- (lessons.group_id), но реальные данные это опровергают: потоковое занятие
-- приходит с одним и тем же id в ленты всех групп потока, а его собственное
-- group_id указывает на якорную группу, которая может быть вовсе не той,
-- которую мы запрашивали. Поэтому занятие хранится один раз, а принадлежность
-- лентам групп вынесена в отношение M:N. Побочная выгода — экономия места:
-- лекция потока из четырёх групп лежит в одном экземпляре.

CREATE TABLE meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

-- ── справочник групп ────────────────────────────────────────────────────────
CREATE TABLE departments (
  id   INTEGER PRIMARY KEY,
  name TEXT NOT NULL
);

CREATE TABLE groups (
  id            INTEGER PRIMARY KEY,          -- внутренний ID
  name          TEXT    NOT NULL,
  department_id INTEGER NOT NULL DEFAULT 0,
  course        INTEGER NOT NULL DEFAULT 0,   -- курс действующей группы
  grad_year     INTEGER NOT NULL DEFAULT 0,   -- год выпуска архивной группы
  name_norm     TEXT    NOT NULL,             -- lower без дефисов и пробелов
  is_active     INTEGER NOT NULL DEFAULT 1,
  -- Признак скрытой группы приходит из нормализованного каталога адаптера.
  shadowed      INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_groups_norm ON groups(name_norm);
CREATE INDEX idx_groups_dep  ON groups(department_id, course);

-- Подгруппы. id глобальный по вузу (334, 335), а не порядковый внутри группы:
-- именно этим числом занятие адресуется в lessons.subgroup_id.
CREATE TABLE subgroups (
  id       INTEGER PRIMARY KEY,
  group_id INTEGER NOT NULL,
  name     TEXT    NOT NULL
);
CREATE INDEX idx_subgroups_group ON subgroups(group_id);

-- ── справочники ─────────────────────────────────────────────────────────────
-- Сырое занятие весит ~764 Б в основном из-за повторяющихся строк: названия
-- дисциплин, фамилии и аудитории встречаются десятки раз за месяц. Вынос в
-- справочники ужимает строку занятия примерно до 90 Б.
--
-- Здесь используются внутренние ключи. Строковые ID адаптера сопоставляются
-- с ними отдельно в provider_ids.
CREATE TABLE disciplines (
  id   INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL UNIQUE
);
CREATE TABLE staff (
  id       INTEGER PRIMARY KEY AUTOINCREMENT,
  name     TEXT NOT NULL,
  full_name TEXT NOT NULL DEFAULT '',
  degree TEXT NOT NULL DEFAULT ''
);
CREATE TABLE classrooms (
  id   INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL UNIQUE
);
CREATE TABLE class_types (
  id   INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL UNIQUE
);

-- Сетка звонков. Время — в минутах от полуночи: сравнение и вычисление окон
-- становятся обычной арифметикой.
CREATE TABLE lesson_times (
  id          INTEGER PRIMARY KEY,   -- внутренний ID
  minute_from INTEGER NOT NULL,
  minute_to   INTEGER NOT NULL,
  label       TEXT    NOT NULL       -- "12:20 - 13:55"
);

-- ── занятия ─────────────────────────────────────────────────────────────────
CREATE TABLE lessons (
  id              INTEGER PRIMARY KEY,        -- внутренний ID занятия
  date            TEXT    NOT NULL,           -- ISO 'YYYY-MM-DD'
  lesson_time_id  INTEGER NOT NULL DEFAULT 0,
  discipline_id   INTEGER,
  class_type_id   INTEGER,
  classroom_id    INTEGER,
  audience        INTEGER NOT NULL DEFAULT 0, -- 0 группа, 1 поток, 2 подгруппа, 3 сборный поток
  anchor_group_id INTEGER NOT NULL DEFAULT 0, -- группа занятия либо якорь потока
  subgroup_id     INTEGER NOT NULL DEFAULT 0, -- 0 = вся группа
  flow_number     INTEGER NOT NULL DEFAULT 0,
  audience_label  TEXT    NOT NULL DEFAULT '',
  flags           INTEGER NOT NULL DEFAULT 0, -- is_empty | self_work | remote | non_study
  comments        TEXT    NOT NULL DEFAULT '',
  topic           TEXT    NOT NULL DEFAULT '',
  superflow       TEXT    NOT NULL DEFAULT '' -- JSON-массив id участников сборного потока
);
CREATE INDEX idx_lessons_date ON lessons(date);  -- «свободные аудитории»

CREATE TABLE lesson_staff (
  lesson_id INTEGER NOT NULL REFERENCES lessons(id) ON DELETE CASCADE,
  staff_id  INTEGER NOT NULL,
  pos       INTEGER NOT NULL DEFAULT 0,   -- порядок как его дал upstream
  PRIMARY KEY (lesson_id, staff_id)
);
CREATE INDEX idx_lesson_staff_rev ON lesson_staff(staff_id);  -- расписание преподавателя

-- В чью ленту попало занятие. Дата продублирована сознательно: она позволяет
-- одному индексу (group_id, date) закрыть и выборку дня, и диапазон недели,
-- не заглядывая в таблицу занятий.
CREATE TABLE group_lessons (
  group_id  INTEGER NOT NULL,
  lesson_id INTEGER NOT NULL REFERENCES lessons(id) ON DELETE CASCADE,
  date      TEXT    NOT NULL,
  PRIMARY KEY (group_id, lesson_id)
);
CREATE INDEX idx_group_lessons_date ON group_lessons(group_id, date);
CREATE INDEX idx_group_lessons_rev  ON group_lessons(lesson_id);

-- ── состояние синхронизации ─────────────────────────────────────────────────
CREATE TABLE month_state (
  group_id     INTEGER NOT NULL,
  year         INTEGER NOT NULL,
  month        INTEGER NOT NULL,
  content_hash TEXT    NOT NULL,   -- хэш нормализованного набора занятий
  -- Отдельный хэш по «ещё не прошедшей» части месяца и граница, на которой он
  -- считался. По нему решается, стоит ли беспокоить подписчиков: вуз
  -- постоянно правит прошедшие дни, и новость о правке пары двухнедельной
  -- давности — чистый шум. Граница хранится потому, что она едет вперёд
  -- каждый день, и сравнивать отпечатки можно только на одной и той же.
  future_hash  TEXT    NOT NULL DEFAULT '',
  future_from  TEXT    NOT NULL DEFAULT '',
  fetched_at   INTEGER NOT NULL,   -- unix ts последнего успешного запроса
  changed_at   INTEGER NOT NULL,   -- когда хэш последний раз изменился
  lesson_count INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (group_id, year, month)
);
CREATE INDEX idx_month_state_fetched ON month_state(fetched_at);

-- ── пользователи ────────────────────────────────────────────────────────────
CREATE TABLE users (
  platform    TEXT    NOT NULL,            -- 'tg' | 'vk'
  ext_id      TEXT    NOT NULL,
  group_id    INTEGER NOT NULL DEFAULT 0,
  subgroup_id INTEGER NOT NULL DEFAULT 0,
  tz_offset   INTEGER NOT NULL DEFAULT 0,
  tz_name TEXT NOT NULL DEFAULT '',
  -- Уведомления. Главный выключатель отдельно от каждой рассылки: раньше его
  -- роль играло notify_at = NULL, и выключить утреннее сообщение, оставив
  -- новости о правках расписания, было нельзя — а это разные вещи.
  notify_on   INTEGER NOT NULL DEFAULT 1,  -- бот вообще пишет сам
  morning_on  INTEGER NOT NULL DEFAULT 1,  -- расписание на сегодня
  morning_at  INTEGER NOT NULL DEFAULT 420,   -- 07:00, минуты от полуночи
  evening_on  INTEGER NOT NULL DEFAULT 0,  -- расписание на следующий учебный день
  evening_at  INTEGER NOT NULL DEFAULT 1110,  -- 18:30
  -- Писать и в пустые дни. Выключено по умолчанию: «занятий нет» — это
  -- сообщение, которое человек не просил, и в выходные оно приходит зря.
  -- Чтобы молчание не выглядело поломкой, в первый же пустой день бот один
  -- раз объясняет, где это включается (см. empty_hinted).
  empty_on    INTEGER NOT NULL DEFAULT 0,
  empty_hinted INTEGER NOT NULL DEFAULT 0,
  changes_on  INTEGER NOT NULL DEFAULT 1,  -- сообщать о правках расписания
  -- Даты последних рассылок в локальном дне пользователя. Защита от
  -- повторной отправки: без них перезапуск botd в ту же минуту будит человека
  -- вторым сообщением.
  notified_on     TEXT NOT NULL DEFAULT '',
  notified_eve_on TEXT NOT NULL DEFAULT '',
  -- Чего бот ждёт от человека текстом ('' | 'tm' | 'te' | 'fb' | 'an:<id>').
  -- Единственное состояние диалога во всей системе: время нельзя выбрать
  -- кнопкой, не превратив клавиатуру в циферблат, а обращение к автору — это
  -- по определению свободный текст.
  await       TEXT    NOT NULL DEFAULT '',
  -- Нижнее меню человеку уже показывали. Признак живёт в базе, а не в памяти
  -- процесса: клавиатура телеграма остаётся у клиента навсегда, и без отметки
  -- каждый перезапуск botd слал бы всем лишнее «меню всегда внизу».
  menu_sent   INTEGER NOT NULL DEFAULT 0,
  menu_version TEXT NOT NULL DEFAULT '',
  created_at  INTEGER NOT NULL,
  last_seen   INTEGER NOT NULL,
  -- Человек заблокировал бота: адресат недостижим навсегда, и уведомления ему
  -- выключены не им самим, а нами. Признак отдельно от notify_on = 0 именно
  -- поэтому: по одному выключателю «выключил сам» и «выкинул из диалога»
  -- неразличимы, а это разные новости — и для панели, и для того, стоит ли
  -- возвращать человеку рассылку, когда он вернётся.
  blocked_at  INTEGER NOT NULL DEFAULT 0,
  -- Когда доступность адресата проверяли живым запросом к площадке. Хранится
  -- у каждого свой, а не одной датой обхода на всех: так недельная проверка
  -- размазывается ровно и переживает перезапуск botd, не начиная круг заново.
  probed_at   INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (platform, ext_id)
);
CREATE INDEX idx_users_group  ON users(group_id);   -- «горячие» группы для синка
CREATE INDEX idx_users_notify ON users(notify_on) WHERE notify_on = 1;
CREATE INDEX idx_users_probe  ON users(platform, probed_at);


-- ── очередь исходящих ───────────────────────────────────────────────────────
-- Рассылки, инициированные не человеком, а событием: расписание правили, и об
-- этом надо сказать. Очередь в базе, а не в памяти botd, потому что событие
-- рождается в raspd, а доставляет его другой процесс — и перезапуск любого из
-- двух не должен терять сообщение.
--
-- dedup_key схлопывает повторы: за день месяц могут поправить трижды, а
-- человеку хватит одного сообщения, пока он его не получил.
CREATE TABLE outbox (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  platform    TEXT    NOT NULL,
  ext_id      TEXT    NOT NULL,
  kind        TEXT    NOT NULL,            -- 'change'
  dedup_key   TEXT    NOT NULL DEFAULT '',
  payload     TEXT    NOT NULL DEFAULT '', -- JSON
  attempts    INTEGER NOT NULL DEFAULT 0,
  next_try_at INTEGER NOT NULL,
  created_at  INTEGER NOT NULL,
  UNIQUE(platform, ext_id, kind, dedup_key)
);
CREATE INDEX idx_outbox_ready ON outbox(platform, next_try_at);

-- ── снимки изменённых дней ──────────────────────────────────────────────────
-- Отпечаток месяца отвечает «правили или нет», но не «что именно»: хэш
-- необратим. Поэтому в момент правки откладывается копия задетых дней в том
-- виде, в каком человек их видел, — из неё собирается «до/после» по кнопке
-- «подробнее». «После» хранить незачем: оно и так лежит в lessons.
--
-- Таблица заведомо маленькая и живёт трое суток: снимки чистит тот же
-- уборщик, что и очередь исходящих.
CREATE TABLE change_days (
  group_id    INTEGER NOT NULL,
  date        TEXT    NOT NULL,           -- ISO 'YYYY-MM-DD'
  before_json TEXT    NOT NULL,           -- Ссылка {revision_id}; старые записи — JSON-массив занятий дня
  created_at  INTEGER NOT NULL,
  PRIMARY KEY (group_id, date)
);
CREATE INDEX idx_change_days_created ON change_days(created_at);

-- ── обратная связь ──────────────────────────────────────────────────────────
-- Обращения из кнопки «Написать автору» и отметка о том, что на них ответили.
--
-- Хранятся не ради архива, а ради ответа: сообщение с обращением живёт в
-- переписке автора вечно, и кнопка «ответить» под ним обязана работать через
-- месяц. Держать адресата в callback-данных нельзя — там 64 байта на всё, а
-- пара «платформа + ext_id» вместе с текстом туда не влезает.
CREATE TABLE feedback (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  platform    TEXT    NOT NULL,            -- откуда написали: 'tg' | 'vk'
  ext_id      TEXT    NOT NULL,
  group_name  TEXT    NOT NULL DEFAULT '', -- пусто у тех, кто ещё не выбрал группу
  text        TEXT    NOT NULL,
  created_at  INTEGER NOT NULL,
  answered_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_feedback_created ON feedback(created_at);
-- Индекс под троттлинг: перед приёмом обращения смотрим, когда этот же
-- человек писал в прошлый раз.
CREATE INDEX idx_feedback_author ON feedback(platform, ext_id, created_at);

CREATE TABLE IF NOT EXISTS schedule_revisions (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 group_id INTEGER NOT NULL,
 monday TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 before_at INTEGER NOT NULL,
 before_json TEXT NOT NULL,
 after_json TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_schedule_revisions_week ON schedule_revisions(group_id, monday, id DESC);
CREATE INDEX IF NOT EXISTS idx_schedule_revisions_expiry ON schedule_revisions(created_at);


CREATE TABLE staff_accounts (
 account_id INTEGER PRIMARY KEY,
 staff_id INTEGER NOT NULL REFERENCES staff(id) ON DELETE CASCADE
);
CREATE INDEX idx_staff_accounts_person ON staff_accounts(staff_id);
CREATE TABLE staff_departments (
 staff_id INTEGER NOT NULL REFERENCES staff(id) ON DELETE CASCADE,
 department TEXT NOT NULL,
 PRIMARY KEY(staff_id, department)
);
CREATE TABLE staff_aliases (
 id INTEGER PRIMARY KEY,
 staff_id INTEGER NOT NULL REFERENCES staff(id) ON DELETE CASCADE
);
CREATE TABLE staff_vacancies (account_id INTEGER PRIMARY KEY);
-- Missing upstream IDs have only a lesson-local identity, never initials as a key.
CREATE TABLE staff_unresolved (
 lesson_id INTEGER NOT NULL REFERENCES lessons(id) ON DELETE CASCADE,
 pos INTEGER NOT NULL,
 staff_id INTEGER NOT NULL REFERENCES staff(id) ON DELETE CASCADE,
 PRIMARY KEY(lesson_id,pos)
);


CREATE TABLE IF NOT EXISTS web_allowlist(platform TEXT NOT NULL, ext_id TEXT NOT NULL, added_at INTEGER NOT NULL, PRIMARY KEY(platform,ext_id));
CREATE TABLE IF NOT EXISTS web_challenges(id TEXT PRIMARY KEY, browser TEXT NOT NULL, platform TEXT NOT NULL, ext_id TEXT NOT NULL DEFAULT '', code TEXT NOT NULL DEFAULT '', attempts INTEGER NOT NULL DEFAULT 0, expires INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS web_sessions(id TEXT PRIMARY KEY, token TEXT NOT NULL UNIQUE, platform TEXT NOT NULL, ext_id TEXT NOT NULL, label TEXT NOT NULL, created INTEGER NOT NULL, expires INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS web_sessions_owner ON web_sessions(platform,ext_id);
-- Единственный личный API-ключ администратора; хранится только SHA-256.
CREATE TABLE bot_api_key (
  singleton INTEGER PRIMARY KEY CHECK(singleton = 1),
  platform TEXT NOT NULL CHECK(platform = 'tg'),
  ext_id TEXT NOT NULL CHECK(ext_id <> ''),
  token_hash TEXT NOT NULL,
  created_at INTEGER NOT NULL
);

CREATE TABLE provider_ids(source TEXT NOT NULL,kind TEXT NOT NULL,external_id TEXT NOT NULL,internal_id INTEGER NOT NULL,PRIMARY KEY(source,kind,external_id),UNIQUE(kind,internal_id));
