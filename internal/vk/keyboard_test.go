package vk

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/SevereCloud/vksdk/v3/object"

	"github.com/UwUOcha/dairy-303-public/internal/api"
	"github.com/UwUOcha/dairy-303-public/internal/botcore"
	"github.com/UwUOcha/dairy-303-public/internal/schedule"
	"github.com/UwUOcha/dairy-303-public/internal/store"
)

// Каждая клавиатура botcore обязана уехать во ВКонтакте целой или, если целой
// не влезает, — с честным счётчиком выброшенного. Молчаливая потеря кнопок
// здесь стоит дороже всего: человек просто не увидит свою группу.
func TestPlaceKeepsButtons(t *testing.T) {
	tests := []struct {
		name        string
		kb          *botcore.Keyboard
		wantInline  bool
		wantDropped bool
	}{
		{
			name:       "навигация по дню — пять кнопок, помещается",
			kb:         botcore.DayKeyboard(api.DayResponse{}, 0),
			wantInline: true,
		},
		{
			name:       "навигация по неделе",
			kb:         botcore.WeekKeyboard(api.WeekResponse{}, 0),
			wantInline: true,
		},
		{
			// В гостях к навигации добавляется ряд «сделать моей / к своей».
			// Три ряда и семь кнопок — инлайн-лимит ВКонтакте это держит, и
			// ронять просмотр чужой группы на нижнюю панель не придётся.
			name:       "навигация по дню в чужой группе",
			kb:         botcore.DayKeyboard(api.DayResponse{}, 232),
			wantInline: true,
		},
		{
			name:       "настройки — четыре строки",
			kb:         botcore.SettingsKeyboard(api.UserResponse{}),
			wantInline: true,
		},
		{
			// Вкладка уведомлений — шесть строк ровно в потолок инлайна.
			// Уедет она на нижнюю панель — и перестанет редактироваться на
			// месте, а каждое нажатие тумблера начнёт плодить сообщения.
			name:       "вкладка уведомлений",
			kb:         botcore.NotifyKeyboard(store.User{}.WithDefaults(180)),
			wantInline: true,
		},
		{
			name:       "выбор времени рассылки",
			kb:         botcore.TimeKeyboard("m", 7*60),
			wantInline: true,
		},
		{
			// Девять институтов — девять строк при потолке инлайна в шесть.
			// Ужать их в две кнопки в строке нельзя: названия под полсотни
			// символов, читать такое на телефоне невозможно.
			name:       "институты — не помещается в инлайн",
			kb:         botcore.DepartmentsKeyboard(departments(), 0),
			wantInline: false,
		},
		{
			name:       "результаты поиска — восемь групп и кнопка обзора",
			kb:         botcore.GroupsKeyboard(groups(8), true),
			wantInline: false,
		},
		{
			name:       "группы курса — влезают на панель",
			kb:         botcore.BrowseGroupsKeyboard(1, 1, groups(18), 0),
			wantInline: false,
		},
		{
			// Реальный максимум по базе вуза: 42 группы на первом курсе.
			// Целиком они на панель не влезали никогда — теперь курс приезжает
			// страницами, и каждая обязана доехать без потерь.
			name:       "самый длинный курс — первая страница",
			kb:         botcore.BrowseGroupsKeyboard(1, 1, groups(42), 0),
			wantInline: false,
		},
		{
			name:       "самый длинный курс — последняя страница",
			kb:         botcore.BrowseGroupsKeyboard(1, 1, groups(42), 2),
			wantInline: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows := buttonRows(tt.kb)
			p := place(rows, nil, panelNone)

			kb := p.inline
			if tt.wantInline {
				if p.panel != nil {
					t.Fatal("клавиатура уехала на панель, хотя помещалась в сообщение")
				}
			} else {
				if p.inline != nil {
					t.Fatal("клавиатура объявлена инлайновой, хотя не помещается")
				}
				kb = p.panel
			}
			if kb == nil {
				t.Fatal("клавиатура потерялась целиком")
			}
			checkLimits(t, kb)

			if got := p.dropped > 0; got != tt.wantDropped {
				t.Errorf("выброшено кнопок %d, ожидалось %v", p.dropped, tt.wantDropped)
			}
			if p.dropped == 0 && buttons(kb) != countButtons(rows) {
				t.Errorf("кнопок доехало %d из %d, а счётчик потерь молчит",
					buttons(kb), countButtons(rows))
			}
		})
	}
}

