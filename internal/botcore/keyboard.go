package botcore

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/UwUOcha/dairy-303-public/internal/api"
	"github.com/UwUOcha/dairy-303-public/internal/schedule"
	"github.com/UwUOcha/dairy-303-public/internal/store"
)

// Префиксы callback-данных. Телеграм отводит на них 64 байта, чего с запасом
// хватает на префикс и ISO-дату.
const (
	cbDay        = "d" // d:<ГГГГ-ММ-ДД>
	cbWeek       = "w" // w:<понедельник>
	cbNextLesson = "nextlesson"
	cbNow        = "now" //
	cbSettings   = "set"
	cbClose      = "close"
	cbGroupPick  = "g"  // g:<id группы>
	cbGroupEdit  = "ge" // начать смену группы
	cbBrowse     = "br" // br | br:<подразделение> | br:<подразделение>:<курс>; хвостом — «pN»
	cbSubPick    = "s"  // s:<id подгруппы>, 0 — вся группа
	cbSubEdit    = "se" // открыть выбор подгруппы
	cbTwinMen    = "tw" // меню копий группы
	cbTwinPick   = "tp" // tp:<id копии>
	cbNotifyMen  = "nm" // вкладка уведомлений
	cbNotifyAll  = "na" // переключить главный выключатель
	cbNotifyMor  = "no" // переключить утреннее сообщение
	cbNotifyEve  = "nv" // переключить вечернее сообщение
	cbNotifyEmp  = "ne" // переключить сообщения о пустых днях
	cbNotifyChg  = "nc" // переключить новости о правках расписания
	// cbChangesSet — «не сообщать о правках» из самой новости и возврат из
	// подтверждения: ncs:off | ncs:on. Не переключатель, в отличие от кнопки
	// настроек: эти сообщения живут в переписке вечно, и нажатие спустя месяц
	// не должно молча вернуть рассылку, от которой человек уже отписался.
	cbChangesSet = "ncs"
	// cbMorningSet — «не писать по утрам» из самой рассылки и возврат из
	// подтверждения: nos:off | nos:on. Тоже не переключатель: утреннее
	// расписание остаётся в переписке навсегда, и нажатие на позавчерашнее
	// сообщение должно делать ровно то, что написано на кнопке.
	cbMorningSet = "nos"
	cbNotifyAsk  = "nt" // nt:m | nt:e — попросить время текстом
	cbNotifySet  = "n"  // n:m:<минуты> | n:e:<минуты>; n:<минуты> и n:off — из старых сообщений
	cbAdopt      = "ad" // ad:<id группы> — сделать просматриваемую группу своей
	cbChanges    = "cl" // cl:<id группы> — список недавно изменённых дней
	cbChangeDay  = "cd" // cd:<id группы>:<дата> — «до/после» одного дня
	cbMore       = "more"
	cbAbout      = "about"
	cbHelp       = "help"
	cbFeedback   = "fb"    // попросить написать автору
	cbShare      = "share" // прислать сообщение для пересылки друзьям
	cbAnswer     = "ans"   // ans:<номер обращения> — ответить, только автору
)

// Просмотр чужой группы кодируется третьей частью callback-данных: «d:<дата>»
// — это своё расписание, «d:<дата>:<группа>» — гостевое. Дальше по цепочке
// кнопок группа едет с каждым нажатием, поэтому листать чужую неделю можно
// сколько угодно, а привязка остаётся своей. Никакого состояния диалога — то
// же правило, что и с абсолютными датами.

// cbView собирает callback-данные, дописывая группу, если смотрим чужое.
func cbView(prefix, arg string, guest int64) string {
	if guest == 0 {
		return cb(prefix, arg)
	}
	return cb(prefix, arg, strconv.FormatInt(guest, 10))
}

// guestRow — что предложить человеку в гостях: забрать группу себе или
// вернуться к своей.
func guestRow(guest int64) []Button {
	return []Button{
		{Label: "✅ Сделать моей", Data: cb(cbAdopt, strconv.FormatInt(guest, 10))},
		{Label: "◀️ К своей", Data: cb(cbDay, todayArg)},
	}
}

