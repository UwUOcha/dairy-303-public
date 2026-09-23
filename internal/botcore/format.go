package botcore

import (
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"

	"github.com/UwUOcha/dairy-303-public/internal/api"
	"github.com/UwUOcha/dairy-303-public/internal/profile"
	"github.com/UwUOcha/dairy-303-public/internal/schedule"
)

// Разметка сообщений — телеграмный HTML. Это подмножество из десятка тегов,
// которое VK просто проигнорирует, поэтому форматтер остаётся общим, а
// адаптеры при необходимости снимают теги у себя.

var weekdaysFull = [...]string{"Воскресенье", "Понедельник", "Вторник", "Среда", "Четверг", "Пятница", "Суббота"}

// monthsGenitive — родительный падеж: «19 сентября», а не «19 сентябрь».
var monthsGenitive = [...]string{"", "января", "февраля", "марта", "апреля", "мая", "июня",
	"июля", "августа", "сентября", "октября", "ноября", "декабря"}

func esc(s string) string { return html.EscapeString(s) }

// monthsNominative — именительный падеж: «расписание на сентябрь».
var monthsNominative = [...]string{"", "январь", "февраль", "март", "апрель", "май", "июнь",
	"июль", "август", "сентябрь", "октябрь", "ноябрь", "декабрь"}

// monthPhrase печатает «расписание на сентябрь» — и с годом, если год не
// текущий: в январе правка декабрьского расписания не должна выглядеть как
// правка предстоящего декабря.
func monthPhrase(year, month int) string {
	if month < 1 || month > 12 {
		return "расписание"
	}
	name := monthsNominative[month]
	if year != time.Now().Year() {
		return fmt.Sprintf("расписание на %s %d", name, year)
	}
	return "расписание на " + name
}

// humanDate печатает «Пятница, 19 сентября».
func humanDate(date string) string {
	t, err := schedule.ParseDate(date)
	if err != nil {
		return date
	}
	return fmt.Sprintf("%s, %d %s", weekdaysFull[int(t.Weekday())], t.Day(), monthsGenitive[int(t.Month())])
}

// dateHeader добавляет год только тогда, когда показанная дата не относится
// к текущему для ответа году. На обычном экране он был бы шумом, а при
// листании архива или следующего учебного года без него дата неоднозначна.
func dateHeader(date, today string) string {
	header := humanDate(date)
	t, err := schedule.ParseDate(date)
	if err != nil {
		return header
	}
	now, err := schedule.ParseDate(today)
	if err != nil || t.Year() != now.Year() {
		header += fmt.Sprintf(" %d", t.Year())
	}
	return header
}

// relativeHint подписывает дату словом «сегодня» или «завтра», если это так.
//
// Мелочь, но именно она снимает вопрос «а какой сегодня день», когда человек
// листает расписание стрелками и теряет точку отсчёта.
func relativeHint(date, today string) string {
	t, err1 := schedule.ParseDate(date)
	n, err2 := schedule.ParseDate(today)
	if err1 != nil || err2 != nil {
		return ""
	}
	switch int(t.Sub(n).Hours() / 24) {
	case 0:
		return "сегодня"
	case 1:
		return "завтра"
	case -1:
		return "вчера"
	}
	return ""
}

// duration печатает промежуток по-русски: «1 ч 50 мин».
func duration(minutes int) string {
	switch {
	case minutes <= 0:
		return ""
	case minutes < 60:
		return fmt.Sprintf("%d мин", minutes)
	case minutes%60 == 0:
		return fmt.Sprintf("%d ч", minutes/60)
	}
	return fmt.Sprintf("%d ч %d мин", minutes/60, minutes%60)
}

// marks собирает значки особых занятий.
func marks(l schedule.Lesson) string {
	var out []string
	if l.Flags.Has(schedule.FlagRemote) {
		out = append(out, "💻 дистанционно")
	}
	if l.Flags.Has(schedule.FlagSelfWork) {
		out = append(out, "📖 самоподготовка")
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, " · ")
}