// Самая дорогая проверка этого файла: с курса не должна пропасть ни одна
// группа. Раньше сорок вторая просто отбрасывалась вместе с подписью «не
// поместилось», и выбрать её кнопкой было нельзя вовсе. Теперь курс приезжает
// страницами — и тест обходит их так же, как человек: нажимая «вперёд».
func TestBrowsePagesCoverWholeCourse(t *testing.T) {
	const total = 42
	all := groups(total)

	seen := make(map[string]bool, total)
	next := "" // пусто — первая страница
	for step := 0; ; step++ {
		if step > total {
			t.Fatal("листание зациклилось, не показав курс целиком")
		}
		rows := buttonRows(botcore.BrowseGroupsKeyboard(1, 1, all, pageOf(t, next)))

		p := place(rows, nil, panelNone)
		if p.panel == nil {
			t.Fatalf("страница %q не собралась", next)
		}
		if p.dropped != 0 {
			t.Fatalf("страница %q потеряла %d кнопок", next, p.dropped)
		}
		checkLimits(t, p.panel)

		forward := ""
		for _, row := range rows {
			for _, b := range row {
				switch {
				case strings.HasPrefix(b.Data, "g:"):
					seen[b.Data] = true
				case strings.Contains(b.Label, "▶️"):
					forward = b.Data
				}
			}
		}
		if forward == "" {
			t.Fatal("на странице нет кнопки «вперёд»")
		}
		if next = forward; strings.HasSuffix(next, "p0") {
			break // круг замкнулся — курс показан целиком
		}
	}

	if len(seen) != total {
		t.Errorf("по страницам доехало %d групп из %d", len(seen), total)
	}
}

// Обрезая список, нельзя выкидывать последнюю строку: в ней у botcore лежит
// навигация, и без неё человек застрянет на обрезанном экране.
//
// Клавиатура здесь синтетическая. Настоящие списки botcore теперь листаются
// страницами и в панель влезают, но обрезка остаётся последней сеткой под
// ними: лимиты ВКонтакте считаются в двух местах, и разъехаться они могут
// молча. Пусть сетка будет проверена.
func TestFitPanelKeepsNavigation(t *testing.T) {
	rows := buttonRows(oversized())
	back := rows[len(rows)-1][0]

	fitted, dropped := fitPanel(rows)
	if dropped == 0 {
		t.Fatal("двадцать строк обязаны не влезть в панель")
	}
	if len(fitted) > maxPanelRows || countButtons(fitted) > maxPanelButtons {
		t.Errorf("после обрезки %d строк и %d кнопок — всё ещё сверх лимита",
			len(fitted), countButtons(fitted))
	}
	last := fitted[len(fitted)-1]
	if len(last) != 1 || last[0].Data != back.Data {
		t.Errorf("последняя строка = %+v, ожидалась навигация %+v", last, back)
	}
	if countButtons(fitted)+dropped != countButtons(rows) {
		t.Errorf("счётчик потерь врёт: %d + %d != %d",
			countButtons(fitted), dropped, countButtons(rows))
	}
}

// Панель одна на диалог, поэтому занявший её список должен удерживать и
// следующие экраны — иначе институты остались бы висеть под выбором курса.
func TestPlaceHoldsPanelUntilMenu(t *testing.T) {
	courses := buttonRows(botcore.CoursesKeyboard(1, []int{1, 2, 3, 4}))

	if p := place(courses, nil, panelNone); p.inline == nil {
		t.Error("на чистой панели короткая клавиатура должна быть инлайновой")
	}

	p := place(courses, nil, panelPicker)
	if p.panel == nil || p.want != panelPicker {
		t.Error("пока панель занята списком, следующий экран обязан её сменить")
	}

	// Ответ с меню возвращает всё на место: сообщение снова инлайновое, а меню
	// уезжает отдельным сообщением и освобождает панель.
	p = place(courses, botcore.MainMenu(), panelPicker)
	if p.inline == nil || p.panel != nil {
		t.Error("ответ с меню должен вернуть инлайн-клавиатуру")
	}
}

// Меню без кнопок цепляется прямо к сообщению — но только если панель ещё не
// занята им же, иначе бот слал бы «Меню всегда внизу» на каждый чих.
func TestPlaceAttachesMenu(t *testing.T) {
	p := place(nil, botcore.MainMenu(), panelNone)
	if p.panel == nil || p.want != panelMenu {
		t.Fatal("меню не прицепилось к сообщению без кнопок")
	}
	if p.panel.Inline {
		t.Error("меню обязано быть нижней панелью, а не инлайном")
	}

	if p := place(nil, botcore.MainMenu(), panelMenu); p.panel != nil {
		t.Error("меню отправлено повторно, хотя уже лежит на панели")
	}
}