// todayArg — «сегодня» словом. Какая сегодня дата, знает raspd: у него
// часовой пояс вуза.
const todayArg = "today"

// cb собирает callback-данные из частей.
func cb(parts ...string) string { return strings.Join(parts, ":") }

// parseCB разбирает callback-данные на префикс и аргументы.
func parseCB(data string) (string, []string) {
	parts := strings.Split(data, ":")
	if len(parts) == 0 {
		return "", nil
	}
	return parts[0], parts[1:]
}

// DayKeyboard — навигация под расписанием дня.
//
// В callback-данные кладётся абсолютная дата, а не смещение от «сегодня».
// Смещения были источником самого живучего бага прошлой версии: стрелка
// «вперёд» срабатывала со второго раза, потому что смещение считалось от
// сегодняшнего дня, а показанная дата успевала уехать после пропуска
// воскресенья. С абсолютной датой каждое нажатие самодостаточно.
func DayKeyboard(r api.DayResponse, guest int64) *Keyboard {
	middle := Button{Label: "🔄 Сегодня", Data: cbView(cbDay, r.Today, guest)}
	if r.Day.Date == r.Today {
		// Уже на сегодня — кнопка работает как «обновить», и подпись не должна
		// обещать переход.
		middle.Label = "🔄 Обновить"
	}
	k := &Keyboard{Rows: [][]Button{
		{
			{Label: "◀️", Data: cbView(cbDay, r.Prev, guest)},
			middle,
			{Label: "▶️", Data: cbView(cbDay, r.Next, guest)},
		},
		{
			{Label: MenuWeek, Data: cbView(cbWeek, weekAnchor(r), guest)},
		},
	}}
	if guest != 0 {
		k.Rows = append(k.Rows, guestRow(guest))
	}
	return k
}

// weekAnchor — какую неделю открыть из показанного дня. Переход должен вести
// в неделю просматриваемой даты, а не в текущую.
func weekAnchor(r api.DayResponse) string { return r.Day.Date }

// WeekKeyboard — навигация под расписанием недели.
func WeekKeyboard(r api.WeekResponse, guest int64) *Keyboard {
	middle := Button{Label: "🔄 Эта неделя", Data: cbView(cbWeek, r.ThisMonday, guest)}
	if r.Week.Monday == r.ThisMonday {
		middle.Label = "🔄 Обновить"
	}
	k := &Keyboard{Rows: [][]Button{
		{
			{Label: "◀️", Data: cbView(cbWeek, r.PrevMonday, guest)},
			middle,
			{Label: "▶️", Data: cbView(cbWeek, r.NextMonday, guest)},
		},
		{
			{Label: "📅 День", Data: cbView(cbDay, dayAnchor(r), guest)},
		},
	}}
	if guest != 0 {
		k.Rows = append(k.Rows, guestRow(guest))
	}
	return k
}

// dayAnchor — в какой день вернуться из недели: в сегодня, если оно на этой
// неделе, иначе в её понедельник.
func dayAnchor(r api.WeekResponse) string {
	for _, d := range r.Week.Days {
		if d.Date == r.Today {
			return r.Today
		}
	}
	return r.Week.Monday
}