// audienceNote подписывает, что пара не для всей группы или, наоборот,
// общая для потока.
//
// viewerSubgroup — подгруппа, которую выбрал читатель. Если пара как раз его
// подгруппы, подпись не нужна: он и так видит только свои пары, и повторять
// «ГР-12/2» у каждой строки — шум. А вот когда подгруппа не выбрана и в
// выдаче лежат пары обеих, подпись становится единственным способом их
// различить.
//
// Прошлая версия вытаскивала это суффиксом из названия группы и умела
// распознать ровно одну зашитую в код группу. Теперь подпись приходит от
// upstream вместе с занятием.
func audienceNote(l schedule.Lesson, viewerSubgroup int64) string {
	switch l.Audience {
	case schedule.AudienceSubgroup:
		if viewerSubgroup != 0 && l.SubgroupID == viewerSubgroup {
			return ""
		}
		if l.AudienceLabel != "" {
			return l.AudienceLabel
		}
		return "подгруппа"
	case schedule.AudienceFlow, schedule.AudienceSuperflow:
		return "поток"
	}
	return ""
}

// viewerSubgroup — какую подгруппу выбрал читатель; 0, если смотрит всё.
func viewerSubgroup(c api.Context) int64 {
	if c.Subgroup != nil {
		return c.Subgroup.ID
	}
	return 0
}

// FormatDay собирает сообщение с расписанием одного дня.
func FormatDay(r api.DayResponse) string {
	var b strings.Builder

	header := dateHeader(r.Day.Date, r.Today)
	if hint := relativeHint(r.Day.Date, r.Today); hint != "" {
		header += " · " + hint
	}
	fmt.Fprintf(&b, "<b>%s</b>\n", esc(header))
	fmt.Fprintf(&b, "<i>%s</i>\n", esc(groupLine(r.Context)))

	switch {
	case len(r.Day.Items) > 0:
		b.WriteString("\n")
		writeItems(&b, r.Day.Items, viewerSubgroup(r.Context))
	case r.Missing:
		b.WriteString("\nРасписание на этот день пока не удалось загрузить. Проверь позже.")
	case !r.Day.Workday:
		b.WriteString("\nВыходной — занятий нет. 🎉")
	default:
		b.WriteString("\nЗанятий нет.")
	}

	writeFreshness(&b, r.Freshness)
	return b.String()
}

// writeItems печатает пары дня в вёрстке, к которой привыкли пользователи
// прошлой версии: шапка с номером пары и цитата с подробностями.
func writeItems(b *strings.Builder, items []schedule.Item, subgroup int64) {
	for i, it := range items {
		if i > 0 {
			b.WriteString("\n")
		}
		if it.GapBefore > 0 {
			fmt.Fprintf(b, "<i>⏳ Окно %s</i>\n\n", esc(duration(it.GapBefore)))
		}

		// Пустая пара — это заведённое расписанием окно; показать её надо,
		// но подробностей у неё нет.
		head := "<b>Занятие</b> · " + esc(it.TimeLabel)
		if it.Number > 0 {
			head = fmt.Sprintf("<b>%d пара</b> · %s", it.Number, esc(it.TimeLabel))
		}
		if it.Flags.Has(schedule.FlagEmpty) {
			fmt.Fprintf(b, "%s\n<blockquote>Окно</blockquote>\n", head)
			continue
		}
		if note := audienceNote(it.Lesson, subgroup); note != "" {
			head += " · <i>" + esc(note) + "</i>"
		}
		fmt.Fprintf(b, "%s\n", head)

		b.WriteString("<blockquote>")
		fmt.Fprintf(b, "<b>%s</b>", esc(orDash(it.Discipline, "Без названия")))
		if it.ClassType != "" {
			fmt.Fprintf(b, " · %s", esc(it.ClassType))
		}
		b.WriteString("\n")

		details := []string{}
		if it.Classroom != "" {
			details = append(details, "<u>"+esc(it.Classroom)+"</u>")
		}
		if len(it.Staff) > 0 {
			details = append(details, esc(strings.Join(it.Staff, ", ")))
		}
		if len(details) == 0 {
			details = append(details, "<i>аудитория и преподаватель не указаны</i>")
		}
		b.WriteString(strings.Join(details, " · "))

		if m := marks(it.Lesson); m != "" {
			b.WriteString("\n" + m)
		}
		if it.Comments != "" {
			fmt.Fprintf(b, "\n<i>%s</i>", esc(it.Comments))
		}
		b.WriteString("</blockquote>\n")
	}
}

