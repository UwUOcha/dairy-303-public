package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/UwUOcha/dairy-303-public/internal/profile"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// page — index.html с подставленными заголовками.
//
// Страница собирается на сервере, а не отдаётся файлом, по двум причинам.
// Первая: OG-разметке нужны абсолютные ссылки, иначе ссылка, кинутая в чат,
// разворачивается пустотой. Вторая: настройки приложения (демонстрация ли это)
// приезжают в теге, а не отдельным запросом — иначе первая отрисовка ждёт
// целый круг до сервера ради одного логического значения.
type page struct {
	template string
	config   string
	up       *upstream
	loc      *time.Location
}

func newPage(index []byte, demo bool, up *upstream) *page {
	cfg, _ := json.Marshal(publicConfig(demo))
	loc, err := time.LoadLocation(profile.Current().Timezone)
	if err != nil {
		loc = time.UTC
	}
	return &page{template: profile.Current().Render(string(index), true), config: string(cfg), up: up, loc: loc}
}

// render собирает страницу для запроса. Имя группы из ссылки попадает в
// заголовок и описание: ссылка на расписание, отправленная в чат, должна
// разворачиваться карточкой своей группы, а не общим слоганом сайта.
func (p *page) render(ctx context.Context, r *http.Request, origin string) string {
	query := r.URL.Query()
	groupID := groupFromQuery(query)
	brand := profile.Current()
	title, desc, canonical := brand.AppName+" · Расписание "+brand.University, brand.Description, origin+"/"
	if desc == "" {
		desc = "Расписание " + brand.University + ": неделя, преподаватели и аудитории."
	}
	if groupID > 0 {
		canonical = origin + "/?group=" + strconv.FormatInt(groupID, 10)
		if name := p.up.groupName(ctx, groupID); name != "" {
			title = name + " · Расписание " + brand.University
			desc = "Расписание группы " + name + ": неделя целиком, окна между парами, аудитории и преподаватели. И общее свободное время с друзьями из других групп."
		}
	}
	return strings.NewReplacer(
		"{{origin}}", html.EscapeString(origin),
		"{{canonical}}", html.EscapeString(canonical),
		"{{title}}", html.EscapeString(title),
		"{{description}}", html.EscapeString(desc),
		"{{config}}", html.EscapeString(p.config),
		"{{preload}}", p.preload(ctx, r, query, groupID),
	).Replace(p.template)
}

// preload вкладывает в страницу ту самую неделю, которую браузер сейчас
// запросит: расписание появляется вместе с разметкой, без круга до сервера.
//
// Подгруппу сервер знает только из ссылки — в куке лежит одна группа. Поэтому
// вкладываем неделю без фильтра по подгруппе, а клиент возьмёт её лишь тогда,
// когда его собственная подгруппа тоже не выбрана: показать чужие пары даже на
// один кадр хуже, чем показать пустой экран.
func (p *page) preload(ctx context.Context, r *http.Request, query url.Values, groupID int64) string {
	if groupID == 0 {
		if c, err := r.Cookie("mp_group"); err == nil {
			if id, err := strconv.ParseInt(c.Value, 10, 64); err == nil && id > 0 && id <= 1_000_000_000 {
				groupID = id
			}
		}
	}
	if groupID == 0 || query.Get("subject") != "" || query.Get("subject_name") != "" {
		return ""
	}
	var subgroupID int64
	if query.Has("group") {
		subgroupID = intFromQuery(query.Get("subgroup"))
	}
	body := p.up.week(ctx, groupID, subgroupID, p.monday(query))
	if body == "" {
		return ""
	}
	// В JSON голая «<» вне строк невозможна, поэтому замена безопасна и
	// закрывает единственный способ выйти из блока данных тегом.
	body = strings.ReplaceAll(body, "<", `\u003c`)
	// Отметка времени нужна из-за офлайн-кэша: service worker хранит страницу
	// целиком, и вложенная в неё неделя может оказаться вчерашней. Клиент
	// возьмёт её только пока она свежая, а иначе покажет свою копию — с честной
	// подписью о дате, чего вложенная неделя про себя сказать не умеет.
	return `<script type="application/json" id="app-week" data-at="` +
		strconv.FormatInt(time.Now().Unix(), 10) + `">` + body + `</script>`
}

// monday повторяет выбор недели, который делает страница: явная неделя, иначе
// неделя показываемого дня, иначе текущая по часовому поясу вуза.
func (p *page) monday(query url.Values) string {
	pick := time.Now().In(p.loc)
	for _, key := range []string{"week", "date"} {
		if t, err := time.ParseInLocation("2006-01-02", query.Get(key), p.loc); err == nil {
			pick = t
			break
		}
	}
	shift := (int(pick.Weekday()) + 6) % 7
	return pick.AddDate(0, 0, -shift).Format("2006-01-02")
}