// SettingsKeyboard — меню настроек.
//
// Каждая строка сразу показывает текущее значение: человек видит, на какую
// группу и подгруппу он подписан, не открывая вложенных меню.
func SettingsKeyboard(u api.UserResponse) *Keyboard {
	group := "не выбрана"
	if u.Group != nil {
		group = u.Group.Name
	}
	subgroup := "вся группа"
	if u.Subgroup != nil {
		subgroup = u.Subgroup.Name
	}

	notify := "🔕 Уведомления: выключены"
	if u.User.Notify {
		notify = "🔔 Уведомления: " + notifySummary(u.User)
	}

	rows := [][]Button{
		{{Label: "🎓 Группа: " + group, Data: cbGroupEdit}},
	}
	// Строка появляется только у тех, у кого есть из чего выбирать: у
	// большинства групп копии в каталоге нет, и лишний пункт с непонятным
	// словом «копия» только мешал бы.
	if u.GroupTwins > 1 && u.Group != nil {
		rows = append(rows, []Button{{Label: "🔀 Копия группы: " + twinLabel(*u.Group), Data: cbTwinMen}})
	}
	rows = append(rows,
		[]Button{{Label: "👥 Подгруппа: " + subgroup, Data: cbSubEdit}},
		[]Button{{Label: notify, Data: cbNotifyMen}},
		// Возврат, а не «закрыть»: настройки теперь лежат внутри «Ещё», и
		// выход из них не должен выбрасывать человека из навигации целиком.
		// Закрывается всё одной кнопкой уровнем выше.
		[]Button{{Label: "◀️ Назад", Data: cbMore}},
	)
	return &Keyboard{Rows: rows}
}

// twinLabel описывает копию так, чтобы её можно было отличить от одноимённой
// соседки: названия у них совпадают до буквы, а курс в каталоге — нет.
func twinLabel(g store.Group) string {
	if g.Course == 0 {
		return "выбрана"
	}
	return fmt.Sprintf("%d курс", g.Course)
}

// TwinsKeyboard — выбор между одноимёнными записями каталога.
//
// У каждой копии подписано состояние расписания: именно по нему человек
// понимает, какая из них его, — курс сам по себе ни о чём не говорит, если
// вуз только что перенумеровал группы.
func TwinsKeyboard(twins []store.Twin, current int64) *Keyboard {
	k := &Keyboard{}
	for _, t := range twins {
		label := fmt.Sprintf("%s · %s", twinLabel(t.Group), twinState(t))
		if t.ID == current {
			label = "✅ " + label
		}
		k.Rows = append(k.Rows, []Button{{Label: label, Data: cb(cbTwinPick, strconv.FormatInt(t.ID, 10))}})
	}
	k.Rows = append(k.Rows, []Button{{Label: "◀️ Назад", Data: cbSettings}})
	return k
}

// twinState — что с расписанием у копии.
func twinState(t store.Twin) string {
	switch {
	case t.HasCurrent:
		return "расписание идёт"
	case t.LastDate != "":
		return "закончилось " + shortDate(t.LastDate)
	default:
		return "расписания нет"
	}
}

// shortDate печатает ISO-дату как «10.06.2026». Год здесь обязателен: копии
// различаются как раз тем, что у одной расписание кончилось прошлой весной.
func shortDate(iso string) string {
	t, err := schedule.ParseDate(iso)
	if err != nil {
		return iso
	}
	return t.Format("02.01.2006")
}

// notifySummary — что бот шлёт, одной строкой для кнопки настроек.
//
// Строка настроек обязана показывать состояние, не открывая вкладку: иначе
// человек, которому бот вдруг перестал писать, идёт искать причину вглубь.
func notifySummary(u store.User) string {
	var parts []string
	if u.Morning {
		parts = append(parts, "утро "+formatMinute(u.MorningAt))
	}
	if u.Evening {
		parts = append(parts, "вечер "+formatMinute(u.EveningAt))
	}
	switch {
	case len(parts) > 0:
		return strings.Join(parts, ", ")
	case u.Changes:
		return "только правки"
	}
	return "ничего не выбрано"
}

// onOff подписывает переключатель.
func onOff(v bool) string {
	if v {
		return "вкл"
	}
	return "выкл"
}

