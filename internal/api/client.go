package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/UwUOcha/dairy-303-public/internal/store"
)

// ErrNotFound — запрошенной сущности нет.
var ErrNotFound = errors.New("api: не найдено")

// Client — типизированный доступ к raspd по unix-сокету.
type Client struct {
	http *http.Client
}

// NewClient собирает клиент поверх unix-сокета.
//
// Хост в URL не имеет значения — соединение всегда идёт в файл сокета, — но
// http.Client требует синтаксически корректный адрес, поэтому используется
// заглушка.
func NewClient(socketPath string) *Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	return &Client{
		http: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return dialer.DialContext(ctx, "unix", socketPath)
				},
			},
		},
	}
}

const baseURL = "http://rasp"

func (c *Client) get(ctx context.Context, path string, q url.Values, dst any) error {
	target := baseURL + path
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	return c.do(req, dst)
}

func (c *Client) post(ctx context.Context, path string, body, dst any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, dst)
}

func (c *Client) do(req *http.Request, dst any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("api: запрос к raspd: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		var e Error
		if err := json.NewDecoder(resp.Body).Decode(&e); err == nil && e.Error != "" {
			return fmt.Errorf("api: raspd вернул %d: %s", resp.StatusCode, e.Error)
		}
		return fmt.Errorf("api: raspd вернул %d", resp.StatusCode)
	}
	if dst == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(dst)
}

func idParams(pairs map[string]int64) url.Values {
	q := url.Values{}
	for k, v := range pairs {
		if v != 0 {
			q.Set(k, strconv.FormatInt(v, 10))
		}
	}
	return q
}

// Health проверяет состояние raspd.
func (c *Client) Health(ctx context.Context) (HealthResponse, error) {
	var out HealthResponse
	err := c.get(ctx, PathHealth, nil, &out)
	return out, err
}

// SearchGroups ищет группы по названию.
//
// Ответ отдаётся целиком, а не одним списком: когда групп не нашлось, в нём
// может лежать подгруппа, названием которой и был запрос.
func (c *Client) SearchGroups(ctx context.Context, query string, limit int) (GroupsResponse, error) {
	q := url.Values{"q": {query}, "limit": {strconv.Itoa(limit)}}
	var out GroupsResponse
	err := c.get(ctx, PathGroupSearch, q, &out)
	return out, err
}

// Departments отдаёт подразделения вуза.
func (c *Client) Departments(ctx context.Context) ([]store.Department, error) {
	var out DepartmentsResponse
	err := c.get(ctx, PathDepartments, nil, &out)
	return out.Departments, err
}

// Courses отдаёт курсы подразделения.
func (c *Client) Courses(ctx context.Context, departmentID int64) ([]int, error) {
	var out CoursesResponse
	err := c.get(ctx, PathGroupList, idParams(map[string]int64{"department": departmentID}), &out)
	return out.Courses, err
}

// GroupsOf отдаёт группы подразделения на курсе.
func (c *Client) GroupsOf(ctx context.Context, departmentID int64, course int) ([]store.Group, error) {
	q := idParams(map[string]int64{"department": departmentID, "course": int64(course)})
	var out GroupsResponse
	err := c.get(ctx, PathGroupList, q, &out)
	return out.Groups, err
}

// Group отдаёт группу по id.
func (c *Client) Group(ctx context.Context, id int64) (store.Group, error) {
	var out store.Group
	err := c.get(ctx, PathGroup, idParams(map[string]int64{"id": id}), &out)
	return out, err
}

// Subgroups отдаёт подгруппы группы.
func (c *Client) Subgroups(ctx context.Context, groupID int64) ([]store.Subgroup, error) {
	var out SubgroupsResponse
	err := c.get(ctx, PathSubgroups, idParams(map[string]int64{"group": groupID}), &out)
	return out.Subgroups, err
}

// GroupTwins отдаёт одноимённые записи каталога для группы.
func (c *Client) GroupTwins(ctx context.Context, groupID int64) ([]store.Twin, error) {
	var out TwinsResponse
	err := c.get(ctx, PathGroupTwins, idParams(map[string]int64{"group": groupID}), &out)
	return out.Twins, err
}

// Day отдаёт расписание дня. Пустая дата означает «сегодня».
func (c *Client) Day(ctx context.Context, groupID, subgroupID int64, date string) (DayResponse, error) {
	q := idParams(map[string]int64{"group": groupID, "subgroup": subgroupID})
	if date != "" {
		q.Set("date", date)
	}
	var out DayResponse
	err := c.get(ctx, PathDay, q, &out)
	return out, err
}

// Week отдаёт расписание недели. Пустая дата означает текущую неделю.
func (c *Client) Week(ctx context.Context, groupID, subgroupID int64, monday string) (WeekResponse, error) {
	q := idParams(map[string]int64{"group": groupID, "subgroup": subgroupID})
	if monday != "" {
		q.Set("monday", monday)
	}
	var out WeekResponse
	err := c.get(ctx, PathWeek, q, &out)
	return out, err
}