// FormatWeek собирает компактное сообщение с расписанием недели.
//
// Прошлая версия склеивала неделю из шести полноразмерных дней и выдавала
// простыню, в которой ничего не найти. Здесь на пару отводится одна строка:
// неделя целиком помещается на экран.
func FormatWeek(r api.WeekResponse) string {
	var b strings.Builder

	fmt.Fprintf(&b, "<b>📆 %s</b>\n", esc(weekRange(r.Week, r.Today)))
	fmt.Fprintf(&b, "<i>%s</i>\n", esc(groupLine(r.Context)))

	if r.Week.Empty() {
		if r.Missing {
			b.WriteString("\nРасписание на эту неделю пока не удалось загрузить. Проверь позже.")
		} else {
			b.WriteString("\nНа этой неделе занятий нет.")
		}
		writeFreshness(&b, r.Freshness)
		return b.String()
	}

	for _, d := range r.Week.Days {
		t, err := schedule.ParseDate(d.Date)
		if err != nil {
			continue
		}
		// Пустой учебный день выкидывать нельзя. Молча пропущенная среда
		// неотличима от среды без пар, а это ровно тот вопрос, с которым
		// человек и открывает неделю: «у меня в среду точно ничего нет?»
		if d.Empty() {
			status := "занятий нет"
			if r.Missing {
				status = "нет полных данных"
			}
			fmt.Fprintf(&b, "\n<b>%s</b> — <i>%s</i>\n", esc(dayTitle(t, d.Date, r.Today)), status)
			continue
		}
		fmt.Fprintf(&b, "\n<b>%s</b>\n<blockquote>", esc(dayTitle(t, d.Date, r.Today)))

		for i, it := range d.Items {
			if i > 0 {
				b.WriteString("\n")
			}
			if it.Number > 0 {
				fmt.Fprintf(&b, "%d · ", it.Number)
			}
			if it.Flags.Has(schedule.FlagEmpty) {
				b.WriteString("<i>окно</i>")
				continue
			}
			fmt.Fprintf(&b, "%s · <b>%s</b>", esc(startTime(it)), esc(it.Discipline))
			// Тип занятия стоит сразу за названием, как и в дневном виде:
			// «лекция или практика» — первое, что уточняют про пару, и по
			// одному названию дисциплины этого не понять.
			if it.ClassType != "" {
				fmt.Fprintf(&b, " · %s", esc(it.ClassType))
			}
			if it.Classroom != "" {
				fmt.Fprintf(&b, " · %s", esc(it.Classroom))
			}
			if m := marks(it.Lesson); m != "" {
				fmt.Fprintf(&b, " · %s", m)
			}
			if note := audienceNote(it.Lesson, viewerSubgroup(r.Context)); note != "" {
				fmt.Fprintf(&b, " · <i>%s</i>", esc(note))
			}
		}
		b.WriteString("</blockquote>\n")
	}

	writeFreshness(&b, r.Freshness)
	return b.String()
}

func pairName(number int) string {
	if number > 0 {
		return fmt.Sprintf("%d пара", number)
	}
	return "занятие"
}