// NotifyKeyboard — вкладка уведомлений.
//
// Всё, что бот шлёт по своей инициативе, собрано на одном экране, и каждая
// строка сразу показывает своё состояние. Выключенный главный тумблер прячет
// остальное: настраивать время рассылки, которая всё равно не придёт, —
// ловушка, в которую человек попадает ровно один раз, а претензия остаётся.
//
// Шесть строк — это потолок инлайн-клавиатуры ВКонтакте (см. vk/keyboard.go).
// Поэтому время стоит второй кнопкой в строке своей рассылки, а не отдельной
// строкой: иначе вкладка уезжала бы на нижнюю панель и переставала
// редактироваться на месте.
func NotifyKeyboard(u store.User) *Keyboard {
	if !u.Notify {
		return &Keyboard{Rows: [][]Button{
			{{Label: "🔕 Уведомления: выкл", Data: cbNotifyAll}},
			{{Label: "◀️ Назад", Data: cbSettings}},
		}}
	}

	morning := []Button{{Label: "🌅 Утро: " + onOff(u.Morning), Data: cbNotifyMor}}
	if u.Morning {
		morning = append(morning, Button{
			Label: "⏰ " + formatMinute(u.MorningAt),
			Data:  cb(cbNotifyAsk, askMorning),
		})
	}
	evening := []Button{{Label: "🌙 Вечер: " + onOff(u.Evening), Data: cbNotifyEve}}
	if u.Evening {
		evening = append(evening, Button{
			Label: "⏰ " + formatMinute(u.EveningAt),
			Data:  cb(cbNotifyAsk, askEvening),
		})
	}

	return &Keyboard{Rows: [][]Button{
		{{Label: "🔔 Уведомления: вкл", Data: cbNotifyAll}},
		morning,
		evening,
		{{Label: "📭 Писать в пустые дни: " + onOff(u.EmptyDays), Data: cbNotifyEmp}},
		{{Label: "📌 Сообщать о правках: " + onOff(u.Changes), Data: cbNotifyChg}},
		{{Label: "◀️ Назад", Data: cbSettings}},
	}}
}

// Какую из двух рассылок настраиваем; едет в callback-данных.
const (
	askMorning = "m"
	askEvening = "e"
)

// TimeKeyboard — быстрые варианты времени, пока человек не написал своё.
//
// Ввод текстом нужен, потому что диапазон — это полторы сотни минут, и
// кнопкой их не выбрать. Но большинству хватит привычного получаса, и
// заставлять их печатать ради этого незачем.
func TimeKeyboard(kind string, current int) *Keyboard {
	presets := []int{6 * 60, 6*60 + 30, 7 * 60, 7*60 + 30, 8 * 60}
	if kind == askEvening {
		presets = []int{17 * 60, 18 * 60, 18*60 + 30, 20 * 60, 21 * 60}
	}
	var row []Button
	for _, m := range presets {
		label := formatMinute(m)
		if m == current {
			label = "✅ " + label
		}
		row = append(row, Button{Label: label, Data: cb(cbNotifySet, kind, strconv.Itoa(m))})
	}
	return &Keyboard{Rows: [][]Button{
		row,
		{{Label: "◀️ Назад", Data: cbNotifyMen}},
	}}
}

// GroupsKeyboard — результаты поиска группы, по одной в строке.
func GroupsKeyboard(groups []store.Group, withBrowse bool) *Keyboard {
	k := &Keyboard{}
	for _, g := range groups {
		label := g.Name
		if g.Department != "" {
			label = fmt.Sprintf("%s · %d курс", g.Name, g.Course)
		}
		k.Rows = append(k.Rows, []Button{{Label: label, Data: cb(cbGroupPick, strconv.FormatInt(g.ID, 10))}})
	}
	if withBrowse {
		k.Rows = append(k.Rows, []Button{{Label: "📚 Выбрать из списка институтов", Data: cbBrowse}})
	}
	return k
}

// ── страницы длинных списков ────────────────────────────────────────────────

// Списки, которые не помещаются на один экран, листаются страницами.
//
// Размер страницы считается по самому тесному из двух наших экранов — нижней
// панели ВКонтакте: десять строк и сорок кнопок на весь ответ. Раньше лишнее
// там просто отбрасывалось, и человек с 42 группами на курсе не находил свою
// среди показанных сорока. Страницы одинаковы на обеих платформах: телеграму
// они не жмут, а расходиться раскладкам ради его лимитов значит заводить в
// сценариях знание о платформе — ровно то, чего этот пакет не делает.
const panelRows = 10

