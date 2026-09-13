// Package schedule — доменное ядро: превращает разложенные по базе занятия в
// то, что человек хочет прочитать.
//
// В пакете нет ни одного обращения к сети или диску. На входе — данные, на
// выходе — структуры. Благодаря этому вся возня с подгруппами, потоками и
// «окнами» покрывается табличными тестами и не расползается по боту.
package schedule

import "time"

// Audience — кому адресовано занятие.
//
// Перечисление намеренно продублировано из пакета importdata, а не импортировано
// оттуда: домен не должен знать про формат upstream. Ценой четырёх констант
// мы покупаем возможность подключить другой источник данных, не трогая
// ни домен, ни бота.
type Audience uint8

const (
	AudienceGroup Audience = iota
	AudienceFlow
	AudienceSubgroup
	AudienceSuperflow
)

// Flags — битовая маска признаков занятия.
type Flags uint32

const (
	// FlagEmpty — пара заведена как пустая. Её надо показать «окном», а не
	// молча выбросить: студенту важно, что окно предусмотрено расписанием.
	FlagEmpty Flags = 1 << iota
	// FlagSelfWork — самоподготовка.
	FlagSelfWork
	// FlagRemote — дистанционное занятие.
	FlagRemote
	// FlagNonStudy — неучебный день.
	FlagNonStudy
)

func (f Flags) Has(x Flags) bool { return f&x != 0 }

// LessonGroup — группа, для которой опубликовано занятие преподавателя.
type LessonGroup struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Lesson — занятие в том виде, в каком оно лежит в базе и уходит в формат.
type Lesson struct {
	ID   int64  `json:"id"`
	Date string `json:"date"` // ISO "2025-09-15"

	TimeID     int64  `json:"time_id"`
	MinuteFrom int    `json:"minute_from"`
	MinuteTo   int    `json:"minute_to"`
	TimeLabel  string `json:"time_label"` // "12:20 - 13:55"

	Discipline string        `json:"discipline"`
	ClassType  string        `json:"class_type,omitempty"` // "Лек", "Пр", "Лб"
	Classroom  string        `json:"classroom,omitempty"`
	Staff      []string      `json:"staff,omitempty"`
	Groups     []LessonGroup `json:"groups,omitempty"`

	Audience   Audience `json:"audience"`
	SubgroupID int64    `json:"subgroup_id,omitempty"`
	// AudienceLabel — подпись аудитории от upstream: "ГР-12/2",
	// "Поток 1 (…)" или перечисление групп сборного потока.
	AudienceLabel string `json:"audience_label,omitempty"`

	Flags    Flags  `json:"flags,omitempty"`
	Comments string `json:"comments,omitempty"`
	Topic    string `json:"topic,omitempty"`
}

// Item — занятие в контексте дня.
type Item struct {
	Lesson
	// Number — порядковый номер пары в сетке звонков (1..7), а не индекс в
	// списке: если первая пара у группы третья по счёту, человек ждёт «3 пара».
	Number int `json:"number"`
	// GapBefore — длина окна перед этой парой в минутах. 0, если пара идёт
	// сразу за предыдущей или она первая за день.
	GapBefore int `json:"gap_before,omitempty"`
}

// Day — расписание одного дня для конкретного (группа, подгруппа).
type Day struct {
	Date    string       `json:"date"`    // ISO
	Weekday time.Weekday `json:"weekday"` // из Date, для удобства форматтера
	// Workday — день считается учебным по сетке lessonTimesEnabled. Разделять
	// «выходной» и «учебный день без пар» важно: во втором случае расписание
	// могли просто не завести, и это стоит сказать вслух.
	Workday bool   `json:"workday"`
	Items   []Item `json:"items,omitempty"`
}

// Empty сообщает, что показывать нечего.
func (d Day) Empty() bool { return len(d.Items) == 0 }

// Week — учебная неделя, понедельник … суббота.
type Week struct {
	Monday string `json:"monday"` // ISO даты понедельника
	Days   []Day  `json:"days"`
}

// Empty сообщает, что на всей неделе нет ни одного занятия.
func (w Week) Empty() bool {
	for _, d := range w.Days {
		if !d.Empty() {
			return false
		}
	}
	return true
}

// Now — ответ на вопрос «что сейчас и что дальше».
type Now struct {
	// At — момент, на который считался ответ, в часовом поясе пользователя.
	At time.Time `json:"at"`
	// Current — идущая прямо сейчас пара, если она есть.
	Current *Item `json:"current,omitempty"`
	// Next — следующая пара сегодня.
	Next *Item `json:"next,omitempty"`
	// MinutesToNext — сколько минут до начала Next.
	MinutesToNext int `json:"minutes_to_next,omitempty"`
	// RestToday — сколько пар осталось сегодня, включая текущую.
	RestToday int `json:"rest_today"`
}

// Grid — сетка звонков вуза плюс признак учебных дней недели.
type Grid struct {
	// Times — строки сетки, отсортированные по времени начала.
	Times []GridTime `json:"times"`
	// Workdays — какие дни недели учебные: ключ 1 (пн) … 7 (вс).
	Workdays map[int]bool `json:"workdays,omitempty"`
}

// GridTime — одна строка сетки звонков.
type GridTime struct {
	ID         int64  `json:"id"`
	Number     int    `json:"number"` // порядковый номер пары
	MinuteFrom int    `json:"minute_from"`
	MinuteTo   int    `json:"minute_to"`
	Label      string `json:"label"`
}

// NumberOf возвращает порядковый номер пары по её id в сетке. 0, если строка
// сетки неизвестна.
func (g Grid) NumberOf(timeID int64) int {
	for _, t := range g.Times {
		if t.ID == timeID {
			return t.Number
		}
	}
	return 0
}

// IsWorkday сообщает, учебный ли день недели. Если сетка не загружена,
// считаем учебными понедельник–субботу — так вуз работает по умолчанию.
func (g Grid) IsWorkday(wd time.Weekday) bool {
	if len(g.Workdays) == 0 {
		return wd != time.Sunday
	}
	return g.Workdays[isoWeekday(wd)]
}

// isoWeekday переводит time.Weekday (вс=0) в нумерацию источника (пн=1 … вс=7).
func isoWeekday(wd time.Weekday) int {
	if wd == time.Sunday {
		return 7
	}
	return int(wd)
}