// FormatNow отвечает на вопрос «что сейчас».
func FormatNow(r api.NowResponse) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<b>⏰ %s</b>\n", esc(r.Now.At.Format("15:04")))
	fmt.Fprintf(&b, "<i>%s</i>\n\n", esc(groupLine(r.Context)))

	switch {
	case r.Now.Current != nil:
		it := r.Now.Current
		left := it.MinuteTo - (r.Now.At.Hour()*60 + r.Now.At.Minute())
		fmt.Fprintf(&b, "Сейчас идёт <b>%s</b>, до конца %s\n", pairName(it.Number), esc(duration(left)))
		b.WriteString("<blockquote>")
		fmt.Fprintf(&b, "<b>%s</b>", esc(it.Discipline))
		if it.Classroom != "" {
			fmt.Fprintf(&b, "\n<u>%s</u>", esc(it.Classroom))
		}
		if m := marks(it.Lesson); m != "" {
			b.WriteString("\n" + m)
		}
		if it.Comments != "" {
			fmt.Fprintf(&b, "\n<i>%s</i>", esc(it.Comments))
		}
		b.WriteString("</blockquote>\n")
	case r.Missing:
		b.WriteString("Расписание на сегодня пока не удалось загрузить. Проверь позже.\n")
	case !r.Day.Workday:
		b.WriteString("Сегодня выходной. 🎉\n")
	case r.Day.Empty():
		b.WriteString("Сегодня занятий нет.\n")
	case r.Now.Next == nil:
		b.WriteString("Пары на сегодня закончились. 🙌\n")
	default:
		b.WriteString("Сейчас пар нет.\n")
	}

	if r.Now.Next != nil {
		it := r.Now.Next
		fmt.Fprintf(&b, "\nЧерез %s — <b>%s</b> в %s\n",
			esc(duration(r.Now.MinutesToNext)), pairName(it.Number), esc(startTime(*it)))
		b.WriteString("<blockquote>")
		fmt.Fprintf(&b, "<b>%s</b>", esc(it.Discipline))
		if it.Classroom != "" {
			fmt.Fprintf(&b, "\n<u>%s</u>", esc(it.Classroom))
		}
		if len(it.Staff) > 0 {
			fmt.Fprintf(&b, " · %s", esc(strings.Join(it.Staff, ", ")))
		}
		if m := marks(it.Lesson); m != "" {
			b.WriteString("\n" + m)
		}
		if it.Comments != "" {
			fmt.Fprintf(&b, "\n<i>%s</i>", esc(it.Comments))
		}
		b.WriteString("</blockquote>\n")
	}

	writeFreshness(&b, r.Freshness)
	return b.String()
}

// writeFreshness добавляет пометку о свежести данных.
//
// Локальная копия позволяет отвечать, даже когда источник лёг. Честно сказать,
// что данные от позавчера, куда лучше, чем молчать или, того хуже, выдавать
// устаревшее за актуальное.
func writeFreshness(b *strings.Builder, f api.Freshness) {
	if f.Missing {
		b.WriteString("\n<i>⚠️ Данные неполные: отсутствие пары в списке не означает её отмену.</i>")
		return
	}
	if !f.Stale || f.FetchedAt.IsZero() {
		return
	}
	fmt.Fprintf(b, "\n<i>⚠️ Данные от %s — давно не обновлялись, возможны изменения.</i>",
		esc(f.FetchedAt.In(profile.Current().Location()).Format("02.01 15:04")))
}

// groupLine — подпись «за кого» показано расписание.
//
// Название подгруппы обычно содержит название группы целиком («ГР-12/2»), и
// печатать оба — шум. Но вуз называет подгруппы и просто «1»: тогда в шапке не
// осталось бы ни следа от того, чьё это расписание.
func groupLine(c api.Context) string {
	if c.Subgroup == nil {
		return c.Group.Name
	}
	if strings.Contains(c.Subgroup.Name, c.Group.Name) {
		return c.Subgroup.Name
	}
	return c.Group.Name + " · " + c.Subgroup.Name
}

// dayTitle — заголовок дня внутри недели: «Вторник, 16 · завтра».
//
// Месяц уже назван в шапке диапазона — повторять его у каждого из шести дней
// значит растить простыню без единой новой мысли.
func dayTitle(t time.Time, date, today string) string {
	title := fmt.Sprintf("%s, %d", weekdaysFull[int(t.Weekday())], t.Day())
	if hint := relativeHint(date, today); hint != "" {
		title += " · " + hint
	}
	return title
}

// weekRange печатает «15 – 20 сентября», схлопывая повтор месяца.
func weekRange(w schedule.Week, today string) string {
	if len(w.Days) == 0 {
		return "Неделя"
	}
	from, err1 := schedule.ParseDate(w.Days[0].Date)
	to, err2 := schedule.ParseDate(w.Days[len(w.Days)-1].Date)
	if err1 != nil || err2 != nil {
		return "Неделя"
	}
	if from.Year() != to.Year() {
		return fmt.Sprintf("%d %s %d – %d %s %d",
			from.Day(), monthsGenitive[int(from.Month())], from.Year(),
			to.Day(), monthsGenitive[int(to.Month())], to.Year())
	}
	var suffix string
	now, err := schedule.ParseDate(today)
	if err != nil || from.Year() != now.Year() {
		suffix = fmt.Sprintf(" %d", from.Year())
	}
	if from.Month() == to.Month() {
		return fmt.Sprintf("%d – %d %s%s", from.Day(), to.Day(), monthsGenitive[int(to.Month())], suffix)
	}
	return fmt.Sprintf("%d %s – %d %s%s",
		from.Day(), monthsGenitive[int(from.Month())], to.Day(), monthsGenitive[int(to.Month())], suffix)
}