// Сколько элементов списка помещается на страницу.
//
// Институты идут по одному в строке — названия под полсотни символов, парой в
// строку их не прочитать; группы по два — их названия короткие. Служебные
// строки вычитаются: у институтов это листание, у групп ещё и возврат к
// курсам.
const (
	depsPerPage   = panelRows - 1
	groupsPerPage = (panelRows - 2) * 2
)

// pageMark — метка страницы в callback-данных: «p2» — третья по счёту.
//
// Буква нужна, чтобы номер страницы не путался с id подразделения и номером
// курса: они едут в тех же данных голыми числами. Заодно кнопки, оставшиеся в
// переписке от прошлой версии бота, продолжают работать — метки у них нет, и
// это ровно первая страница.
const pageMark = "p"

func pageArg(page int) string { return pageMark + strconv.Itoa(page) }

// splitPage отделяет от аргументов кнопки метку страницы. Она всегда последняя.
func splitPage(args []string) ([]string, int) {
	n := len(args)
	if n == 0 || !strings.HasPrefix(args[n-1], pageMark) {
		return args, 0
	}
	page, err := strconv.Atoi(args[n-1][len(pageMark):])
	if err != nil {
		return args, 0
	}
	return args[:n-1], page
}

// pages — сколько страниц займёт список. Пустой список — это одна пустая
// страница, а не ноль: экран всё равно рисуется.
func pages(n, size int) int {
	if n <= size {
		return 1
	}
	return (n + size - 1) / size
}

// clampPage возвращает страницу в границы списка.
//
// За границы она уезжает по-настоящему: кнопка со страницей живёт в переписке
// сколько угодно, а каталог за это время пересобирается, и групп на курсе
// становится меньше. Пустой экран вместо списка выглядел бы как поломка бота.
func clampPage(page, total int) int {
	if page < 0 || page >= total {
		return 0
	}
	return page
}

// pageOf вырезает из списка одну страницу.
func pageOf[T any](items []T, page, size int) []T {
	start := clampPage(page, pages(len(items), size)) * size
	if start >= len(items) {
		return nil
	}
	return items[start:min(start+size, len(items))]
}

// pageRow — строка листания; для списка в одну страницу её нет вовсе.
//
// Стрелки закольцованы: с последней страницы «вперёд» ведёт на первую.
// Погасить кнопку не умеет ни телеграм, ни ВКонтакте, а мёртвая на вид кнопка
// раздражает сильнее, чем возврат к началу списка. Номер на подписи — чтобы
// стрелку было видно куда, и чтобы её не путали с «◀️ К курсам» рядом.
func pageRow(page, total int, data func(page int) string) []Button {
	if total < 2 {
		return nil
	}
	prev, next := (page-1+total)%total, (page+1)%total
	return []Button{
		{Label: fmt.Sprintf("◀️ Стр. %d", prev+1), Data: data(prev)},
		{Label: fmt.Sprintf("Стр. %d ▶️", next+1), Data: data(next)},
	}
}

// pageTitle дописывает к заголовку, где мы в списке. По одним стрелкам не
// понять, сколько ещё впереди, — а от этого зависит, листать или искать по
// названию.
func pageTitle(s string, page, total int) string {
	if total < 2 {
		return s + ":"
	}
	return fmt.Sprintf("%s — страница %d из %d:", s, page+1, total)
}

// DepartmentsKeyboard — первый шаг обзора дерева групп.
func DepartmentsKeyboard(deps []store.Department, page int) *Keyboard {
	total := pages(len(deps), depsPerPage)
	page = clampPage(page, total)

	k := &Keyboard{}
	for _, d := range pageOf(deps, page, depsPerPage) {
		k.Rows = append(k.Rows, []Button{{
			Label: d.Name,
			Data:  cb(cbBrowse, strconv.FormatInt(d.ID, 10)),
		}})
	}
	if row := pageRow(page, total, func(p int) string { return cb(cbBrowse, pageArg(p)) }); row != nil {
		k.Rows = append(k.Rows, row)
	}
	return k
}