// Кнопка должна вернуться теми же данными, что положил botcore: на этом
// протоколе держится вся навигация, и он общий с телеграмом.
func TestPayloadRoundTrip(t *testing.T) {
	kb := keyboard(buttonRows(botcore.DayKeyboard(api.DayResponse{
		Context: api.Context{Today: "2025-09-19"},
		Day:     schedule.Day{Date: "2025-09-19"},
		Prev:    "2025-09-18",
		Next:    "2025-09-20",
	}, 0)), true)

	raw := kb.Buttons[0][0].Action.Payload
	if !json.Valid([]byte(raw)) || raw[0] != '{' {
		// ВКонтакте принимает только payload-объект длиной до 255 символов.
		t.Fatalf("payload = %q, ожидался JSON-объект", raw)
	}
	if len(raw) > 255 {
		t.Errorf("payload длиной %d — ВКонтакте столько не примет", len(raw))
	}
	cb, command := parsePayload([]byte(raw))
	if cb != "d:2025-09-18" || command != "" {
		t.Errorf("разобрано cb=%q command=%q", cb, command)
	}
}

func TestParsePayload(t *testing.T) {
	tests := []struct {
		name        string
		raw         string
		wantCB      string
		wantCommand string
	}{
		{name: "пусто", raw: ""},
		{name: "наша кнопка", raw: `{"c":"br:1:2"}`, wantCB: "br:1:2"},
		{name: "кнопка «Начать»", raw: `{"command":"start"}`, wantCommand: "start"},
		{
			name:        "старый клиент",
			raw:         `{"command":"not_supported_button"}`,
			wantCommand: object.CommandNotSupportedButton,
		},
		// Чужой payload — например, от прошлой версии бота. Падать нельзя.
		{name: "не объект", raw: `"просто строка"`},
		{name: "мусор", raw: `{{{`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cb, command := parsePayload([]byte(tt.raw))
			if cb != tt.wantCB || command != tt.wantCommand {
				t.Errorf("parsePayload(%q) = %q, %q; ожидалось %q, %q",
					tt.raw, cb, command, tt.wantCB, tt.wantCommand)
			}
		})
	}
}

// Строку длиннее пяти кнопок ВКонтакте не принимает вовсе — режем сами.
func TestButtonRowsSplitsWideRows(t *testing.T) {
	var row []botcore.Button
	for i := range 12 {
		row = append(row, botcore.Button{Label: strconv.Itoa(i), Data: strconv.Itoa(i)})
	}
	rows := buttonRows(&botcore.Keyboard{Rows: [][]botcore.Button{row}})

	if countButtons(rows) != 12 {
		t.Errorf("кнопок после разрезания %d, было 12", countButtons(rows))
	}
	for i, r := range rows {
		if len(r) > maxButtonsInRow {
			t.Errorf("строка %d длиной %d — сверх лимита", i, len(r))
		}
	}
}

func TestMenuKeyboardIsPersistent(t *testing.T) {
	kb := menuKeyboard(botcore.MainMenu())
	if kb == nil {
		t.Fatal("меню не собралось")
	}
	if kb.OneTime {
		t.Error("меню одноразовое — оно должно оставаться под рукой")
	}
	checkLimits(t, kb)
	for _, row := range kb.Buttons {
		for _, b := range row {
			// botcore разбирает кнопки меню по подписи, как обычный текст.
			if b.Action.Type != object.ButtonText {
				t.Errorf("кнопка меню %q типа %q, ожидался %q",
					b.Action.Label, b.Action.Type, object.ButtonText)
			}
		}
	}
}

func checkLimits(t *testing.T, kb *object.MessagesKeyboard) {
	t.Helper()

	rows, total := maxPanelRows, maxPanelButtons
	if kb.Inline {
		rows, total = maxInlineRows, maxInlineButtons
	}
	if len(kb.Buttons) > rows {
		t.Errorf("строк %d при потолке %d", len(kb.Buttons), rows)
	}
	if buttons(kb) > total {
		t.Errorf("кнопок %d при потолке %d", buttons(kb), total)
	}
	for i, row := range kb.Buttons {
		if len(row) > maxButtonsInRow {
			t.Errorf("в строке %d кнопок %d при потолке %d", i, len(row), maxButtonsInRow)
		}
		for j, b := range row {
			if n := labelLen(b.Action.Label); n > maxLabelLen {
				t.Errorf("подпись кнопки [%d][%d] длиной %d при потолке %d: %q",
					i, j, n, maxLabelLen, b.Action.Label)
			}
		}
	}
}