// Now отвечает, что идёт сейчас и что дальше, в часовом поясе пользователя.
// Время ответа и занятий — в поясе вуза; tzOffset сохранён для совместимости вызовов.
func (c *Client) Now(ctx context.Context, groupID, subgroupID int64, tzOffset int) (NowResponse, error) {
	q := idParams(map[string]int64{"group": groupID, "subgroup": subgroupID, "tz": int64(tzOffset)})
	var out NowResponse
	err := c.get(ctx, PathNow, q, &out)
	return out, err
}

// ChangedDates отдаёт дни, в которых у группы недавно правили расписание.
func (c *Client) ChangedDates(ctx context.Context, groupID int64) (ChangeDatesResponse, error) {
	var out ChangeDatesResponse
	err := c.get(ctx, PathChangeDates, idParams(map[string]int64{"group": groupID}), &out)
	return out, err
}

// ChangedDay отдаёт один день до правки и после неё.
func (c *Client) ChangedDay(ctx context.Context, groupID, subgroupID int64, date string) (ChangeDayResponse, error) {
	q := idParams(map[string]int64{"group": groupID, "subgroup": subgroupID})
	q.Set("date", date)
	var out ChangeDayResponse
	err := c.get(ctx, PathChangeDay, q, &out)
	return out, err
}

// User отдаёт пользователя вместе с его группой и подгруппой.
func (c *Client) User(ctx context.Context, platform, extID string) (UserResponse, error) {
	q := url.Values{"platform": {platform}, "ext_id": {extID}}
	var out UserResponse
	err := c.get(ctx, PathUserGet, q, &out)
	return out, err
}

// SaveUser сохраняет настройки пользователя.
func (c *Client) SaveUser(ctx context.Context, u store.User) error {
	return c.post(ctx, PathUserSave, u, nil)
}

// UsersToNotify отдаёт тех, у кого наступила минута рассылки, вместе с тем,
// какая именно это рассылка. minute — минута суток по UTC.
func (c *Client) UsersToNotify(ctx context.Context, minute int) ([]store.NotifyTarget, error) {
	q := url.Values{"minute": {strconv.Itoa(minute)}}
	var out NotifyResponse
	err := c.get(ctx, PathNotifyList, q, &out)
	return out.Targets, err
}

// MarkMenuSent запоминает, что человеку показали нижнее меню.
func (c *Client) MarkMenuSent(ctx context.Context, platform, extID string, version ...string) error {
	q := url.Values{"platform": {platform}, "ext_id": {extID}}
	if len(version) > 0 {
		q.Set("version", version[0])
	}
	return c.get(ctx, PathUserMenu, q, nil)
}

// MarkNotified отмечает успешную отправку, чтобы не написать дважды.
func (c *Client) MarkNotified(ctx context.Context, platform, extID string, kind store.NotifyKind, localDate string) error {
	q := url.Values{
		"platform": {platform}, "ext_id": {extID},
		"kind": {string(kind)}, "date": {localDate},
	}
	return c.get(ctx, PathNotifyMark, q, nil)
}

// MarkEmptyHinted запоминает, что разовую подсказку про пустые дни человек
// получил.
func (c *Client) MarkEmptyHinted(ctx context.Context, platform, extID string) error {
	q := url.Values{"platform": {platform}, "ext_id": {extID}}
	return c.get(ctx, PathNotifyHint, q, nil)
}

// DisableNotify выключает уведомления адресату, который заблокировал бота.
func (c *Client) DisableNotify(ctx context.Context, platform, extID string) error {
	q := url.Values{"platform": {platform}, "ext_id": {extID}}
	return c.get(ctx, PathNotifyOff, q, nil)
}

// ── проверка живости адресатов ──────────────────────────────────────────────

// UsersToProbe забирает порцию адресатов, которых пора проверить.
func (c *Client) UsersToProbe(ctx context.Context, platform string, limit int) ([]store.ProbeTarget, error) {
	q := url.Values{"platform": {platform}, "limit": {strconv.Itoa(limit)}}
	var out ProbeResponse
	err := c.get(ctx, PathProbeList, q, &out)
	return out.Targets, err
}

// MarkProbed записывает исход проверки одного адресата.
func (c *Client) MarkProbed(ctx context.Context, platform, extID string, reachable bool) error {
	q := url.Values{"platform": {platform}, "ext_id": {extID}, "reachable": {"0"}}
	if reachable {
		q.Set("reachable", "1")
	}
	return c.get(ctx, PathProbeMark, q, nil)
}

// ── очередь исходящих ───────────────────────────────────────────────────────