// CoursesKeyboard — второй шаг обзора: курсы подразделения.
func CoursesKeyboard(departmentID int64, courses []int) *Keyboard {
	var row []Button
	for _, c := range courses {
		row = append(row, Button{
			Label: fmt.Sprintf("%d курс", c),
			Data:  cb(cbBrowse, strconv.FormatInt(departmentID, 10), strconv.Itoa(c)),
		})
	}
	return &Keyboard{Rows: [][]Button{
		row,
		{{Label: "◀️ К институтам", Data: cbBrowse}},
	}}
}

// BrowseGroupsKeyboard — третий шаг обзора: группы курса, по две в строке и
// страницами.
//
// Страницы здесь не украшение: на первом курсе медицинского института 42
// группы, а нижняя панель ВКонтакте держит сорок кнопок. Отбрасывать лишние с
// подписью «не поместилось» значило оставлять часть курса без единого способа
// выбрать себя кнопкой.
func BrowseGroupsKeyboard(departmentID int64, course int, groups []store.Group, page int) *Keyboard {
	total := pages(len(groups), groupsPerPage)
	page = clampPage(page, total)

	k := &Keyboard{}
	part := pageOf(groups, page, groupsPerPage)
	for i := 0; i < len(part); i += 2 {
		var row []Button
		for _, g := range part[i:min(i+2, len(part))] {
			row = append(row, Button{Label: g.Name, Data: cb(cbGroupPick, strconv.FormatInt(g.ID, 10))})
		}
		k.Rows = append(k.Rows, row)
	}
	if row := pageRow(page, total, func(p int) string {
		return cb(cbBrowse, strconv.FormatInt(departmentID, 10), strconv.Itoa(course), pageArg(p))
	}); row != nil {
		k.Rows = append(k.Rows, row)
	}
	k.Rows = append(k.Rows, []Button{{
		Label: "◀️ К курсам",
		Data:  cb(cbBrowse, strconv.FormatInt(departmentID, 10)),
	}})
	return k
}

// SubgroupsKeyboard — выбор подгруппы.
//
// Кнопки строятся по настоящим подгруппам группы с их глобальными id.
// Прошлая версия предлагала жёстко «Подгруппа 1» и «Подгруппа 2» и вычисляла
// принадлежность пары суффиксом в названии — это работало ровно для одной
// группы, под которую и писалось.
func SubgroupsKeyboard(subs []store.Subgroup, current int64) *Keyboard {
	k := &Keyboard{}
	for _, s := range subs {
		label := s.Name
		if s.ID == current {
			label = "✅ " + label
		}
		k.Rows = append(k.Rows, []Button{{Label: label, Data: cb(cbSubPick, strconv.FormatInt(s.ID, 10))}})
	}
	all := "Вся группа"
	if current == 0 {
		all = "✅ " + all
	}
	k.Rows = append(k.Rows, []Button{{Label: all, Data: cb(cbSubPick, "0")}})
	return k
}

// ── что изменилось ──────────────────────────────────────────────────────────

// changeDaysOnKeyboard — сколько дней помещается в кнопки.
//
// Потолок ставит инлайн-клавиатура ВКонтакте: шесть строк и десять кнопок на
// сообщение (см. vk/keyboard.go). Восемь дней по два в строке плюс строка
// возврата укладываются ровно. Дни идут от ближайшего: до дальних правка
// успеет доехать ещё раз.
const changeDaysOnKeyboard = 8

// ChangeDatesKeyboard — выбор дня из тех, где недавно правили расписание.
func ChangeDatesKeyboard(dates []string, today string, groupID int64, guest int64) *Keyboard {
	if len(dates) > changeDaysOnKeyboard {
		dates = dates[:changeDaysOnKeyboard]
	}
	group := strconv.FormatInt(groupID, 10)

	k := &Keyboard{}
	for i := 0; i < len(dates); i += 2 {
		var row []Button
		for _, d := range dates[i:min(i+2, len(dates))] {
			row = append(row, Button{
				Label: dayButtonLabel(d, today),
				Data:  cb(cbChangeDay, group, d),
			})
		}
		k.Rows = append(k.Rows, row)
	}
	k.Rows = append(k.Rows, []Button{
		{Label: MenuToday, Data: cbView(cbDay, todayArg, guest)},
		{Label: MenuWeek, Data: cbView(cbWeek, "", guest)},
	})
	return k
}