func buttons(kb *object.MessagesKeyboard) int {
	n := 0
	for _, row := range kb.Buttons {
		n += len(row)
	}
	return n
}

// departments — настоящие названия из базы вуза. Синтетические сюда не
// годятся: ровно на этом списке ВКонтакте отверг клавиатуру целиком из-за
// подписи в 51 символ, и тест обязан ловить возврат такой ошибки.
func departments() []store.Department {
	names := []string{
		"Инженерно-технологический институт",
		"Институт естествознания",
		"Институт искусств и социокультурного проектирования",
		"Институт истории и права",
		"Институт лингвистики и мировых языков",
		"Институт педагогики",
		"Институт психологии",
		"Институт филологии и массмедиа",
		"Учебный факультет",
	}
	out := make([]store.Department, len(names))
	for i, name := range names {
		out[i] = store.Department{ID: int64(i + 1), Name: name}
	}
	return out
}

func groups(n int) []store.Group {
	out := make([]store.Group, n)
	for i := range out {
		out[i] = store.Group{ID: int64(i + 1), Name: "ГР-" + strconv.Itoa(i+1), Course: 1}
	}
	return out
}

// pageOf достаёт номер страницы из данных кнопки «вперёд»: «br:1:1:p2» — это
// третья страница. Пустые данные — первая.
func pageOf(t *testing.T, data string) int {
	t.Helper()
	if data == "" {
		return 0
	}
	parts := strings.Split(data, ":")
	page, err := strconv.Atoi(strings.TrimPrefix(parts[len(parts)-1], "p"))
	if err != nil {
		t.Fatalf("кнопка листания ведёт в %q", data)
	}
	return page
}

// oversized — заведомо не влезающая в панель клавиатура: двадцать строк по две
// кнопки и строка навигации под ними.
func oversized() *botcore.Keyboard {
	k := &botcore.Keyboard{}
	for i := range 20 {
		k.Rows = append(k.Rows, []botcore.Button{
			{Label: "А" + strconv.Itoa(2*i), Data: "g:" + strconv.Itoa(2*i)},
			{Label: "А" + strconv.Itoa(2*i+1), Data: "g:" + strconv.Itoa(2*i+1)},
		})
	}
	k.Rows = append(k.Rows, []botcore.Button{{Label: "◀️ К курсам", Data: "br:1"}})
	return k
}

// Экран со списком во ВКонтакте обязан говорить, куда уехали кнопки.
//
// Это не украшение. Дерево институтов не помещается в сообщение и целиком
// живёт на нижней панели диалога, к сообщению не привязанной. Абзац «Выбери
// свою», под которым в телеграме кнопки, здесь висит сам по себе — и человек,
// не нашедший связи, пишет ответ словами.
func TestPanelHint(t *testing.T) {
	deps := make([]store.Department, 9)
	for i := range deps {
		deps[i] = store.Department{ID: int64(i + 1), Name: "Институт " + strconv.Itoa(i+1)}
	}
	subs := []store.Subgroup{
		{ID: 707, GroupID: 545, Name: "ГР-22/1"},
		{ID: 708, GroupID: 545, Name: "ГР-22/2"},
	}

	tests := []struct {
		name  string
		kb    *botcore.Keyboard
		menu  *botcore.Menu
		panel string
		want  bool
	}{
		{
			name:  "список институтов не влезает в сообщение",
			kb:    botcore.DepartmentsKeyboard(deps, 0),
			panel: panelNone,
			want:  true,
		},
		{
			// Три кнопки влезли бы в сообщение, но панель уже занята списком:
			// вернуть их в инлайн нельзя, не оставив под ним живой чужой
			// список. Значит и подпись нужна ровно та же.
			name:  "выбор подгруппы следом за списком групп",
			kb:    botcore.SubgroupsKeyboard(subs, 0),
			panel: panelPicker,
			want:  true,
		},
		{
			name:  "кнопки в самом сообщении — искать нечего",
			kb:    botcore.DayKeyboard(api.DayResponse{}, 0),
			panel: panelNone,
			want:  false,
		},
		{
			// Нижнее меню приезжает со своей подписью «Меню всегда внизу».
			name:  "постоянное меню",
			kb:    nil,
			menu:  botcore.MainMenu(),
			panel: panelNone,
			want:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := place(buttonRows(tt.kb), tt.menu, tt.panel)
			got := strings.Contains(compose("Выбери свою:", p), panelHint)
			if got != tt.want {
				t.Errorf("подсказка про нижнюю панель = %v, ожидалось %v", got, tt.want)
			}
		})
	}
}
