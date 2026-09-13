// Package api — контракт между raspd и ботами: HTTP+JSON поверх unix-сокета.
//
// Сокет вместо TCP выбран сознательно: ноль сетевого стека, права доступа
// раздаёт файловая система, отладка обычным `curl --unix-socket`. gRPC на
// такой нагрузке дал бы только кодоген и вес.
package api

import (
	"time"

	"github.com/UwUOcha/dairy-303-public/internal/schedule"
	"github.com/UwUOcha/dairy-303-public/internal/store"
)

// Пути эндпоинтов.
const (
	PathHealth      = "/health"
	PathGroupSearch = "/groups/search"
	PathDepartments = "/groups/departments"
	PathGroupList   = "/groups/list"
	PathGroup       = "/groups/get"
	PathSubgroups   = "/groups/subgroups"
	PathGroupTwins  = "/groups/twins"
	PathDay         = "/schedule/day"
	PathWeek        = "/schedule/week"
	PathNow         = "/schedule/now"
	PathUserGet     = "/users/get"
	PathUserSave    = "/users/save"
	PathUserMenu    = "/users/menu"
	PathNotifyList  = "/users/notify"
	PathNotifyMark  = "/users/notify/mark"
	PathNotifyHint  = "/users/notify/hinted"
	PathNotifyOff   = "/users/notify/off"
	PathChangeDates = "/changes/dates"
	PathChangeDay   = "/changes/day"
	PathOutboxTake  = "/outbox/take"
	PathOutboxDone  = "/outbox/done"
	PathOutboxFail  = "/outbox/fail"
	PathOutboxPut   = "/outbox/put"
	PathFeedbackAdd = "/feedback/add"
	PathFeedbackGet = "/feedback/get"
	PathFeedbackAns = "/feedback/answered"
	PathUsersCount  = "/users/count"
	PathProbeList   = "/users/probe"
	PathProbeMark   = "/users/probe/mark"
)

// Слова, которые понимает параметр date у /schedule/day наравне с ISO-датой.
// Сетка учебных дней известна только серверу, поэтому «следующий учебный
// день» считает он, а не бот.
const (
	DateToday = "today"
	DateNext  = "next"
)

// Error — тело ответа при ошибке.
type Error struct {
	Error string `json:"error"`
}

// Freshness описывает, насколько свежи показанные данные.
//
// Локальная копия — это ещё и устойчивость: если источник лёг, бот продолжает
// отвечать вчерашними данными с честной пометкой, а не молчит из-за чужого
// сервера.
type Freshness struct {
	// FetchedAt — когда эти месяцы последний раз успешно загружались.
	FetchedAt time.Time `json:"fetched_at"`
	// Stale — данные достаточно старые, чтобы предупредить пользователя.
	Stale bool `json:"stale"`
	// Missing — хотя бы один месяц диапазона не загружен; список может быть неполным.
	Missing bool `json:"missing"`
}

// Context — кому показывается расписание.
type Context struct {
	Group    store.Group     `json:"group"`
	Subgroup *store.Subgroup `json:"subgroup,omitempty"`
	// Today — сегодняшняя дата в часовом поясе вуза, ISO.
	Today string `json:"today"`
}

// DayResponse — расписание одного дня со всем, что нужно для навигации.
//
// Prev и Next считает сервер, а не бот: только здесь известна сетка учебных
// дней, и вычислять её в двух адаптерах заново — верный способ развести
// поведение телеграма и VK.
type DayResponse struct {
	Context
	Day  schedule.Day `json:"day"`
	Prev string       `json:"prev"`
	Next string       `json:"next"`
	// Grid — сетка звонков вуза. Нужна тем, кто рисует день целиком: без неё
	// нельзя отличить «первая пара в 10:25» от «к 8:30 всё-таки идти».
	Grid schedule.Grid `json:"grid,omitzero"`
	Freshness
}

// WeekResponse — расписание недели.
type WeekResponse struct {
	Context
	Week schedule.Week `json:"week"`
	// PrevMonday и NextMonday — понедельники соседних недель, ISO.
	PrevMonday string `json:"prev_monday"`
	NextMonday string `json:"next_monday"`
	ThisMonday string `json:"this_monday"`
	// Grid — сетка звонков вуза, см. DayResponse.Grid.
	Grid schedule.Grid `json:"grid,omitzero"`
	Freshness
}

// NowResponse — ответ на «что сейчас».
type NowResponse struct {
	Context
	Now schedule.Now `json:"now"`
	// Day — сегодняшний день целиком: пригодится, чтобы показать остаток.
	Day schedule.Day `json:"day"`
	Freshness
}