// TakeOutbox забирает готовые к отправке сообщения своей платформы.
func (c *Client) TakeOutbox(ctx context.Context, platform string, limit int) ([]store.OutboxItem, error) {
	q := url.Values{"platform": {platform}, "limit": {strconv.Itoa(limit)}}
	var out OutboxResponse
	err := c.get(ctx, PathOutboxTake, q, &out)
	return out.Items, err
}

// OutboxDone подтверждает доставку и убирает сообщение из очереди.
func (c *Client) OutboxDone(ctx context.Context, id int64, expected ...string) error {
	if len(expected) > 0 {
		return c.post(ctx, PathOutboxDone+"?id="+strconv.FormatInt(id, 10), map[string]string{"payload": expected[0]}, nil)
	}
	q := url.Values{"id": {strconv.FormatInt(id, 10)}}
	return c.get(ctx, PathOutboxDone, q, nil)
}

// OutboxFail откладывает повторную попытку на after.
func (c *Client) OutboxFail(ctx context.Context, id int64, after time.Duration) error {
	q := url.Values{
		"id":    {strconv.FormatInt(id, 10)},
		"after": {strconv.Itoa(int(after / time.Second))},
	}
	return c.get(ctx, PathOutboxFail, q, nil)
}

// PutOutbox ставит в очередь одно сообщение конкретному адресату.
func (c *Client) PutOutbox(ctx context.Context, req OutboxPutRequest) error {
	return c.post(ctx, PathOutboxPut, req, nil)
}

// ── обратная связь ──────────────────────────────────────────────────────────

// AddFeedback передаёт обращение и возвращает его номер.
//
// Второе возвращаемое значение — сколько ждать, если человек пишет слишком
// часто. Ненулевая пауза означает, что обращение не принято и номера у него
// нет: это не ошибка, а ответ, который боту надо показать человеку словами.
func (c *Client) AddFeedback(ctx context.Context, f store.Feedback) (int64, time.Duration, error) {
	var out FeedbackResponse
	if err := c.post(ctx, PathFeedbackAdd, f, &out); err != nil {
		return 0, 0, err
	}
	return out.ID, time.Duration(out.WaitSec) * time.Second, nil
}

// Feedback читает обращение по номеру.
func (c *Client) Feedback(ctx context.Context, id int64) (store.Feedback, error) {
	q := url.Values{"id": {strconv.FormatInt(id, 10)}}
	var out FeedbackResponse
	if err := c.get(ctx, PathFeedbackGet, q, &out); err != nil {
		return store.Feedback{}, err
	}
	if out.Feedback == nil {
		return store.Feedback{}, ErrNotFound
	}
	return *out.Feedback, nil
}

// MarkAnswered отмечает, что на обращение ответили.
func (c *Client) MarkAnswered(ctx context.Context, id int64) error {
	q := url.Values{"id": {strconv.FormatInt(id, 10)}}
	return c.get(ctx, PathFeedbackAns, q, nil)
}

// CountUsers считает тех, кто довёл настройку до конца. Нужна экрану
// «О проекте».
func (c *Client) CountUsers(ctx context.Context) (int, error) {
	var out CountResponse
	err := c.get(ctx, PathUsersCount, nil, &out)
	return out.Count, err
}

// ── диагностика ─────────────────────────────────────────────────────────────

// Stats забирает снимок системы для админ-панели.
func (c *Client) Stats(ctx context.Context) (StatsResponse, error) {
	var out StatsResponse
	err := c.get(ctx, PathStats, nil, &out)
	return out, err
}

// ReportBot отдаёт демону расписания счётчики бота.
//
// Ошибка здесь не должна ронять ни один сценарий: статистика — это удобство, а
// не работа бота. Вызывающий её логирует и живёт дальше.
func (c *Client) ReportBot(ctx context.Context, rep BotReport) error {
	return c.post(ctx, PathStatsBot, rep, nil)
}

func (c *Client) NextLesson(ctx context.Context, group, subgroup int64) (NextLessonResponse, error) {
	var out NextLessonResponse
	err := c.get(ctx, PathNextLesson, idParams(map[string]int64{"group": group, "subgroup": subgroup}), &out)
	return out, err
}

func (c *Client) ChangeSummary(ctx context.Context, req ChangeSummaryRequest) (ChangeSummaryResponse, error) {
	var out ChangeSummaryResponse
	err := c.post(ctx, PathChangeSummary, req, &out)
	return out, err
}

// NextStudyDay finds the first day with lessons starting tomorrow in university time.
func (c *Client) NextStudyDay(ctx context.Context, group, subgroup int64) (NextLessonResponse, error) {
	var out NextLessonResponse
	params := idParams(map[string]int64{"group": group, "subgroup": subgroup})
	params.Set("from", "tomorrow")
	err := c.get(ctx, PathNextLesson, params, &out)
	return out, err
}
