package vk

import (
	"encoding/json"

	"github.com/SevereCloud/vksdk/v3/object"

	"github.com/UwUOcha/dairy-303-public/internal/botcore"
)

// Лимиты клавиатур ВКонтакте.
//
// Их две, и они разные. Инлайн-клавиатура живёт внутри сообщения и позволяет
// его редактировать, но вмещает всего десять кнопок. Нижняя панель вмещает
// сорок, зато она одна на весь диалог и к сообщению не привязана. Из этой
// разницы растёт вся раскладка ниже: телеграму хватало одного вида кнопок, а
// здесь под каждый экран приходится выбирать.
const (
	maxButtonsInRow  = 5
	maxInlineRows    = 6
	maxInlineButtons = 10
	maxPanelRows     = 10
	maxPanelButtons  = 40
)

// emptyInline — клавиатура, которой снимают кнопки с отредактированного
// сообщения. Не передать её значит оставить кнопки прошлого экрана висеть под
// новым текстом.
const emptyInline = `{"inline":true,"buttons":[]}`

// payload — то, что уезжает в кнопке и возвращается при нажатии.
//
// ВКонтакте требует от payload быть JSON-объектом (не строкой) длиной до 255
// символов. Наши callback-данные — это десяток байт вида «d:2026-09-19», так
// что ограничение не жмёт; общий с телеграмом протокол кнопок не меняется.
type payload struct {
	CB string `json:"c,omitempty"`
}

// parsePayload достаёт из нажатой кнопки наши callback-данные и служебную
// команду ВКонтакте.
//
// Команды присылает сам мессенджер: «start» — это кнопка «Начать» в новом
// диалоге, «not_supported_button» — старый клиент, не умеющий такие кнопки.
func parsePayload(raw []byte) (cb, command string) {
	if len(raw) == 0 {
		return "", ""
	}
	var p struct {
		CB      string `json:"c"`
		Command string `json:"command"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		// Чужой payload — например, от прошлой версии бота. Не повод падать.
		return "", ""
	}
	return p.CB, p.Command
}

// buttonRows раскладывает клавиатуру botcore в строки, которые ВКонтакте
// вообще способен показать: больше пяти кнопок в строке он не принимает.
func buttonRows(k *botcore.Keyboard) [][]botcore.Button {
	if k == nil {
		return nil
	}
	var rows [][]botcore.Button
	for _, row := range k.Rows {
		for len(row) > maxButtonsInRow {
			rows = append(rows, row[:maxButtonsInRow])
			row = row[maxButtonsInRow:]
		}
		if len(row) > 0 {
			rows = append(rows, row)
		}
	}
	return rows
}

// fitsInline сообщает, помещается ли раскладка в инлайн-клавиатуру.
//
// Пустая раскладка помещается: сообщению без кнопок инлайн-режим не мешает.
func fitsInline(rows [][]botcore.Button) bool {
	return len(rows) <= maxInlineRows && countButtons(rows) <= maxInlineButtons
}

// fitPanel урезает раскладку до лимитов нижней панели и говорит, сколько
// кнопок пришлось выбросить.
//
// Сетка последней надежды. Раньше сюда приезжали 42 группы первого курса и
// теряли лишние кнопки; теперь botcore режет длинные списки на страницы под
// эти же лимиты, и обрезать нечего. Но считаются лимиты в двух местах, и
// разъехаться они могут молча — пусть лучше список приедет неполным с честной
// подписью, чем ВКонтакте отвергнет клавиатуру целиком.
//
// Последняя строка сохраняется всегда — в ней у botcore лежит навигация
// («◀️ К курсам»), без которой человек застрянет на обрезанном списке.
func fitPanel(rows [][]botcore.Button) ([][]botcore.Button, int) {
	total := countButtons(rows)
	if len(rows) <= maxPanelRows && total <= maxPanelButtons {
		return rows, 0
	}

	last := rows[len(rows)-1]
	kept := make([][]botcore.Button, 0, maxPanelRows)
	used := 0
	for _, row := range rows[:len(rows)-1] {
		if len(kept) >= maxPanelRows-1 || used+len(row)+len(last) > maxPanelButtons {
			break
		}
		kept = append(kept, row)
		used += len(row)
	}
	return append(kept, last), total - used - len(last)
}

// keyboard собирает клавиатуру ВКонтакте из раскладки botcore.
//
// Кнопки везде callback: нажатие не оставляет в переписке сообщения с подписью
// кнопки, а значит листание расписания не засоряет диалог — ровно то же, ради
// чего в телеграме сообщение редактируется, а не отправляется заново.
func keyboard(rows [][]botcore.Button, inline bool) *object.MessagesKeyboard {
	if len(rows) == 0 {
		return nil
	}
	kb := object.NewMessagesKeyboard(false)
	kb.Inline = object.BaseBoolInt(inline)
	for _, row := range rows {
		kb.AddRow()
		for _, b := range row {
			// Что делает кнопка, знает payload, а не подпись, — поэтому её
			// можно безболезненно укоротить под лимит платформы.
			kb.AddCallbackButton(label(b.Label), payload{CB: b.Data}, object.Secondary)
		}
	}
	return kb
}

// placement — куда легли кнопки одного ответа.
type placement struct {
	// inline — клавиатура внутри сообщения; с ней сообщение можно править.
	inline *object.MessagesKeyboard
	// panel — клавиатура на нижней панели диалога.
	panel *object.MessagesKeyboard
	// want — чем панель занята после этого ответа; panelNone — не трогаем.
	want string
	// dropped — сколько кнопок не поместилось.
	dropped int
}

// place решает, куда положить кнопки ответа.
//
// Правило одно: инлайн, пока помещается. Не поместилось — уходит на нижнюю
// панель, и там же остаются следующие экраны, пока не придёт ответ с меню.
// Без этой оговорки панель со списком институтов висела бы под сообщением
// «Выбери курс:» — и жать её было бы можно.
func place(rows [][]botcore.Button, menu *botcore.Menu, panel string) placement {
	switch {
	case len(rows) == 0:
		// Кнопок нет — меню можно прицепить прямо к этому сообщению.
		if menu == nil || panel == panelMenu {
			return placement{want: panelNone}
		}
		return placement{panel: menuKeyboard(menu), want: panelMenu}

	case fitsInline(rows) && !(panel == panelPicker && menu == nil):
		return placement{inline: keyboard(rows, true), want: panelNone}

	default:
		rows, dropped := fitPanel(rows)
		return placement{panel: keyboard(rows, false), want: panelPicker, dropped: dropped}
	}
}

// menuKeyboard переводит постоянное меню botcore в нижнюю панель.
//
// Кнопки меню — текстовые: botcore разбирает их по подписи, как обычные
// сообщения, и это же делает их понятными клиенту любой давности.
//
// Подписи здесь, в отличие от остальных кнопок, не укорачиваются: они и есть
// протокол, и тихо обрезанная подпись перестала бы совпадать с тем, что ищет
// маршрутизатор. Если меню однажды перерастёт сорок символов, честнее получить
// отказ от ВКонтакте, чем молча сломать кнопку.
func menuKeyboard(m *botcore.Menu) *object.MessagesKeyboard {
	if m == nil || len(m.Rows) == 0 {
		return nil
	}
	kb := object.NewMessagesKeyboard(false)
	for _, row := range m.Rows {
		kb.AddRow()
		for _, label := range row {
			kb.AddTextButton(label, payload{}, object.Secondary)
		}
	}
	return kb
}

func countButtons(rows [][]botcore.Button) int {
	n := 0
	for _, row := range rows {
		n += len(row)
	}
	return n
}