// upstream — походы webd к приватному API ради самой страницы: имя группы для
// заголовка и неделя, вложенная в разметку. Браузер сюда не ходит.
//
// Имена групп помним недолго: без памяти каждый заход краулера и каждое
// открытие ссылки дёргали бы raspd. Отрицательный ответ тоже запоминается:
// перебор несуществующих номеров не должен превращаться в поток запросов.
type upstream struct {
	client  *http.Client
	enabled bool
	slots   chan struct{}

	mu    sync.Mutex
	names map[int64]groupName
}

type groupName struct {
	name string
	at   time.Time
}

const (
	groupNameTTL     = time.Hour
	groupMissTTL     = 5 * time.Minute
	groupNamesMax    = 512
	groupNameTimeout = 1500 * time.Millisecond
)

func newUpstream(socket string, enabled bool, slots chan struct{}) *upstream {
	return &upstream{
		enabled: enabled,
		slots:   slots,
		names:   map[int64]groupName{},
		client: &http.Client{Transport: &http.Transport{MaxIdleConns: 4, MaxIdleConnsPerHost: 4,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", socket)
			}}},
	}
}

func (g *upstream) groupName(ctx context.Context, id int64) string {
	if !g.enabled {
		return ""
	}
	g.mu.Lock()
	entry, ok := g.names[id]
	g.mu.Unlock()
	if ok {
		ttl := groupNameTTL
		if entry.name == "" {
			ttl = groupMissTTL
		}
		if time.Since(entry.at) < ttl {
			return entry.name
		}
	}
	// Заголовок страницы не стоит того, чтобы занимать последний слот работы
	// у настоящего расписания: не досталось — отдаём общий заголовок.
	select {
	case g.slots <- struct{}{}:
		defer func() { <-g.slots }()
	default:
		return entry.name
	}
	name := g.fetchName(ctx, id)
	g.mu.Lock()
	if len(g.names) >= groupNamesMax {
		g.names = map[int64]groupName{}
	}
	g.names[id] = groupName{name: name, at: time.Now()}
	g.mu.Unlock()
	return name
}

func (g *upstream) fetchName(ctx context.Context, id int64) string {
	ctx, cancel := context.WithTimeout(ctx, groupNameTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"http://rasp/groups/get?id="+strconv.FormatInt(id, 10), nil)
	if err != nil {
		return ""
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return ""
	}
	var body struct {
		Name string `json:"name"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 16<<10)).Decode(&body) != nil {
		return ""
	}
	name := strings.TrimSpace(body.Name)
	if r := []rune(name); len(r) > 64 {
		name = string(r[:64])
	}
	return name
}

// intFromQuery читает неотрицательное число из параметра ссылки.
func intFromQuery(raw string) int64 {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 || id > 1_000_000_000 {
		return 0
	}
	return id
}

// groupFromQuery достаёт номер группы из ссылки. Параметры страницы приходят
// от посетителя, поэтому всё, что не похоже на номер, просто игнорируется.
func groupFromQuery(raw url.Values) int64 { return intFromQuery(raw.Get("group")) }

// etag — сильный тег содержимого. Статика встроена в бинарь и не имеет даты
// изменения, поэтому без явного тега браузер качает css и js целиком при
// каждом открытии, а не получает 304.
func etag(body []byte) string {
	sum := sha256.Sum256(body)
	return `"` + hex.EncodeToString(sum[:12]) + `"`
}

// notModified отвечает 304, если у клиента уже есть эта версия.
func notModified(w http.ResponseWriter, r *http.Request, tag string) bool {
	w.Header().Set("ETag", tag)
	for _, candidate := range strings.Split(r.Header.Get("If-None-Match"), ",") {
		if strings.TrimSpace(candidate) == tag {
			w.WriteHeader(http.StatusNotModified)
			return true
		}
	}
	return false
}

// week берёт у приватного API неделю группы. Ждём недолго: страница важнее
// расписания в ней, а браузер всё равно запросит неделю сам.
func (g *upstream) week(ctx context.Context, groupID, subgroupID int64, monday string) string {
	if !g.enabled {
		return ""
	}
	select {
	case g.slots <- struct{}{}:
		defer func() { <-g.slots }()
	default:
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, weekTimeout)
	defer cancel()
	query := url.Values{
		"group":  {strconv.FormatInt(groupID, 10)},
		"monday": {monday},
	}
	if subgroupID > 0 {
		query.Set("subgroup", strconv.FormatInt(subgroupID, 10))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://rasp/schedule/week?"+query.Encode(), nil)
	if err != nil {
		return ""
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, weekLimit))
	if err != nil || !json.Valid(body) {
		return ""
	}
	return string(bytes.TrimSpace(body))
}

const (
	weekTimeout = 2 * time.Second
	weekLimit   = 512 << 10
)