// ChangeDatesResponse — дни, в которых у группы недавно правили расписание.
type ChangeDatesResponse struct {
	Dates []string `json:"dates,omitempty"`
	// Today — сегодняшняя дата в часовом поясе вуза, ISO. Нужна боту, чтобы
	// подписать даты словами «сегодня» и «завтра», не спрашивая отдельно.
	Today string `json:"today"`
}

// ChangeDayResponse — один день до правки и после неё.
//
// «После» здесь всегда живое: это то, что лежит в базе прямо сейчас. Правку
// могли поправить ещё раз, пока человек читал сообщение, и показать ему
// отложенную копию значило бы соврать в тот единственный момент, ради
// которого он и нажал кнопку.
type ChangeDayResponse struct {
	Context
	Date   string       `json:"date"`
	Before schedule.Day `json:"before"`
	After  schedule.Day `json:"after"`
	// Known — снимок «до» ещё жив. Он живёт трое суток: кнопка из сообщения
	// недельной давности покажет только нынешний день, и это честнее, чем
	// выдавать его за неизменённый.
	Known bool `json:"known"`
	Freshness
}

// GroupsResponse — список групп.
type GroupsResponse struct {
	Groups []store.Group `json:"groups"`
	// Subgroup — подгруппа, названием которой оказался запрос («ГР-22/2»).
	// Приезжает только вместе с пустым списком групп: пока хоть одна группа
	// нашлась, разбираться в подгруппах незачем.
	Subgroup *store.Subgroup `json:"subgroup,omitempty"`
}

// DepartmentsResponse — подразделения вуза для обзора дерева.
type DepartmentsResponse struct {
	Departments []store.Department `json:"departments"`
}

// TwinsResponse — одноимённые записи каталога для одной группы.
type TwinsResponse struct {
	Twins []store.Twin `json:"twins"`
}

// SubgroupsResponse — подгруппы одной группы.
type SubgroupsResponse struct {
	Subgroups []store.Subgroup `json:"subgroups"`
}

// CoursesResponse — курсы подразделения.
type CoursesResponse struct {
	Courses []int `json:"courses"`
}

// UserResponse — пользователь и его группа одним ответом, чтобы бот не ходил
// за названием группы отдельным запросом на каждое сообщение.
type UserResponse struct {
	User     store.User      `json:"user"`
	Group    *store.Group    `json:"group,omitempty"`
	Subgroup *store.Subgroup `json:"subgroup,omitempty"`
	// GroupTwins — сколько записей каталога носит то же название, что и
	// группа пользователя. Больше одной — значит, есть между чем выбирать, и
	// в настройках появляется строка переключения копии.
	GroupTwins int `json:"group_twins,omitempty"`
	// Known — пользователь уже есть в базе.
	Known bool `json:"known"`
}

// NotifyResponse — кому и что пора слать в наступившую минуту.
type NotifyResponse struct {
	Targets []store.NotifyTarget `json:"targets"`
}

// ProbeResponse — порция адресатов, которых пора проверить на живость.
type ProbeResponse struct {
	Targets []store.ProbeTarget `json:"targets"`
}

// OutboxResponse — порция сообщений очереди для одной платформы.
type OutboxResponse struct {
	Items []store.OutboxItem `json:"items"`
}

// FeedbackResponse — что вышло из попытки принять обращение.
//
// Отказ по частоте — не ошибка запроса: бот на него отвечает человеку
// по-человечески, а не «сервис недоступен». Поэтому он приезжает полем, а не
// кодом ответа.
type FeedbackResponse struct {
	// ID — номер принятого обращения; ноль, если обращение отклонено.
	ID int64 `json:"id,omitempty"`
	// WaitSec — сколько секунд ждать до следующей попытки. Ноль означает, что
	// обращение принято.
	WaitSec int `json:"wait_sec,omitempty"`
	// Feedback — само обращение (ответ /feedback/get).
	Feedback *store.Feedback `json:"feedback,omitempty"`
}

// OutboxPutRequest — постановка одного сообщения в очередь.
type OutboxPutRequest struct {
	Platform string `json:"platform"`
	ExtID    string `json:"ext_id"`
	Kind     string `json:"kind"`
	DedupKey string `json:"dedup_key,omitempty"`
	Payload  string `json:"payload,omitempty"`
}

// CountResponse — одно число в ответ.
type CountResponse struct {
	Count int `json:"count"`
}

// HealthResponse — состояние демона.
//
// OK отвечает только за живость: база открыта, запросы обслуживаются.
// Сломанный фоновый синк живости не отменяет — локальная копия для того и
// держится, — поэтому он живёт в отдельном поле и нужен алертингу, а не
// ожиданию готовности.
type HealthResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	// SyncError — чем закончился последний проход фоновых контуров.
	SyncError string `json:"sync_error,omitempty"`
	Groups    int    `json:"groups"`
	Lessons   int    `json:"lessons"`
	FullSync  string `json:"full_sync,omitempty"`
}