// ChangeDayKeyboard — навигация под сравнением «до/после».
//
// «Открыть день» ведёт в обычное расписание того же дня: сравнение отвечает на
// вопрос «что поменялось», а жить человек будет с полной версией дня.
func ChangeDayKeyboard(date string, groupID, guest int64) *Keyboard {
	return &Keyboard{Rows: [][]Button{
		{{Label: "📅 Открыть день", Data: cbView(cbDay, date, guest)}},
		{{Label: "◀️ Другие изменения", Data: cb(cbChanges, strconv.FormatInt(groupID, 10))}},
	}}
}

// ── «Ещё» ───────────────────────────────────────────────────────────────────

// MoreKeyboard — экран, заменивший в нижней панели кнопку настроек.
//
// Пять строк при потолке инлайн-клавиатуры ВКонтакте в шесть (см.
// vk/keyboard.go), поэтому связь с автором стоит отдельной строкой, а помощь и
// рассказ о боте делят одну: их открывают по одному разу и больше не
// возвращаются.
//
// withFeedback выключает строку обратной связи там, где адресат не задан:
// кнопка, которая пишет в пустоту, хуже отсутствующей.
func MoreKeyboard(withFeedback bool) *Keyboard {
	rows := [][]Button{
		{{Label: "⚙️ Настройки", Data: cbSettings}},
		{
			{Label: "❓ Что я умею", Data: cbHelp},
			{Label: "ℹ️ О проекте", Data: cbAbout},
		},
	}
	if withFeedback {
		rows = append(rows, []Button{{Label: "💬 Написать автору", Data: cbFeedback}})
	}
	rows = append(rows,
		[]Button{{Label: "🔗 Поделиться ботом", Data: cbShare}},
		[]Button{{Label: "✖️ Закрыть", Data: cbClose}},
	)
	return &Keyboard{Rows: rows}
}

// AboutKeyboard — что предложить после рассказа о боте.
//
// Обе кнопки здесь не украшение: человек, дочитавший этот экран, — ровно тот,
// кто готов написать автору или позвать однокурсника.
func AboutKeyboard(withFeedback bool) *Keyboard {
	row := []Button{{Label: "🔗 Поделиться", Data: cbShare}}
	if withFeedback {
		row = append([]Button{{Label: "💬 Написать автору", Data: cbFeedback}}, row...)
	}
	return &Keyboard{Rows: [][]Button{row, backToMore()}}
}

// HelpKeyboard — возврат из списка возможностей.
func HelpKeyboard() *Keyboard { return &Keyboard{Rows: [][]Button{backToMore()}} }

// FeedbackKeyboard — выход из ожидания текста обращения.
//
// Кнопка обязательна ровно по той же причине, что и в диалоге времени: без неё
// человек, случайно нажавший «Написать автору», остаётся в состоянии, из
// которого не выйти ничем, кроме команды.
func FeedbackKeyboard() *Keyboard { return &Keyboard{Rows: [][]Button{backToMore()}} }

// AnswerKeyboard — кнопка под обращением, пришедшим автору.
func AnswerKeyboard(id int64) *Keyboard {
	return &Keyboard{Rows: [][]Button{{{
		Label: "✍️ Ответить",
		Data:  cb(cbAnswer, strconv.FormatInt(id, 10)),
	}}}}
}

// ReplyKeyboard — кнопка под ответом автора: продолжить разговор.
func ReplyKeyboard() *Keyboard {
	return &Keyboard{Rows: [][]Button{{{Label: "💬 Ответить", Data: cbFeedback}}}}
}

func backToMore() []Button {
	return []Button{{Label: "◀️ Назад", Data: cbMore}}
}