// startTime вытаскивает время начала пары из подписи сетки звонков.
func startTime(it schedule.Item) string {
	if i := strings.Index(it.TimeLabel, " - "); i > 0 {
		return it.TimeLabel[:i]
	}
	if it.MinuteFrom > 0 {
		return fmt.Sprintf("%02d:%02d", it.MinuteFrom/60, it.MinuteFrom%60)
	}
	return it.TimeLabel
}

// lessonTime сохраняет и конец пары: сокращение занятия тоже является правкой.
func lessonTime(it schedule.Item) string {
	if it.MinuteTo > it.MinuteFrom {
		return formatMinute(it.MinuteFrom) + "–" + formatMinute(it.MinuteTo)
	}
	return it.TimeLabel
}

func orDash(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// formatMinute печатает минуты от полуночи как «07:30».
func formatMinute(m int) string { return fmt.Sprintf("%02d:%02d", m/60, m%60) }

// userTime возвращает текущее время в поясе пользователя.
func userTime(tzOffset int) time.Time {
	return time.Now().In(time.FixedZone("user", tzOffset*60))
}

// ── что изменилось ──────────────────────────────────────────────────────────

// weekdaysShort — подпись дня недели там, где полная не помещается: в списке
// изменённых дней и на кнопках.
var weekdaysShort = [...]string{"вс", "пн", "вт", "ср", "чт", "пт", "сб"}

// changeDaysInText — сколько дней перечисляется прямо в сообщении о правке.
//
// Сообщение должно читаться с экрана телефона одним взглядом. Если вуз
// перезалил полмесяца, перечень из тридцати дат превращает новость в
// простыню — остальные видны по кнопке.
const changeDaysInText = 6

// changeDaysPhrase печатает задетые дни: «пн 15, вт 16 и ещё 3 дня».
//
// Число месяца плюс день недели, а не одна дата: «вторник» человек сверяет со
// своей неделей быстрее, чем «16».
func changeDaysPhrase(year, month int, days []int, limit int) string {
	if month < 1 || month > 12 || len(days) == 0 {
		return ""
	}
	shown := days
	hidden := 0
	if limit > 0 && len(days) > limit {
		shown, hidden = days[:limit], len(days)-limit
	}

	parts := make([]string, 0, len(shown))
	for _, d := range shown {
		label := strconv.Itoa(d)
		if t, err := schedule.ParseDate(isoDate(year, month, d)); err == nil {
			label = weekdaysShort[int(t.Weekday())] + " " + label
		}
		parts = append(parts, label)
	}
	out := strings.Join(parts, ", ") + " " + monthsGenitive[month]
	if hidden > 0 {
		out += fmt.Sprintf(" и ещё %d %s", hidden, plural(hidden, "день", "дня", "дней"))
	}
	return out
}

// isoDate собирает ISO-дату из года, месяца и числа.
func isoDate(year, month, day int) string {
	return fmt.Sprintf("%04d-%02d-%02d", year, month, day)
}

// plural выбирает форму русского существительного по числу.
func plural(n int, one, few, many string) string {
	n = n % 100
	if n >= 11 && n <= 14 {
		return many
	}
	switch n % 10 {
	case 1:
		return one
	case 2, 3, 4:
		return few
	}
	return many
}

// dayButtonLabel подписывает кнопку изменённого дня: «19.09 · пт».
//
// «Сегодня» и «завтра» вытесняют день недели: если правка касается ближайших
// суток, это первое, что человеку надо увидеть.
func dayButtonLabel(date, today string) string {
	t, err := schedule.ParseDate(date)
	if err != nil {
		return date
	}
	note := weekdaysShort[int(t.Weekday())]
	if hint := relativeHint(date, today); hint != "" {
		note = hint
	}
	return fmt.Sprintf("%02d.%02d · %s", t.Day(), int(t.Month()), note)
}

// FormatChangeDay собирает «до/после» одного дня.
//
// Списком строк, а не двумя расписаниями подряд: правка обычно трогает одну
// пару из шести, и человек должен увидеть именно её, а не искать разницу
// глазами. Поэтому совпавшие строки помечаются точкой, пропавшие — минусом,
// появившиеся — плюсом.
func FormatChangeDay(r api.ChangeDayResponse) string {
	var b strings.Builder

	header := dateHeader(r.Date, r.Today)
	if hint := relativeHint(r.Date, r.Today); hint != "" {
		header += " · " + hint
	}
	fmt.Fprintf(&b, "<b>🔍 %s</b>\n", esc(header))
	fmt.Fprintf(&b, "<i>%s</i>\n", esc(groupLine(r.Context)))

	sub := viewerSubgroup(r.Context)
	after := changeLines(r.After, sub)

	switch {
	case !r.Known:
		// Снимок протух. Промолчать нельзя: кнопка нажата, и ответ «ничего не
		// изменилось» был бы враньём — мы просто не знаем, что было.
		b.WriteString("\n<i>Что было до правки, я уже не помню: такие снимки живут трое суток. Вот как этот день выглядит сейчас.</i>\n\n")
		writeChangeBlock(&b, "Сейчас", "· ", after, nil)
	case sameLines(changeLines(r.Before, sub), after):
		// Правка задела другую подгруппу. Сказать об этом прямо дешевле, чем
		// заставлять человека сличать два одинаковых списка.
		b.WriteString("\n<i>Тебя правка не задела: в твоём расписании этот день не изменился.</i>\n\n")
		writeChangeBlock(&b, "День", "· ", after, nil)
	default:
		before := changeLines(r.Before, sub)
		b.WriteString("\n")
		writeChangeBlock(&b, "Было", "➖ ", before, after)
		writeChangeBlock(&b, "Стало", "➕ ", after, before)
	}

	writeFreshness(&b, r.Freshness)
	return b.String()
}

// changeLines превращает день в строки, которые можно сравнивать.
//
// Одна пара — одна строка: сравнение по строке целиком показывает и перенос
// пары, и смену аудитории, и замену преподавателя, не требуя отдельного
// правила на каждое поле.
func changeLines(d schedule.Day, subgroup int64) []string {
	out := make([]string, 0, len(d.Items))
	for _, it := range d.Items {
		prefix := ""
		if it.Number > 0 {
			prefix = fmt.Sprintf("%d · ", it.Number)
		}
		if it.Flags.Has(schedule.FlagEmpty) {
			out = append(out, prefix+"окно")
			continue
		}
		parts := []string{
			prefix + lessonTime(it),
			orDash(it.Discipline, "Без названия"),
		}
		if it.ClassType != "" {
			parts = append(parts, it.ClassType)
		}
		if it.Classroom != "" {
			parts = append(parts, it.Classroom)
		}
		if len(it.Staff) > 0 {
			parts = append(parts, strings.Join(it.Staff, ", "))
		}
		if note := audienceNote(it.Lesson, subgroup); note != "" {
			parts = append(parts, note)
		}
		if m := marks(it.Lesson); m != "" {
			parts = append(parts, m)
		}
		if it.Flags.Has(schedule.FlagNonStudy) {
			parts = append(parts, "неучебный день")
		}
		if it.Comments != "" {
			parts = append(parts, it.Comments)
		}
		out = append(out, strings.Join(parts, " · "))
	}
	return out
}

func sameLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// writeChangeBlock печатает одну половину сравнения, помечая строки, которых
// нет во второй. other == nil означает «помечать нечего»: блок показывается
// сам по себе.
func writeChangeBlock(b *strings.Builder, title, mark string, lines, other []string) {
	fmt.Fprintf(b, "<b>%s</b>\n", title)
	if len(lines) == 0 {
		b.WriteString("<blockquote>Занятий нет</blockquote>\n")
		return
	}

	// Счётчик, а не множество: две одинаковые пары в один слот у разных
	// подгрупп — обычное дело, и пропажа одной из них не должна теряться.
	rest := make(map[string]int, len(other))
	for _, l := range other {
		rest[l]++
	}

	b.WriteString("<blockquote>")
	for i, l := range lines {
		if i > 0 {
			b.WriteString("\n")
		}
		prefix := mark
		if rest[l] > 0 {
			rest[l]--
			prefix = "· "
		}
		fmt.Fprintf(b, "%s%s", prefix, esc(l))
	}
	b.WriteString("</blockquote>\n")
}
