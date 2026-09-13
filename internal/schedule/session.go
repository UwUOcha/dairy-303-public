package schedule

import "strings"

// SessionKind — что это за испытание, если занятие вообще им является.
//
// Вуз присылает короткие подписи вида «Экз», «Зач», «Конс», и отличить их от
// обычной пары можно только по ним. Сравниваем по началу слова: подписи в
// разных семестрах пишут по-разному («Зач», «Зачет», «Диф. зач»), а начало
// у них устойчивое. Пустая строка означает обычное занятие.
//
// Список намеренно узкий. Отнести к сессии лишнее хуже, чем пропустить: экран
// сессии — это обещание «вот всё, что тебе сдавать», и мусор в нём обесценивает
// его целиком.
func SessionKind(classType string) string {
	t := strings.ToLower(strings.TrimSpace(classType))
	switch t {
	case "exam":
		return "Экзамен"
	case "credit":
		return "Зачёт"
	case "graded_credit":
		return "Дифзачёт"
	case "consultation":
		return "Консультация"
	case "coursework":
		return "Курсовая"
	}
	switch {
	case strings.HasPrefix(t, "экз"):
		return "Экзамен"
	case strings.HasPrefix(t, "диф"):
		return "Дифзачёт"
	case strings.HasPrefix(t, "зач"):
		return "Зачёт"
	case strings.HasPrefix(t, "конс"):
		return "Консультация"
	case strings.HasPrefix(t, "кп") || strings.HasPrefix(t, "курсов"):
		return "Курсовая"
	}
	return ""
}

// IsSession сообщает, относится ли занятие к сессии.
func (l Lesson) IsSession() bool { return SessionKind(l.ClassType) != "" }
